package ntfy

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

func TestUTF8JSONPublish(t *testing.T) {
	var data map[string]any
	var auth string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&data)
		w.WriteHeader(200)
	}))
	defer s.Close()
	c, e := NewClient(s.URL, "emails", func(context.Context) (string, error) { return "fake-token", nil }, s.Client())
	if e != nil {
		t.Fatal(e)
	}
	e = c.Publish(context.Background(), domain.Notification{Title: "中文", Message: "通知", Priority: 3})
	if e != nil || data["title"] != "中文" || data["topic"] != "emails" || auth != "Bearer fake-token" {
		t.Fatalf("%v %+v %s", e, data, auth)
	}
}
func TestRetryAfter429(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "120"); w.WriteHeader(429) }))
	defer s.Close()
	c, _ := NewClient(s.URL, "emails", nil, s.Client())
	e := c.Publish(context.Background(), domain.Notification{})
	var p *domain.DeliveryError
	if !errors.As(e, &p) || p.Status != "pending" || p.RetryAfter != 120*time.Second {
		t.Fatalf("%v", e)
	}
}
func TestTransientRetries(t *testing.T) {
	for _, code := range []int{500, 503} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		c, _ := NewClient(s.URL, "emails", nil, s.Client())
		e := c.Publish(context.Background(), domain.Notification{})
		var p *domain.DeliveryError
		if !errors.As(e, &p) || p.Status != "pending" {
			t.Fatal(e)
		}
		s.Close()
	}
}
func TestAuthFailureBlocksUntilConfigurationChanges(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer s.Close()
	c, _ := NewClient(s.URL, "emails", nil, s.Client())
	var p *domain.DeliveryError
	if !errors.As(c.Publish(context.Background(), domain.Notification{}), &p) || p.Status != "blocked" {
		t.Fatal("auth failure retry loop")
	}
}
func TestPermanentFailureIsInspectable(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer s.Close()
	c, _ := NewClient(s.URL, "emails", nil, s.Client())
	var p *domain.DeliveryError
	if !errors.As(c.Publish(context.Background(), domain.Notification{}), &p) || p.Status != "failed" {
		t.Fatal("wrong status")
	}
}
