package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Account struct {
	ID       string          `json:"id"`
	Alias    string          `json:"alias"`
	Provider string          `json:"provider"`
	Address  string          `json:"address"`
	Enabled  bool            `json:"enabled"`
	Settings json.RawMessage `json:"settings"`
}
type MailEvent struct {
	Key, AccountID, Folder, SenderAddress, SenderName, Subject, ClickURL, Kind string
	ReceivedAt                                                                 time.Time
}
type Cursor struct {
	AccountID, Folder, Generation, Position string
	Initialized                             bool
}
type Batch struct {
	Events   []MailEvent
	Next     Cursor
	Baseline bool
}
type Decision struct {
	EventKey string
	Notify   bool
	Payload  Notification
}
type Notification struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
	Click    string `json:"click,omitempty"`
}
type Delivery struct {
	ID, AccountID, EventKey   string
	Payload                   Notification
	Attempt                   int
	NextAttemptAt, LeaseUntil time.Time
}
type AccountStatus struct {
	AccountID     string    `json:"account_id"`
	State         string    `json:"state"`
	LastSuccessAt time.Time `json:"last_success_at"`
	ErrorCode     string    `json:"error_code,omitempty"`
}
type Rule struct {
	ID              string `json:"id"`
	Order           int    `json:"order"`
	AccountID       string `json:"account_id,omitempty"`
	SenderAddress   string `json:"sender_address,omitempty"`
	SenderDomain    string `json:"sender_domain,omitempty"`
	SubjectContains string `json:"subject_contains,omitempty"`
	Action          string `json:"action"`
	Priority        int    `json:"priority,omitempty"`
	HideSubject     bool   `json:"hide_subject,omitempty"`
	HideSender      bool   `json:"hide_sender,omitempty"`
}
type BatchStore interface {
	CommitBatch(context.Context, Batch, []Decision) error
}
type Provider interface {
	Scan(context.Context, Account, Cursor) (Batch, error)
	Wait(context.Context, Account, time.Duration) error
}
type Publisher interface {
	Publish(context.Context, Notification) error
}
type SecretStore interface {
	Load(context.Context, string) ([]byte, error)
	Save(context.Context, string, []byte) error
}

var ErrCapacity = errors.New("outbox capacity reached")
var ErrNotFound = errors.New("not found")

type ProviderError struct {
	Code       string
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string { return e.Code }

type DeliveryError struct {
	Status, Code string
	RetryAfter   time.Duration
}

func (e *DeliveryError) Error() string { return e.Code }
