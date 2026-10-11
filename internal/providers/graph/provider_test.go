package graph

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Syt100/mailpulse/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type token struct{}

func (token) AccessToken(context.Context, string) (string, error) { return "fake-token", nil }
func server(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	p := NewProvider(token{}, s.Client())
	p.baseURL = s.URL
	return p
}

func TestGraphRetryAfterHTTPDate(t *testing.T) {
	until := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Second)
	p := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", until.Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := p.Scan(context.Background(), domain.Account{ID: "a"}, domain.Cursor{})
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Code != "throttled" || pe.RetryAfter < 110*time.Second || pe.RetryAfter > 2*time.Minute {
		t.Fatalf("HTTP-date throttle was not respected: %v", err)
	}
}
func TestGraphInitialPaginationSilent(t *testing.T) {
	var p *Provider
	p = server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Prefer") != `IdType="ImmutableId"` {
			t.Error("missing immutable ID")
		}
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.deltaLink": p.baseURL + "/delta?token=x"})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{"id": "old", "subject": "old"}}, "@odata.nextLink": p.baseURL + "/delta?page=2"})
		}
	})
	a := domain.Account{ID: "a"}
	b, e := p.Scan(context.Background(), a, domain.Cursor{})
	if e != nil || !b.Baseline || !b.More || b.Next.Initialized || len(b.Events) != 1 {
		t.Fatalf("%+v %v", b, e)
	}
	b, e = p.Scan(context.Background(), a, b.Next)
	if e != nil || !b.Baseline || b.More || !b.Next.Initialized {
		t.Fatalf("%+v %v", b, e)
	}
}
func TestGraphPageReplay(t *testing.T) {
	p := server(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{"id": "CaseSensitive"}}, "@odata.deltaLink": "https://graph.microsoft.com/delta?token=x"})
	})
	a := domain.Account{ID: "a"}
	b1, _ := p.Scan(context.Background(), a, domain.Cursor{})
	b2, _ := p.Scan(context.Background(), a, domain.Cursor{})
	if b1.Events[0].Key != b2.Events[0].Key {
		t.Fatal("unstable key")
	}
}
func TestDeltaExpiredNeedsResync(t *testing.T) {
	p := server(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) })
	_, e := p.Scan(context.Background(), domain.Account{ID: "a"}, domain.Cursor{})
	var pe *domain.ProviderError
	if !errors.As(e, &pe) || pe.Code != "needs_resync" {
		t.Fatal(e)
	}
}
func TestMaliciousNextLink(t *testing.T) {
	p := server(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "@odata.nextLink": "https://attacker.invalid/steal"})
	})
	_, e := p.Scan(context.Background(), domain.Account{ID: "a"}, domain.Cursor{})
	if e == nil {
		t.Fatal("malicious next link accepted")
	}
}
func TestGraphRedirectDoesNotLeakToken(t *testing.T) {
	var received bool
	evil := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true }))
	defer evil.Close()
	p := server(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, evil.URL, 302) })
	_, e := p.Scan(context.Background(), domain.Account{ID: "a"}, domain.Cursor{})
	if e == nil || received {
		t.Fatal("followed redirect")
	}
}
func TestIgnoreRemovedAndStateChanges(t *testing.T) {
	p := server(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{"id": "removed", "@removed": map[string]string{"reason": "deleted"}}, {"id": "flags", "isRead": true}, {"id": "new", "subject": "hello", "from": map[string]any{"emailAddress": map[string]string{"address": "a@example.com"}}}}, "@odata.deltaLink": "https://graph.microsoft.com/delta?token=x"})
	})
	b, e := p.Scan(context.Background(), domain.Account{ID: "a"}, domain.Cursor{Initialized: true})
	if e != nil || len(b.Events) != 1 || b.Events[0].Subject != "hello" {
		t.Fatalf("%+v %v", b, e)
	}
}
