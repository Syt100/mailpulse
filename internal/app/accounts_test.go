package app_test

import (
	"context"
	"encoding/json"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/store/sqlite"
	"path/filepath"
	"strings"
	"testing"
)

func accounts(t *testing.T) (*sqlite.Store, *app.AccountService) {
	t.Helper()
	ctx := context.Background()
	s, e := sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.db"), 10000)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.BindKey(ctx, make([]byte, 32)); e != nil {
		t.Fatal(e)
	}
	return s, app.NewAccountService(s, s)
}
func TestSecretsAbsentFromAccountJSON(t *testing.T) {
	s, svc := accounts(t)
	a, e := svc.Add(context.Background(), domain.Account{Alias: "Work", Provider: "gmail", Address: "demo@example.com"}, []byte("super-secret"))
	if e != nil {
		t.Fatal(e)
	}
	list, _ := svc.List(context.Background())
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "super-secret") {
		t.Fatal("leak")
	}
	secret, e := s.Load(context.Background(), a.ID)
	if e != nil || string(secret) != "super-secret" {
		t.Fatal(e)
	}
}
func TestPausePreservesState(t *testing.T) {
	s, svc := accounts(t)
	ctx := context.Background()
	a, _ := svc.Add(ctx, domain.Account{Provider: "gmail", Address: "demo@example.com"}, []byte("secret"))
	b := domain.Batch{Next: domain.Cursor{AccountID: a.ID, Folder: "INBOX", Position: "42", Initialized: true}}
	s.CommitBatch(ctx, b, nil)
	if e := svc.Pause(ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	c, _ := s.Cursor(ctx, a.ID, "INBOX")
	if c.Position != "42" {
		t.Fatal("pause removed cursor")
	}
}
func TestRemoveRequiresExplicitConfirmation(t *testing.T) {
	_, svc := accounts(t)
	a, _ := svc.Add(context.Background(), domain.Account{Provider: "qq", Address: "demo@example.com"}, []byte("secret"))
	if e := svc.Remove(context.Background(), a.ID, false); e == nil {
		t.Fatal("no confirmation")
	}
}
func TestResyncRequiresIgnoreHistory(t *testing.T) {
	_, svc := accounts(t)
	if e := svc.Resync(context.Background(), "a", false); e == nil {
		t.Fatal("unsafe resync")
	}
}
func TestResyncPreservesOutbox(t *testing.T) {
	s, svc := accounts(t)
	ctx := context.Background()
	a, _ := svc.Add(ctx, domain.Account{Provider: "qq", Address: "demo@example.com"}, []byte("secret"))
	s.CommitBatch(ctx, domain.Batch{Next: domain.Cursor{AccountID: a.ID, Folder: "INBOX"}, Events: []domain.MailEvent{{Key: "k"}}}, []domain.Decision{{EventKey: "k", Notify: true, Payload: domain.Notification{Priority: 3}}})
	if e := svc.Resync(ctx, a.ID, true); e != nil {
		t.Fatal(e)
	}
	jobs, e := s.ListOutbox(ctx)
	if e != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%d %v", len(jobs), e)
	}
}
