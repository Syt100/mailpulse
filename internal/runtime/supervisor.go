package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type AccountRepository interface {
	ListAccounts(context.Context) ([]domain.Account, error)
	GetAccount(context.Context, string) (domain.Account, error)
	Cursor(context.Context, string, string) (domain.Cursor, error)
	ListRules(context.Context) ([]domain.Rule, error)
	PutStatus(context.Context, domain.AccountStatus) error
	Ping(context.Context) error
}
type worker struct {
	cancel    context.CancelFunc
	done      chan struct{}
	signature string
}
type Supervisor struct {
	Logger     *slog.Logger
	store      AccountRepository
	providers  map[string]domain.Provider
	processor  *app.Processor
	deliveries *app.DeliveryService
	mu         sync.Mutex
	workers    map[string]*worker
	wake       chan string
	running    atomic.Bool
	Interval   time.Duration
}

func NewSupervisor(store AccountRepository, providers map[string]domain.Provider, p *app.Processor, d *app.DeliveryService) *Supervisor {
	return &Supervisor{Logger: slog.Default(), store: store, providers: providers, processor: p, deliveries: d, workers: map[string]*worker{}, wake: make(chan string, 64), Interval: 60 * time.Second}
}
func (s *Supervisor) Ping(ctx context.Context) error { return s.store.Ping(ctx) }
func (s *Supervisor) Ready() bool                    { return s.running.Load() && s.deliveries.Running() }
func (s *Supervisor) Wake(id string) {
	s.mu.Lock()
	if w := s.workers[id]; w != nil {
		w.cancel()
	}
	s.mu.Unlock()
	select {
	case s.wake <- id:
	default:
	}
}

// WithAccountStopped joins the old worker and excludes reconciliation until
// the mutation finishes, so no old cursor or event can cross a reset/removal.
func (s *Supervisor) WithAccountStopped(ctx context.Context, id string, mutate func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.workers[id]; w != nil {
		w.cancel()
		select {
		case <-w.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		delete(s.workers, id)
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	return mutate()
}
func (s *Supervisor) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.running.Store(true)
	defer s.running.Store(false)
	sendErr := make(chan error, 1)
	go func() { sendErr <- s.deliveries.Run(ctx, 4) }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer func() {
		cancel()
		s.mu.Lock()
		workers := make([]*worker, 0, len(s.workers))
		for _, w := range s.workers {
			w.cancel()
			workers = append(workers, w)
		}
		s.mu.Unlock()
		for _, w := range workers {
			<-w.done
		}
		for _, p := range s.providers {
			if c, ok := p.(io.Closer); ok {
				c.Close()
			}
		}
	}()
	for {
		if e := s.reconcile(ctx); e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			<-sendErr
			return nil
		case e := <-sendErr:
			return e
		case id := <-s.wake:
			s.mu.Lock()
			if w := s.workers[id]; w != nil {
				w.cancel()
			}
			s.mu.Unlock()
		case <-tick.C:
		}
	}
}
func (s *Supervisor) reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts, e := s.store.ListAccounts(ctx)
	if e != nil {
		return e
	}
	present := map[string]bool{}
	for _, a := range accounts {
		present[a.ID] = a.Enabled
		raw, _ := json.Marshal(a)
		signature := string(raw)
		w := s.workers[a.ID]
		if w != nil {
			select {
			case <-w.done:
				delete(s.workers, a.ID)
				w = nil
			default:
			}
		}
		if !a.Enabled {
			if w != nil {
				w.cancel()
			}
			s.store.PutStatus(ctx, domain.AccountStatus{AccountID: a.ID, State: "paused"})
			continue
		}
		if w != nil {
			if signature != w.signature {
				w.cancel()
			}
			continue
		}
		child, cancel := context.WithCancel(ctx)
		w = &worker{cancel: cancel, done: make(chan struct{}), signature: signature}
		s.workers[a.ID] = w
		go func(a domain.Account, w *worker) { defer close(w.done); s.runAccount(child, a) }(a, w)
	}
	for id, w := range s.workers {
		if !present[id] {
			w.cancel()
		}
	}
	return nil
}
func (s *Supervisor) runAccount(ctx context.Context, a domain.Account) {
	status := domain.AccountStatus{AccountID: a.ID, State: "initializing"}
	s.store.PutStatus(ctx, status)
	p := s.providers[a.Provider]
	if p == nil {
		status.State = "auth_required"
		status.ErrorCode = "provider_unavailable"
		s.store.PutStatus(ctx, status)
		<-ctx.Done()
		return
	}
	defer func() {
		if c, ok := p.(interface{ CloseAccount(string) }); ok {
			c.CloseAccount(a.ID)
		}
	}()
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		storageFailure := true
		cursor, e := s.store.Cursor(ctx, a.ID, "INBOX")
		if e == nil {
			var b domain.Batch
			storageFailure = false
			b, e = p.Scan(ctx, a, cursor)
			if e == nil {
				storageFailure = true
				rules, err := s.store.ListRules(ctx)
				e = err
				if e == nil {
					b.Alias = a.Alias
					e = s.processor.Process(ctx, b, rules)
				}
				if e == nil {
					attempt = 0
					status.ErrorCode = ""
					status.State = "running"
					if !b.Next.Initialized {
						status.State = "initializing"
					}
					status.LastSuccessAt = time.Now().UTC()
					if s.store.PutStatus(ctx, status) != nil {
						s.Logger.Warn("account status unavailable", "account_id", a.ID, "error_code", "storage_unavailable")
						return
					}
					s.Logger.Info("mail scan completed", "account_id", a.ID, "elapsed_ms", time.Since(started).Milliseconds(), "events", len(b.Events), "baseline", b.Baseline, "more", b.More)
					if b.More {
						continue
					}
					storageFailure = false
					started = time.Now()
					e = p.Wait(ctx, a, s.Interval)
					if e == nil {
						continue
					}
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		status.State = "backoff"
		status.ErrorCode = "provider_transient"
		if storageFailure {
			status.State = "degraded"
			status.ErrorCode = "storage_unavailable"
		}
		delay := app.RetryDelay(attempt)
		attempt++
		var pe *domain.ProviderError
		if errors.As(e, &pe) {
			status.ErrorCode = pe.Code
			if pe.RetryAfter > delay {
				delay = pe.RetryAfter
			}
			switch pe.Code {
			case "authentication_required":
				status.State = "auth_required"
			case "needs_resync":
				status.State = "needs_resync"
			}
		}
		if errors.Is(e, domain.ErrCapacity) {
			status.State = "degraded"
			status.ErrorCode = "outbox_capacity"
		}
		s.Logger.Warn("account scan interrupted", "account_id", a.ID, "state", status.State, "error_code", status.ErrorCode, "elapsed_ms", time.Since(started).Milliseconds())
		if s.store.PutStatus(ctx, status) != nil {
			s.Logger.Warn("account status unavailable", "account_id", a.ID, "error_code", "storage_unavailable")
			return
		}
		if status.State == "auth_required" || status.State == "needs_resync" {
			<-ctx.Done()
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
