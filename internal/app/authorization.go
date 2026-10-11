package app

import (
	"context"
	"fmt"
	"github.com/Syt100/mailpulse/internal/domain"
	"sync"
	"time"
)

type DeviceGrant struct {
	Code, VerificationURL string
	ExpiresAt             time.Time
	Complete              func(context.Context) error
}
type AuthorizationBackend interface {
	Begin(context.Context, domain.Account) (DeviceGrant, error)
}
type AuthorizationStart struct {
	SessionID       string    `json:"session_id"`
	Code            string    `json:"code"`
	VerificationURL string    `json:"verification_url"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type AuthorizationStatus struct {
	State     string `json:"state"`
	ErrorCode string `json:"error_code,omitempty"`
}
type authSession struct {
	accountID string
	expires   time.Time
	status    AuthorizationStatus
	cancel    context.CancelFunc
}
type AuthorizationService struct {
	OnComplete func(string)

	backend  AuthorizationBackend
	get      func(context.Context, string) (domain.Account, error)
	mu       sync.Mutex
	sessions map[string]*authSession
	root     context.Context
	cancel   context.CancelFunc
}

func NewAuthorizationService(b AuthorizationBackend, get func(context.Context, string) (domain.Account, error)) *AuthorizationService {
	ctx, cancel := context.WithCancel(context.Background())
	return &AuthorizationService{backend: b, get: get, sessions: map[string]*authSession{}, root: ctx, cancel: cancel}
}
func (s *AuthorizationService) Close() { s.cancel() }
func (s *AuthorizationService) Start(ctx context.Context, id string) (AuthorizationStart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, x := range s.sessions {
		if time.Now().After(x.expires.Add(time.Hour)) {
			x.cancel()
			delete(s.sessions, key)
			continue
		}
		if x.accountID == id && x.status.State == "pending" {
			return AuthorizationStart{}, fmt.Errorf("authorization already pending")
		}
	}
	a, e := s.get(ctx, id)
	if e != nil {
		return AuthorizationStart{}, e
	}
	g, e := s.backend.Begin(ctx, a)
	if e != nil {
		return AuthorizationStart{}, fmt.Errorf("authorization could not start")
	}
	sessionID := ID()
	bg, cancel := context.WithDeadline(s.root, g.ExpiresAt)
	session := &authSession{accountID: id, expires: g.ExpiresAt, status: AuthorizationStatus{State: "pending"}, cancel: cancel}
	s.sessions[sessionID] = session
	go func() {
		defer cancel()
		e := g.Complete(bg)
		s.mu.Lock()
		defer s.mu.Unlock()
		session.status.State = "complete"
		if e == nil && s.OnComplete != nil {
			s.OnComplete(id)
		}
		if e != nil {
			session.status.State = "failed"
			session.status.ErrorCode = "authorization_failed"
		}
	}()
	return AuthorizationStart{sessionID, g.Code, g.VerificationURL, g.ExpiresAt}, nil
}
func (s *AuthorizationService) Status(ctx context.Context, id string) (AuthorizationStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.sessions[id]
	if x == nil {
		return AuthorizationStatus{}, domain.ErrNotFound
	}
	return x.status, nil
}
