package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Syt100/mailpulse/internal/domain"
)

type StatusRepository interface {
	NotificationStatus(context.Context) (domain.RuntimeStatus, error)
}
type Readiness interface{ Ready() bool }
type StatusService struct {
	store     StatusRepository
	readiness Readiness
}

func NewStatusService(store StatusRepository, readiness Readiness) *StatusService {
	return &StatusService{store, readiness}
}
func (s *StatusService) Status(ctx context.Context) (domain.RuntimeStatus, error) {
	status, err := s.store.NotificationStatus(ctx)
	if err != nil {
		return status, err
	}
	if s.readiness != nil {
		status.Ready = s.readiness.Ready()
	}
	return status, nil
}

type OutboxRepository interface {
	ListOutboxPage(context.Context, domain.OutboxQuery) (domain.OutboxPage, error)
	Retry(context.Context, string) error
}
type OutboxService struct{ store OutboxRepository }

func NewOutboxService(store OutboxRepository) *OutboxService { return &OutboxService{store} }
func (s *OutboxService) List(ctx context.Context, q domain.OutboxQuery) (domain.OutboxPage, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 || len(q.After) > 128 || len(q.AccountID) > 128 {
		return domain.OutboxPage{}, fmt.Errorf("invalid outbox query")
	}
	switch q.Status {
	case "", "pending", "blocked", "failed", "sent":
	default:
		return domain.OutboxPage{}, fmt.Errorf("invalid outbox status")
	}
	return s.store.ListOutboxPage(ctx, q)
}
func (s *OutboxService) Retry(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("delivery ID required")
	}
	return s.store.Retry(ctx, id)
}

type NotificationRepository interface {
	Save(context.Context, string, []byte) error
	Unblock(context.Context) error
}
type NotificationService struct {
	store     NotificationRepository
	publisher domain.Publisher
}

func NewNotificationService(store NotificationRepository, publisher domain.Publisher) *NotificationService {
	return &NotificationService{store, publisher}
}
func (s *NotificationService) Configure(ctx context.Context, c domain.NotificationConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err = s.store.Save(ctx, "system:ntfy", raw); err != nil {
		return err
	}
	return s.store.Unblock(ctx)
}
func (s *NotificationService) Test(ctx context.Context) error {
	return s.publisher.Publish(ctx, domain.Notification{Title: "MailPulse 测试通知", Message: "邮箱通知推送连接正常。", Priority: 3})
}
