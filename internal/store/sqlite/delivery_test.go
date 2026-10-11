package sqlite

import (
	"context"
	"testing"
	"time"
)

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
