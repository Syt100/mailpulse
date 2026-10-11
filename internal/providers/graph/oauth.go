package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	msalerrors "github.com/AzureAD/microsoft-authentication-library-for-go/apps/errors"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var scopes = []string{"https://graph.microsoft.com/Mail.ReadBasic", "offline_access"}

type AccountGetter interface {
	GetAccount(context.Context, string) (domain.Account, error)
}
type OAuthSecrets interface {
	domain.SecretStore
	SaveAuthorization(context.Context, string, string, []byte) error
}
type OAuth struct {
	Secrets  OAuthSecrets
	Accounts AccountGetter
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	HTTP     *http.Client
}

func (o *OAuth) lock(id string) *sync.Mutex {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.locks == nil {
		o.locks = map[string]*sync.Mutex{}
	}
	if o.locks[id] == nil {
		o.locks[id] = &sync.Mutex{}
	}
	return o.locks[id]
}
func (o *OAuth) client(a domain.Account, cacheOpt public.Option) (public.Client, error) {
	var cfg struct {
		ClientID  string `json:"client_id"`
		Authority string `json:"authority"`
	}
	if json.Unmarshal(a.Settings, &cfg) != nil || cfg.ClientID == "" {
		return public.Client{}, fmt.Errorf("invalid OAuth configuration")
	}
	authority := "https://login.microsoftonline.com/consumers"
	if cfg.Authority != "" && cfg.Authority != "consumers" {
		return public.Client{}, fmt.Errorf("organization accounts are outside v0.1 scope")
	}
	var h http.Client
	if o.HTTP != nil {
		h = *o.HTTP
	}
	if h.Timeout == 0 {
		h.Timeout = 30 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return public.New(cfg.ClientID, public.WithAuthority(authority), cacheOpt, public.WithHTTPClient(&h))
}
func (o *OAuth) AccessToken(ctx context.Context, id string) (string, error) {
	l := o.lock(id)
	l.Lock()
	defer l.Unlock()
	a, e := o.Accounts.GetAccount(ctx, id)
	if e != nil {
		return "", oauthFailure(e)
	}
	identity, e := o.Secrets.Load(ctx, "identity:"+id)
	if e != nil {
		return "", oauthFailure(e)
	}
	if len(identity) == 0 {
		return "", &domain.ProviderError{Code: "authentication_required"}
	}
	client, e := o.client(a, public.WithCache(&CacheAdapter{Secrets: o.Secrets, AccountID: id}))
	if e != nil {
		return "", &domain.ProviderError{Code: "authentication_required"}
	}
	accounts, e := client.Accounts(ctx)
	if e != nil {
		return "", oauthFailure(e)
	}
	for _, account := range accounts {
		if account.HomeAccountID != string(identity) {
			continue
		}
		result, e := client.AcquireTokenSilent(ctx, scopes, public.WithSilentAccount(account))
		if e != nil {
			return "", oauthFailure(e)
		}
		return result.AccessToken, nil
	}
	return "", &domain.ProviderError{Code: "authentication_required"}
}

// Only permanent authentication rejection parks an account. Network and cache
// failures remain retryable, without copying SDK errors containing credentials.
func oauthFailure(err error) error {
	p := &domain.ProviderError{Code: "transient"}
	if errors.Is(err, domain.ErrNotFound) || err.Error() == "no token found" {
		p.Code = "authentication_required"
		return p
	}
	var call msalerrors.CallErr
	if !errors.As(err, &call) || call.Resp == nil {
		return p
	}
	r := call.Resp
	if r.StatusCode == 429 {
		p.Code = "throttled"
		p.RetryAfter = retryAfter(r.Header.Get("Retry-After"), time.Now())
		return p
	}
	if r.StatusCode == 401 || r.StatusCode == 403 {
		p.Code = "authentication_required"
		return p
	}
	if r.StatusCode != 400 || r.Body == nil {
		return p
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body) != nil {
		return p
	}
	switch body.Error {
	case "invalid_grant", "interaction_required", "consent_required", "login_required", "invalid_client", "unauthorized_client":
		p.Code = "authentication_required"
	}
	return p
}
func (o *OAuth) Begin(ctx context.Context, a domain.Account) (app.DeviceGrant, error) {
	if a.Provider != "outlook" {
		return app.DeviceGrant{}, fmt.Errorf("account is not Outlook")
	}
	staging := &stagingCache{}
	client, e := o.client(a, public.WithCache(staging))
	if e != nil {
		return app.DeviceGrant{}, e
	}
	device, e := client.AcquireTokenByDeviceCode(ctx, scopes)
	if e != nil {
		return app.DeviceGrant{}, fmt.Errorf("device authorization failed")
	}
	grant := app.DeviceGrant{Code: device.Result.UserCode, VerificationURL: device.Result.VerificationURL, ExpiresAt: device.Result.ExpiresOn}
	grant.Complete = func(ctx context.Context) error {
		result, e := device.AuthenticationResult(ctx)
		if e != nil {
			return fmt.Errorf("authorization failed")
		}
		l := o.lock(a.ID)
		l.Lock()
		defer l.Unlock()
		current, e := o.Accounts.GetAccount(ctx, a.ID)
		if e != nil {
			return e
		}
		identity, e := o.Secrets.Load(ctx, "identity:"+a.ID)
		if e != nil && e != domain.ErrNotFound {
			return e
		}
		if len(identity) > 0 {
			if string(identity) != result.Account.HomeAccountID {
				return fmt.Errorf("authorized account does not match existing identity")
			}
		} else if !strings.EqualFold(result.Account.PreferredUsername, current.Address) {
			return fmt.Errorf("authorized address does not match configured primary address")
		}
		if result.Account.HomeAccountID == "" || len(staging.raw) == 0 {
			return fmt.Errorf("incomplete authorization result")
		}
		return o.Secrets.SaveAuthorization(ctx, a.ID, result.Account.HomeAccountID, staging.raw)
	}
	return grant, nil
}
