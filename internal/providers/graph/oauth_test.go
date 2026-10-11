package graph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Syt100/mailpulse/internal/domain"
)

type oauthAccounts struct{}

func (oauthAccounts) GetAccount(context.Context, string) (domain.Account, error) {
	return domain.Account{ID: "a", Provider: "outlook", Address: "fake@outlook.com", Settings: json.RawMessage(`{"client_id":"fake-client"}`)}, nil
}

type oauthStore struct {
	secrets
	loadErr error
}

func (s *oauthStore) Load(ctx context.Context, id string) ([]byte, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.secrets.Load(ctx, id)
}
func (s *oauthStore) SaveAuthorization(ctx context.Context, id, identity string, raw []byte) error {
	return errors.New("not used by refresh test")
}

type oauthTransport func(*http.Request) (*http.Response, error)

func (f oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMSALRefreshFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		status               int
		body, code           string
		network, saveFailure bool
		after                time.Duration
	}{
		{name: "network", network: true, code: "transient"},
		{name: "service outage", status: 503, body: `{"error":"temporarily_unavailable"}`, code: "transient"},
		{name: "throttled", status: 429, body: `{"error":"temporarily_unavailable"}`, code: "throttled", after: 10 * time.Second},
		{name: "revoked grant", status: 400, body: `{"error":"invalid_grant","error_description":"sensitive text"}`, code: "authentication_required"},
		{name: "cache write failure", status: 200, saveFailure: true, code: "transient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &oauthStore{secrets: secrets{data: map[string][]byte{}}}
			calls := 0
			initial := true
			transport := oauthTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "login.microsoftonline.com" {
					t.Fatalf("unexpected OAuth host %s", r.URL.Host)
				}
				status, body := 200, ""
				switch {
				case strings.Contains(r.URL.Path, "openid-configuration"):
					body = `{"token_endpoint":"https://login.microsoftonline.com/consumers/oauth2/v2.0/token","authorization_endpoint":"https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize","issuer":"https://login.microsoftonline.com/consumers/v2.0"}`
				case strings.Contains(r.URL.Path, "discovery/instance"):
					body = `{"tenant_discovery_endpoint":"https://login.microsoftonline.com/consumers/v2.0/.well-known/openid-configuration","metadata":[{"preferred_network":"login.microsoftonline.com","preferred_cache":"login.microsoftonline.com","aliases":["login.microsoftonline.com"]}]}`
				case strings.HasSuffix(r.URL.Path, "/token"):
					calls++
					if !initial && tc.network {
						return nil, errors.New("temporary network failure")
					}
					status, body = tc.status, tc.body
					if initial {
						status = 200
					}
					if status == 200 {
						payload := base64.RawURLEncoding.EncodeToString([]byte(`{"aud":"fake-client","tid":"consumers","preferred_username":"fake@outlook.com","oid":"uid","exp":4102444800}`))
						clientInfo := base64.RawURLEncoding.EncodeToString([]byte(`{"uid":"uid","utid":"utid"}`))
						b, _ := json.Marshal(map[string]any{"access_token": "fake-access", "refresh_token": "fake-refresh-new", "expires_in": 3600, "token_type": "Bearer", "scope": strings.Join(scopes, " "), "client_info": clientInfo, "id_token": "header." + payload + ".signature"})
						body = string(b)
					}
				default:
					t.Fatalf("unexpected OAuth request %s", r.URL.Path)
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}, "Retry-After": []string{"10"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			o := &OAuth{Accounts: oauthAccounts{}, Secrets: store, HTTP: &http.Client{Transport: transport}}
			a, _ := o.Accounts.GetAccount(context.Background(), "a")
			client, e := o.client(a, public.WithCache(&CacheAdapter{Secrets: store, AccountID: "a"}))
			if e != nil {
				t.Fatal(e)
			}
			result, e := client.AcquireTokenByAuthCode(context.Background(), "fake-code", "https://localhost", scopes)
			if e != nil {
				t.Fatal(e)
			}
			store.data["identity:a"] = []byte(result.Account.HomeAccountID)
			var cacheData map[string]any
			if e = json.Unmarshal(store.data["cache:a"], &cacheData); e != nil {
				t.Fatal(e)
			}
			delete(cacheData, "AccessToken")
			store.data["cache:a"], _ = json.Marshal(cacheData)
			initial = false
			calls = 0
			store.fail = tc.saveFailure
			token, e := o.AccessToken(context.Background(), "a")
			var pe *domain.ProviderError
			if calls == 0 {
				t.Fatal("actual MSAL did not attempt token refresh")
			}
			if token != "" || !errors.As(e, &pe) || pe.Code != tc.code || pe.RetryAfter < tc.after {
				t.Fatalf("token=%q error=%v", token, e)
			}
			if strings.Contains(e.Error(), "sensitive") || strings.Contains(e.Error(), "fake-refresh") {
				t.Fatal("raw OAuth error leaked")
			}
		})
	}
}

func TestIdentityStorageFailureRetries(t *testing.T) {
	o := &OAuth{Accounts: oauthAccounts{}, Secrets: &oauthStore{loadErr: errors.New("storage unavailable")}}
	_, e := o.AccessToken(context.Background(), "a")
	var pe *domain.ProviderError
	if !errors.As(e, &pe) || pe.Code != "transient" {
		t.Fatalf("storage failure parked account: %v", e)
	}
}
