package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/store/sqlite"
)

type sink struct{ received []domain.Notification }

func (s *sink) Publish(_ context.Context, n domain.Notification) error {
	s.received = append(s.received, n)
	return nil
}

type cancelOnCompletionStore struct {
	*sqlite.Store
	cancel context.CancelFunc
}

func (s cancelOnCompletionStore) MarkSent(ctx context.Context, id, token string, now time.Time) error {
	s.cancel()
	return s.Store.MarkSent(ctx, id, token, now)
}
func (s cancelOnCompletionStore) MarkFailure(ctx context.Context, id, token, status, code string, next time.Time) error {
	s.cancel()
	return s.Store.MarkFailure(ctx, id, token, status, code, next)
}

type resultSink struct{ err error }

func (p resultSink) Publish(context.Context, domain.Notification) error { return p.err }

func TestCancellationDuringDeliveryCompletionReleasesClaim(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"mark_sent", nil}, {"mark_failure", &domain.DeliveryError{Status: "pending", Code: "network_error"}}} {
		t.Run(tc.name, func(t *testing.T) {
			s := managementStore(t)
			addJob(t, s, "a", "k")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			service := app.NewDeliveryService(cancelOnCompletionStore{s, cancel}, resultSink{tc.err})
			if err := service.Run(ctx, 1); err != nil {
				t.Fatalf("clean cancellation failed: %v", err)
			}
			jobs, err := s.ClaimDeliveries(context.Background(), time.Now(), 1, time.Minute)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("cancelled completion retained lease: %v %v", jobs, err)
			}
		})
	}
}

type failedReleaseStore struct {
	*sqlite.Store
	err error
}

func (s failedReleaseStore) ReleaseDelivery(context.Context, string, string) error { return s.err }

type cancellingSink struct{ cancel context.CancelFunc }

func (s cancellingSink) Publish(context.Context, domain.Notification) error { s.cancel(); return nil }

func TestShutdownReportsReleaseFailure(t *testing.T) {
	for _, failure := range []error{errors.New("release failed"), context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			s := managementStore(t)
			addJob(t, s, "a", "k")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := app.NewDeliveryService(failedReleaseStore{s, failure}, cancellingSink{cancel}).Run(ctx, 1)
			if !errors.Is(err, failure) {
				t.Fatalf("release failure was hidden: %v", err)
			}
			jobs, err := s.ClaimDeliveries(context.Background(), time.Now().Add(61*time.Second), 1, time.Minute)
			if err != nil || len(jobs) != 1 {
				t.Fatal("failed cleanup lost durable job")
			}
		})
	}
}

type ready bool

func (r ready) Ready() bool { return bool(r) }

func managementStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.BindKey(context.Background(), make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	return s
}

func addJob(t *testing.T, s *sqlite.Store, account, key string) {
	t.Helper()
	err := s.CommitBatch(context.Background(), domain.Batch{Next: domain.Cursor{AccountID: account}, Events: []domain.MailEvent{{Key: key}}}, []domain.Decision{{EventKey: key, Notify: true, Payload: domain.Notification{Title: "private-subject", Message: "private-sender", Priority: 3}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNotificationServiceConfiguresEncryptedSecretsAndUnblocks(t *testing.T) {
	s := managementStore(t)
	ctx := context.Background()
	addJob(t, s, "a", "k")
	jobs, err := s.ClaimDeliveries(ctx, time.Now(), 1, time.Minute)
	if err != nil || len(jobs) != 1 {
		t.Fatal(err)
	}
	if err = s.MarkFailure(ctx, jobs[0].ID, jobs[0].ClaimToken, "blocked", "ntfy_http_401", time.Now()); err != nil {
		t.Fatal(err)
	}
	p := &sink{}
	service := app.NewNotificationService(s, p)
	if err = service.Configure(ctx, domain.NotificationConfig{URL: "http://127.0.0.1:8080", Topic: "mail", Token: "fake-secret"}); err != nil {
		t.Fatal(err)
	}
	secret, err := s.Load(ctx, "system:ntfy")
	if err != nil || len(secret) == 0 {
		t.Fatal("configuration not saved")
	}
	jobs, err = s.ClaimDeliveries(ctx, time.Now(), 1, time.Minute)
	if err != nil || len(jobs) != 1 {
		t.Fatal("configuration did not unblock queue")
	}
	if err = service.Test(ctx); err != nil || len(p.received) != 1 || p.received[0].Priority != 3 {
		t.Fatal("test notification missing")
	}
	items, err := s.ListOutbox(ctx)
	if err != nil || len(items) != 1 {
		t.Fatal("test notification created a queue job")
	}
	if err = service.Configure(ctx, domain.NotificationConfig{URL: "https://example.test/?token=x", Topic: "mail"}); err == nil {
		t.Fatal("invalid configuration accepted")
	}
}

func TestOutboxServiceFiltersPagesAndRetries(t *testing.T) {
	s := managementStore(t)
	ctx := context.Background()
	for _, pair := range [][2]string{{"a", "one"}, {"b", "two"}, {"a", "three"}} {
		addJob(t, s, pair[0], pair[1])
	}
	jobs, err := s.ClaimDeliveries(ctx, time.Now(), 3, time.Minute)
	if err != nil || len(jobs) != 3 {
		t.Fatal(err)
	}
	var failedID string
	for _, j := range jobs {
		if j.AccountID == "b" {
			err = s.MarkSent(ctx, j.ID, j.ClaimToken, time.Now())
		} else {
			failedID = j.ID
			err = s.MarkFailure(ctx, j.ID, j.ClaimToken, "failed", "bad_request", time.Now())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	service := app.NewOutboxService(s)
	page, err := service.List(ctx, domain.OutboxQuery{AccountID: "a", Status: "failed", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("missing first filtered page: %+v %v", page, err)
	}
	next, err := service.List(ctx, domain.OutboxQuery{AccountID: "a", Status: "failed", Limit: 1, After: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" || next.Items[0].ID == page.Items[0].ID {
		t.Fatalf("wrong second page: %+v %v", next, err)
	}
	if err = service.Retry(ctx, failedID); err != nil {
		t.Fatal(err)
	}
	page, err = service.List(ctx, domain.OutboxQuery{Status: "pending"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != failedID {
		t.Fatal("retry not visible")
	}
	for _, q := range []domain.OutboxQuery{{Limit: -1}, {Limit: 1001}, {Status: "invalid"}, {After: string(make([]byte, 129))}} {
		if _, err = service.List(ctx, q); err == nil {
			t.Fatalf("invalid query accepted: %+v", q)
		}
	}
}

func TestStatusServiceIncludesUnsentStatusesAndReadiness(t *testing.T) {
	s := managementStore(t)
	ctx := context.Background()
	if err := s.CreateAccount(ctx, domain.Account{ID: "a", Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"pending", "blocked", "failed", "sent"} {
		addJob(t, s, "a", k)
	}
	jobs, err := s.ClaimDeliveries(ctx, time.Now(), 4, time.Minute)
	if err != nil || len(jobs) != 4 {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.EventKey == "sent" {
			err = s.MarkSent(ctx, j.ID, j.ClaimToken, time.Now())
		} else {
			err = s.MarkFailure(ctx, j.ID, j.ClaimToken, j.EventKey, "error", time.Now())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	status, err := app.NewStatusService(s, ready(false)).Status(ctx)
	if err != nil || status.Ready || status.PendingNotifications != 3 || len(status.Accounts) != 1 || status.Accounts[0].PendingNotifications != 3 {
		t.Fatalf("wrong status: %+v %v", status, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = app.NewStatusService(s, ready(true)).Status(ctx); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("storage failure hidden")
	}
}
