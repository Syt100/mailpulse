package app

import (
	"context"
	"errors"
	"github.com/Syt100/mailpulse/internal/domain"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

type DeliveryRepository interface {
	ClaimDeliveries(context.Context, time.Time, int, time.Duration) ([]domain.Delivery, error)
	MarkSent(context.Context, string, time.Time) error
	MarkFailure(context.Context, string, string, string, time.Time) error
	Prune(context.Context, time.Time) error
}
type DeliveryService struct {
	store     DeliveryRepository
	publisher domain.Publisher
	running   atomic.Bool
}

func NewDeliveryService(store DeliveryRepository, publisher domain.Publisher) *DeliveryService {
	return &DeliveryService{store: store, publisher: publisher}
}
func (s *DeliveryService) Running() bool { return s.running.Load() }
func RetryDelay(attempt int) time.Duration {
	if attempt > 6 {
		attempt = 6
	}
	d := time.Duration(float64(5*time.Second) * float64(int64(1)<<attempt) * (0.8 + rand.Float64()*0.4))
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}
func (s *DeliveryService) Run(ctx context.Context, concurrency int) error {
	if concurrency < 1 {
		concurrency = 4
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.running.Store(true)
	defer s.running.Store(false)
	errs := make(chan error, concurrency)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.worker(ctx); e != nil {
				errs <- e
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case e := <-errs:
		cancel()
		<-done
		return e
	case <-ctx.Done():
		<-done
		return nil
	case <-done:
		return nil
	}
}
func (s *DeliveryService) worker(ctx context.Context) error {
	timer := time.NewTicker(500 * time.Millisecond)
	defer timer.Stop()
	cleanup := time.Now()
	for {
		if ctx.Err() != nil {
			return nil
		}
		now := time.Now()
		if now.Sub(cleanup) > time.Hour {
			if e := s.store.Prune(ctx, now); e != nil {
				return e
			}
			cleanup = now
		}
		jobs, e := s.store.ClaimDeliveries(ctx, now, 1, time.Minute)
		if e != nil {
			return e
		}
		for _, j := range jobs {
			e = s.publisher.Publish(ctx, j.Payload)
			if ctx.Err() != nil {
				return nil
			}
			if e == nil {
				if e = s.store.MarkSent(ctx, j.ID, time.Now()); e != nil {
					return e
				}
				continue
			}
			status, code, after := "pending", "publish_error", RetryDelay(j.Attempt)
			var p *domain.DeliveryError
			if errors.As(e, &p) {
				status, code = p.Status, p.Code
				if p.RetryAfter > after {
					after = p.RetryAfter
				}
			}
			if e = s.store.MarkFailure(ctx, j.ID, status, code, time.Now().Add(after)); e != nil {
				return e
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
}
