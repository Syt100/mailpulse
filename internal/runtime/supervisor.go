package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"io"
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
	return &Supervisor{store: store, providers: providers, processor: p, deliveries: d, workers: map[string]*worker{}, wake: make(chan string, 64), Interval: 60 * time.Second}
}
func (s *Supervisor) Ping(ctx context.Context) error { return s.store.Ping(ctx) }
func (s *Supervisor) Ready() bool                    { return s.running.Load() && s.deliveries.Running() }
func (s *Supervisor) Wake(id string) {
	select {
	case s.wake <- id:
	default:
	}
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
	accounts, e := s.store.ListAccounts(ctx)
	if e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
		cursor, e := s.store.Cursor(ctx, a.ID, "INBOX")
		if e == nil {
			var b domain.Batch
			b, e = p.Scan(ctx, a, cursor)
			if e == nil {
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
						return
					}
					if b.More {
						continue
					}
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
		if s.store.PutStatus(ctx, status) != nil {
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
