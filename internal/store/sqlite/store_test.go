package sqlite

import (
	"context"
	"errors"
	"github.com/Syt100/mailpulse/internal/domain"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T, capacity int) *Store {
	t.Helper()
	s, e := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), capacity)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func batch(key string) domain.Batch {
	return domain.Batch{Events: []domain.MailEvent{{Key: key, AccountID: "a", Kind: "added"}}, Next: domain.Cursor{AccountID: "a", Folder: "INBOX", Position: "1", Initialized: true}}
}
func decision(key string) []domain.Decision {
	return []domain.Decision{{EventKey: key, Notify: true, Payload: domain.Notification{Title: "new", Message: "mail", Priority: 3}}}
}
func TestReplayDoesNotEnqueueTwice(t *testing.T) {
	s := openTest(t, 10000)
	ctx := context.Background()
	for range 2 {
		if e := s.CommitBatch(ctx, batch("k"), decision("k")); e != nil {
			t.Fatal(e)
		}
	}
	jobs, e := s.ClaimDeliveries(ctx, time.Now(), 4, time.Minute)
	if e != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%d err=%v", len(jobs), e)
	}
}
func TestBaselineSavesKeysWithoutNotification(t *testing.T) {
	s := openTest(t, 10000)
	b := batch("old")
	b.Baseline = true
	ctx := context.Background()
	if e := s.CommitBatch(ctx, b, decision("old")); e != nil {
		t.Fatal(e)
	}
	b.Baseline = false
	if e := s.CommitBatch(ctx, b, decision("old")); e != nil {
		t.Fatal(e)
	}
	jobs, _ := s.ClaimDeliveries(ctx, time.Now(), 4, time.Minute)
	if len(jobs) != 0 {
		t.Fatal("baseline replay notified")
	}
}
func TestCapacityDoesNotAdvanceCursor(t *testing.T) {
	s := openTest(t, 1)
	ctx := context.Background()
	s.CommitBatch(ctx, batch("first"), decision("first"))
	b := batch("second")
	b.Next.Position = "2"
	if e := s.CommitBatch(ctx, b, decision("second")); !errors.Is(e, domain.ErrCapacity) {
		t.Fatalf("want capacity, got %v", e)
	}
	c, e := s.Cursor(ctx, "a", "INBOX")
	if e != nil || c.Position != "1" {
		t.Fatalf("cursor=%+v err=%v", c, e)
	}
}
func TestCommitBatchAtomic(t *testing.T) {
	s := openTest(t, 10000)
	ctx := context.Background()
	s.db.Exec(`CREATE TRIGGER reject_cursor BEFORE INSERT ON sync_state BEGIN SELECT RAISE(FAIL, 'injected'); END`)
	if e := s.CommitBatch(ctx, batch("k"), decision("k")); e == nil {
		t.Fatal("expected injected failure")
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM processed_events`).Scan(&n)
	if n != 0 {
		t.Fatal("event survived rollback")
	}
	s.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&n)
	if n != 0 {
		t.Fatal("job survived rollback")
	}
}
