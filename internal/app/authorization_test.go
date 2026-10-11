package app

import (
	"context"
	"github.com/Syt100/mailpulse/internal/domain"
	"testing"
	"time"
)

type fakeAuth struct{ calls int }

func (f *fakeAuth) Begin(ctx context.Context, a domain.Account) (DeviceGrant, error) {
	f.calls++
	return DeviceGrant{Code: "fake", VerificationURL: "https://example.com", ExpiresAt: time.Now().Add(time.Hour), Complete: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}, nil
}
func TestDeviceFlowIsExplicitAndCancellable(t *testing.T) {
	f := &fakeAuth{}
	s := NewAuthorizationService(f, func(context.Context, string) (domain.Account, error) { return domain.Account{ID: "a"}, nil })
	if f.calls != 0 {
		t.Fatal("implicit authorization")
	}
	start, e := s.Start(context.Background(), "a")
	if e != nil || start.Code != "fake" {
		t.Fatal(e)
	}
	s.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, _ := s.Status(context.Background(), start.SessionID)
		if status.State != "pending" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("session did not cancel")
}
