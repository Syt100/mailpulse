package runtime

import (
	"context"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/store/sqlite"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountMutationWaitsForScanAndPreventsRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, e := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db"), 10)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.CreateAccount(ctx, domain.Account{ID: "a", Provider: "fake", Enabled: true}, nil); e != nil {
		t.Fatal(e)
	}
	p := &stalledScan{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	super := NewSupervisor(s, map[string]domain.Provider{"fake": p}, app.NewProcessor(s), app.NewDeliveryService(s, sink{}))
	done := make(chan error, 1)
	go func() { done <- super.Run(ctx) }()
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("scan not started")
	}
	mutated := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- super.WithAccountStopped(ctx, "a", func() error { close(mutated); return s.RemoveAccount(ctx, "a") })
	}()
	select {
	case <-p.cancelled:
	case <-time.After(time.Second):
		t.Fatal("scan not cancelled")
	}
	select {
	case <-mutated:
		t.Fatal("mutation ran before scan joined")
	default:
	}
	close(p.release)
	if e = <-mutationDone; e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetAccount(ctx, "a"); e != domain.ErrNotFound {
		t.Fatal("account survived")
	}
	c, e := s.Cursor(ctx, "a", "INBOX")
	if e != nil || c.Initialized {
		t.Fatal("old scan recreated state")
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}

type stalledScan struct{ started, cancelled, release chan struct{} }

func (p *stalledScan) Scan(ctx context.Context, a domain.Account, c domain.Cursor) (domain.Batch, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	<-p.release
	return domain.Batch{Next: domain.Cursor{AccountID: a.ID, Folder: "INBOX", Position: "old", Initialized: true}}, nil
}
func (p *stalledScan) Wait(ctx context.Context, a domain.Account, d time.Duration) error {
	return ctx.Err()
}

func TestSecondDaemonRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	l, e := AcquireLock(p)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if _, e = AcquireLock(p); e == nil {
		t.Fatal("two writers")
	}
}

type provider struct{ fail bool }

func (p provider) Scan(ctx context.Context, a domain.Account, c domain.Cursor) (domain.Batch, error) {
	if p.fail {
		return domain.Batch{}, &domain.ProviderError{Code: "authentication_required"}
	}
	return domain.Batch{Next: domain.Cursor{AccountID: a.ID, Folder: "INBOX", Position: "1", Initialized: true}, Events: []domain.MailEvent{{Key: a.ID + ":1", AccountID: a.ID, Kind: "added", Subject: "test"}}}, nil
}
func (p provider) Wait(ctx context.Context, a domain.Account, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Millisecond):
		return nil
	}
}

type sink struct{}

func (sink) Publish(context.Context, domain.Notification) error { return nil }
func TestOneAccountFailureIsIsolated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, e := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db"), 100)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.CreateAccount(ctx, domain.Account{ID: "bad", Alias: "bad", Provider: "bad", Enabled: true}, nil)
	s.CreateAccount(ctx, domain.Account{ID: "good", Alias: "good", Provider: "good", Enabled: true}, nil)
	super := NewSupervisor(s, map[string]domain.Provider{"bad": provider{true}, "good": provider{}}, app.NewProcessor(s), app.NewDeliveryService(s, sink{}))
	done := make(chan error, 1)
	go func() { done <- super.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		statuses, _ := s.Statuses(ctx)
		for _, x := range statuses {
			if x.AccountID == "good" && x.State == "running" {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if !found {
		t.Fatal("good mailbox blocked")
	}
}
func TestDiskFailureDoesNotAdvanceCursor(t *testing.T) {
	s, e := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "db"), 1)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	b := domain.Batch{Next: domain.Cursor{AccountID: "a", Position: "2"}}
	if e = s.CommitBatch(context.Background(), b, nil); e == nil {
		t.Fatal("closed DB accepted transaction")
	}
}

type blockingSink struct{ started chan struct{} }

func (p blockingSink) Publish(ctx context.Context, n domain.Notification) error {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}
func TestShutdownRecoversInFlightJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s, _ := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db"), 10)
	defer s.Close()
	s.CommitBatch(ctx, domain.Batch{Next: domain.Cursor{AccountID: "a"}, Events: []domain.MailEvent{{Key: "k"}}}, []domain.Decision{{EventKey: "k", Notify: true, Payload: domain.Notification{Priority: 3}}})
	p := blockingSink{make(chan struct{}, 1)}
	ds := app.NewDeliveryService(s, p)
	done := make(chan error, 1)
	go func() { done <- ds.Run(ctx, 1) }()
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("no delivery")
	}
	cancel()
	<-done
	jobs, _ := s.ClaimDeliveries(context.Background(), time.Now().Add(2*time.Minute), 1, time.Minute)
	if len(jobs) != 1 {
		t.Fatal("in-flight job lost")
	}
}

type slowAuth struct{}

func (slowAuth) Begin(ctx context.Context, a domain.Account) (app.DeviceGrant, error) {
	return app.DeviceGrant{ExpiresAt: time.Now().Add(time.Hour), Complete: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}, nil
}
func TestAuthorizationDoesNotBlockScans(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, _ := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db"), 10)
	defer s.Close()
	s.CreateAccount(ctx, domain.Account{ID: "good", Provider: "good", Enabled: true}, nil)
	auth := app.NewAuthorizationService(slowAuth{}, s.GetAccount)
	defer auth.Close()
	auth.Start(ctx, "good")
	super := NewSupervisor(s, map[string]domain.Provider{"good": provider{}}, app.NewProcessor(s), app.NewDeliveryService(s, sink{}))
	done := make(chan error, 1)
	go func() { done <- super.Run(ctx) }()
	for range 100 {
		c, _ := s.Cursor(ctx, "good", "INBOX")
		if c.Initialized {
			cancel()
			<-done
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("authorization blocked scan")
}
