package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaseMigrationPreservesExistingQueue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY);INSERT INTO schema_migrations VALUES(1);
CREATE TABLE outbox(id TEXT PRIMARY KEY,account_id TEXT NOT NULL,event_key TEXT NOT NULL,payload BLOB NOT NULL,status TEXT NOT NULL DEFAULT 'pending',attempt INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,lease_until INTEGER NOT NULL DEFAULT 0,sent_at INTEGER NOT NULL DEFAULT 0,error_code TEXT NOT NULL DEFAULT '',UNIQUE(account_id,event_key));
INSERT INTO outbox(id,account_id,event_key,payload) VALUES('legacy','a','k','{"title":"fake","message":"fake","priority":3}');`)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := Open(ctx, path, 10)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	jobs, e := s.ClaimDeliveries(ctx, time.Now(), 1, time.Minute)
	if e != nil || len(jobs) != 1 || jobs[0].ID != "legacy" || jobs[0].ClaimToken == "" {
		t.Fatalf("migration lost queue: %+v %v", jobs, e)
	}
}

func TestRestartRecoversLease(t *testing.T) {
	s := openTest(t, 100)
	ctx := context.Background()
	s.CommitBatch(ctx, batch("k"), decision("k"))
	now := time.Now()
	jobs, _ := s.ClaimDeliveries(ctx, now, 1, time.Minute)
	if len(jobs) != 1 {
		t.Fatal("claim")
	}
	jobs, _ = s.ClaimDeliveries(ctx, now, 1, time.Minute)
	if len(jobs) != 0 {
		t.Fatal("duplicate lease")
	}
	jobs, _ = s.ClaimDeliveries(ctx, now.Add(61*time.Second), 1, time.Minute)
	if len(jobs) != 1 {
		t.Fatal("expired lease not recovered")
	}
}
func TestSentPayloadPrunedAfterSevenDays(t *testing.T) {
	s := openTest(t, 100)
	ctx := context.Background()
	s.CommitBatch(ctx, batch("old"), decision("old"))
	s.CommitBatch(ctx, batch("pending"), decision("pending"))
	jobs, _ := s.ClaimDeliveries(ctx, time.Now(), 2, time.Minute)
	for _, j := range jobs {
		if j.EventKey == "old" {
			s.MarkSent(ctx, j.ID, j.ClaimToken, time.Now().Add(-8*24*time.Hour))
		}
	}
	s.Prune(ctx, time.Now())
	all, _ := s.ListOutbox(ctx)
	if len(all) != 1 || all[0].Status != "pending" {
		t.Fatalf("%+v", all)
	}
}

func TestRetryCannotBreakActiveLease(t *testing.T) {
	s := openTest(t, 100)
	ctx := context.Background()
	if e := s.CommitBatch(ctx, batch("k"), decision("k")); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	jobs, e := s.ClaimDeliveries(ctx, now, 1, time.Minute)
	if e != nil || len(jobs) != 1 {
		t.Fatal(e)
	}
	if e = s.Retry(ctx, jobs[0].ID); e == nil {
		t.Fatal("active lease retried")
	}
	if again, e := s.ClaimDeliveries(ctx, now, 1, time.Minute); e != nil || len(again) != 0 {
		t.Fatal("leased job claimed twice")
	}
}

func TestStaleSenderCannotOverwriteNewClaim(t *testing.T) {
	s := openTest(t, 100)
	ctx := context.Background()
	if e := s.CommitBatch(ctx, batch("k"), decision("k")); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	old, e := s.ClaimDeliveries(ctx, now, 1, time.Minute)
	if e != nil || len(old) != 1 {
		t.Fatal(e)
	}
	newer, e := s.ClaimDeliveries(ctx, now.Add(61*time.Second), 1, time.Minute)
	if e != nil || len(newer) != 1 {
		t.Fatal(e)
	}
	if old[0].ClaimToken == newer[0].ClaimToken || old[0].ClaimToken == "" {
		t.Fatal("claims not fenced")
	}
	if e = s.MarkSent(ctx, newer[0].ID, newer[0].ClaimToken, now); e != nil {
		t.Fatal(e)
	}
	if e = s.MarkFailure(ctx, old[0].ID, old[0].ClaimToken, "pending", "old_failure", now); e != nil {
		t.Fatal(e)
	}
	items, e := s.ListOutbox(ctx)
	if e != nil || len(items) != 1 || items[0].Status != "sent" {
		t.Fatalf("stale completion unsent job: %+v %v", items, e)
	}
}

func TestReleaseDeliveryDoesNotBreakNewerLease(t *testing.T) {
	s := openTest(t, 100)
	ctx := context.Background()
	if err := s.CommitBatch(ctx, batch("k"), decision("k")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old, err := s.ClaimDeliveries(ctx, now, 1, time.Minute)
	if err != nil || len(old) != 1 {
		t.Fatal(err)
	}
	current, err := s.ClaimDeliveries(ctx, now.Add(61*time.Second), 1, time.Minute)
	if err != nil || len(current) != 1 {
		t.Fatal(err)
	}
	if err = s.ReleaseDelivery(ctx, old[0].ID, old[0].ClaimToken); err != nil {
		t.Fatal(err)
	}
	if jobs, err := s.ClaimDeliveries(ctx, now.Add(61*time.Second), 1, time.Minute); err != nil || len(jobs) != 0 {
		t.Fatal("stale release broke newer lease")
	}
	if err = s.ReleaseDelivery(ctx, current[0].ID, current[0].ClaimToken); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ClaimDeliveries(ctx, now.Add(61*time.Second), 1, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].ClaimToken == current[0].ClaimToken {
		t.Fatal("current release not immediately reclaimable")
	}
}
