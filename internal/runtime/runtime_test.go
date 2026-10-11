package runtime

import (
	"bytes"
	"context"
	"errors"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/store/sqlite"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type failedCommit struct{}

type pausedAccountSnapshot struct {
	*sqlite.Store
	started, release chan struct{}
}

func (s *pausedAccountSnapshot) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	accounts, e := s.Store.ListAccounts(ctx)
	close(s.started)
	select {
	case <-s.release:
		return accounts, e
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestAccountMutationCannotRaceReconciliationSnapshot(t *testing.T) {
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
	repo := &pausedAccountSnapshot{Store: s, started: make(chan struct{}), release: make(chan struct{})}
	super := NewSupervisor(repo, map[string]domain.Provider{"fake": provider{true}}, app.NewProcessor(s), app.NewDeliveryService(s, sink{}))
	reconciled := make(chan error, 1)
	go func() { reconciled <- super.reconcile(ctx) }()
	<-repo.started
	mutated := make(chan error, 1)
	go func() {
		mutated <- super.WithAccountStopped(ctx, "a", func() error { return s.RemoveAccount(ctx, "a") })
	}()
	premature := false
	select {
	case e = <-mutated:
		premature = true
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	if err := <-reconciled; err != nil {
		t.Fatal(err)
	}
	if !premature {
		e = <-mutated
	}
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	super.mu.Lock()
	workers := super.workers
	super.mu.Unlock()
	for _, w := range workers {
		w.cancel()
		<-w.done
	}
	if premature {
		t.Fatal("mutation crossed an unguarded account snapshot")
	}
	if len(workers) != 0 {
		t.Fatal("reconciliation resurrected removed worker")
	}
}

func (failedCommit) CommitBatch(context.Context, domain.Batch, []domain.Decision) error {
	return errors.New("sensitive storage failure detail")
}
func TestStorageFailureReportsDegradedWithoutPrivateLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, e := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db"), 10)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	a := domain.Account{ID: "a", Alias: "private-alias", Address: "private@example.test", Provider: "fake", Enabled: true}
	if e = s.CreateAccount(ctx, a, nil); e != nil {
		t.Fatal(e)
	}
	var logs bytes.Buffer
	super := NewSupervisor(s, map[string]domain.Provider{"fake": provider{}}, app.NewProcessor(failedCommit{}), app.NewDeliveryService(s, sink{}))
	super.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	done := make(chan struct{})
	go func() { defer close(done); super.runAccount(ctx, a) }()
	state := ""
	code := ""
	for range 100 {
		statuses, _ := s.Statuses(ctx)
		for _, x := range statuses {
			if x.ErrorCode != "" {
				state, code = x.State, x.ErrorCode
			}
		}
		if code != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if state != "degraded" || code != "storage_unavailable" {
		t.Fatalf("state=%s code=%s", state, code)
	}
	c, e := s.Cursor(context.Background(), "a", "INBOX")
	if e != nil || c.Initialized {
		t.Fatal("failed write advanced cursor")
	}
	output := logs.String()
	for _, sensitive := range []string{"private-alias", "private@example.test", "sensitive storage", "\"subject\"", "\"sender\""} {
		if strings.Contains(output, sensitive) {
			t.Fatal("private data in logs")
		}
	}
	if !strings.Contains(output, `"account_id":"a"`) || !strings.Contains(output, "storage_unavailable") || !strings.Contains(output, "elapsed_ms") {
		t.Fatalf("missing diagnostic fields: %s", output)
	}
}

type transientOnce struct{ calls atomic.Int32 }

func (p *transientOnce) Scan(ctx context.Context, a domain.Account, c domain.Cursor) (domain.Batch, error) {
	if p.calls.Add(1) == 1 {
		return domain.Batch{}, &domain.ProviderError{Code: "transient"}
	}
	return provider{}.Scan(ctx, a, c)
}
func (p *transientOnce) Wait(ctx context.Context, a domain.Account, d time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}
func TestTransientFailureRecoversWithoutWake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, e := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db"), 10)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.CreateAccount(ctx, domain.Account{ID: "a", Provider: "fake", Enabled: true}, nil); e != nil {
		t.Fatal(e)
	}
	p := &transientOnce{}
	super := NewSupervisor(s, map[string]domain.Provider{"fake": p}, app.NewProcessor(s), app.NewDeliveryService(s, sink{}))
	done := make(chan error, 1)
	go func() { done <- super.Run(ctx) }()
	backoff := false
	recovered := false
	for ctx.Err() == nil {
		states, e := s.Statuses(ctx)
		if e != nil {
			break
		}
		for _, state := range states {
			if state.State == "backoff" {
				backoff = true
			}
			if state.State == "running" {
				recovered = true
			}
		}
		if recovered {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !backoff || !recovered || p.calls.Load() != 2 {
		t.Fatalf("backoff=%v recovered=%v calls=%d", backoff, recovered, p.calls.Load())
	}
}

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
	jobs, _ := s.ClaimDeliveries(context.Background(), time.Now(), 1, time.Minute)
	if len(jobs) != 1 {
		t.Fatal("shutdown did not immediately release the in-flight job")
	}
}

type cancelledReconcile struct {
	*sqlite.Store
	started <-chan struct{}
	cancel  context.CancelFunc
}

func (s cancelledReconcile) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	select {
	case <-s.started:
		s.cancel()
		return nil, ctx.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type delayedRelease struct {
	*sqlite.Store
	started chan struct{}
	resume  <-chan struct{}
}

func (s delayedRelease) ReleaseDelivery(ctx context.Context, id, token string) error {
	close(s.started)
	select {
	case <-s.resume:
		return s.Store.ReleaseDelivery(ctx, id, token)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestShutdownDuringReconcileJoinsDeliveryCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db"), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CommitBatch(ctx, domain.Batch{Next: domain.Cursor{AccountID: "a"}, Events: []domain.MailEvent{{Key: "k"}}}, []domain.Decision{{EventKey: "k", Notify: true, Payload: domain.Notification{Priority: 3}}}); err != nil {
		t.Fatal(err)
	}
	p := blockingSink{make(chan struct{}, 1)}
	gate := make(chan struct{})
	release := delayedRelease{s, make(chan struct{}), gate}
	super := NewSupervisor(cancelledReconcile{s, p.started, cancel}, nil, app.NewProcessor(s), app.NewDeliveryService(release, p))
	done := make(chan error, 1)
	go func() { done <- super.Run(ctx) }()
	select {
	case <-release.started:
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("release not started")
	}
	premature := false
	select {
	case <-done:
		premature = true
	case <-time.After(20 * time.Millisecond):
	}
	close(gate)
	if !premature {
		select {
		case err = <-done:
		case <-time.After(time.Second):
			t.Fatal("supervisor did not finish")
		}
	}
	if premature {
		t.Fatal("supervisor returned before delivery cleanup")
	}
	if err != nil {
		t.Fatalf("clean cancellation failed: %v", err)
	}
	jobs, err := s.ClaimDeliveries(context.Background(), time.Now(), 1, time.Minute)
	if err != nil || len(jobs) != 1 {
		t.Fatal("cleanup did not finish before supervisor returned")
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
