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
			s.MarkSent(ctx, j.ID, time.Now().Add(-8*24*time.Hour))
		}
	}
	s.Prune(ctx, time.Now())
	all, _ := s.ListOutbox(ctx)
	if len(all) != 1 || all[0].Status != "pending" {
		t.Fatalf("%+v", all)
	}
}
