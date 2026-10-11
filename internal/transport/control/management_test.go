package control

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/store/sqlite"
)

type readySupervisor struct{}

func (readySupervisor) Wake(string) {}
func (readySupervisor) Ready() bool { return true }
func (readySupervisor) WithAccountStopped(_ context.Context, _ string, f func() error) error {
	return f()
}

func managementServer(t *testing.T) (*Server, *sqlite.Store) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), 2000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewServer("", Services{Status: app.NewStatusService(s, readySupervisor{}), Outbox: app.NewOutboxService(s), Accounts: app.NewAccountService(s, s), Supervisor: readySupervisor{}}), s
}

func queueEvents(t *testing.T, s *sqlite.Store, account string, count int) {
	t.Helper()
	b := domain.Batch{Next: domain.Cursor{AccountID: account, Folder: "INBOX", Initialized: true}}
	ds := make([]domain.Decision, 0, count)
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("%s:%d", account, i)
		b.Events = append(b.Events, domain.MailEvent{Key: key})
		ds = append(ds, domain.Decision{EventKey: key, Notify: true, Payload: domain.Notification{Title: "private-subject", Message: "private-sender", Priority: 3}})
	}
	if err := s.CommitBatch(context.Background(), b, ds); err != nil {
		t.Fatal(err)
	}
}

func TestStatusReportsPerAccountPendingIncludingIdleAccounts(t *testing.T) {
	server, s := managementServer(t)
	ctx := context.Background()
	for _, a := range []domain.Account{{ID: "a", Enabled: true}, {ID: "b", Enabled: false}, {ID: "c", Enabled: true}} {
		if err := s.CreateAccount(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutStatus(ctx, domain.AccountStatus{AccountID: "a", State: "running", LastSuccessAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	queueEvents(t, s, "a", 2)
	queueEvents(t, s, "b", 1)
	value, err := server.dispatch(ctx, Request{Method: "status"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	var result struct {
		Accounts []struct {
			ID      string `json:"account_id"`
			State   string `json:"state"`
			Pending int    `json:"pending_notifications"`
		} `json:"accounts"`
		Pending int `json:"pending_notifications"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Accounts) != 3 || result.Pending != 3 {
		t.Fatalf("missing accounts or counts: %s", raw)
	}
	counts := map[string]int{}
	for _, a := range result.Accounts {
		counts[a.ID] = a.Pending
	}
	if counts["a"] != 2 || counts["b"] != 1 || counts["c"] != 0 {
		t.Fatalf("wrong per-account counts: %s", raw)
	}
}

func TestOutboxPaginationReachesMoreThan1000WithoutPayloads(t *testing.T) {
	server, s := managementServer(t)
	queueEvents(t, s, "a", 1005)
	seen := map[string]bool{}
	after := ""
	for page := 0; page < 20; page++ {
		params, _ := json.Marshal(map[string]any{"limit": 100, "after": after})
		value, err := server.dispatch(context.Background(), Request{Method: "outbox.list", Params: params})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(value)
		var result struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Next string `json:"next_cursor"`
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("expected paginated result: %v", err)
		}
		if len(result.Items) > 100 {
			t.Fatal("page exceeded requested limit")
		}
		for _, item := range result.Items {
			if seen[item.ID] {
				t.Fatal("duplicate page item")
			}
			seen[item.ID] = true
		}
		var data map[string]any
		json.Unmarshal(raw, &data)
		for _, item := range data["items"].([]any) {
			m := item.(map[string]any)
			if _, ok := m["payload"]; ok {
				t.Fatal("mail payload exposed")
			}
		}
		if result.Next == "" {
			break
		}
		if result.Next == after {
			t.Fatal("cursor did not advance")
		}
		after = result.Next
	}
	if len(seen) != 1005 {
		t.Fatalf("only inspected %d of 1005 jobs", len(seen))
	}
}
