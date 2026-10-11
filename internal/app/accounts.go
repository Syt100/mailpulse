package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Syt100/mailpulse/internal/domain"
	"net/mail"
	"strings"
)

type AccountRepository interface {
	CreateAccount(context.Context, domain.Account, []byte) error
	GetAccount(context.Context, string) (domain.Account, error)
	ListAccounts(context.Context) ([]domain.Account, error)
	SetEnabled(context.Context, string, bool) error
	RemoveAccount(context.Context, string) error
	ResetState(context.Context, string) error
}
type AccountService struct {
	store   AccountRepository
	secrets domain.SecretStore
}

func NewAccountService(s AccountRepository, secrets domain.SecretStore) *AccountService {
	return &AccountService{s, secrets}
}
func ID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func (s *AccountService) Add(ctx context.Context, a domain.Account, secret []byte) (domain.Account, error) {
	m, e := mail.ParseAddress(a.Address)
	if e != nil || m.Address != a.Address {
		return a, fmt.Errorf("valid email address required")
	}
	switch a.Provider {
	case "gmail", "qq":
		if len(secret) == 0 {
			return a, fmt.Errorf("mailbox secret required")
		}
	case "outlook":
		var v struct {
			ClientID string `json:"client_id"`
		}
		if json.Unmarshal(a.Settings, &v) != nil || v.ClientID == "" {
			return a, fmt.Errorf("Outlook client_id required")
		}
	default:
		return a, fmt.Errorf("unsupported provider")
	}
	a.ID = ID()
	a.Enabled = true
	if a.Alias == "" {
		a.Alias = a.Provider
	}
	if len(a.Settings) == 0 {
		a.Settings = json.RawMessage(`{}`)
	}
	if !json.Valid(a.Settings) {
		return a, fmt.Errorf("invalid settings")
	}
	e = s.store.CreateAccount(ctx, a, secret)
	return a, e
}
func (s *AccountService) List(ctx context.Context) ([]domain.Account, error) {
	return s.store.ListAccounts(ctx)
}
func (s *AccountService) Pause(ctx context.Context, id string) error {
	return s.store.SetEnabled(ctx, id, false)
}
func (s *AccountService) Resume(ctx context.Context, id string) error {
	return s.store.SetEnabled(ctx, id, true)
}
func (s *AccountService) Remove(ctx context.Context, id string, confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("confirmation required: account and queued notifications will be removed")
	}
	return s.store.RemoveAccount(ctx, id)
}
func (s *AccountService) Resync(ctx context.Context, id string, ignoreHistory bool) error {
	if !ignoreHistory {
		return fmt.Errorf("--ignore-history required: indeterminate missed events will be skipped")
	}
	return s.store.ResetState(ctx, id)
}

type RuleRepository interface {
	AddRule(context.Context, domain.Rule) error
	ListRules(context.Context) ([]domain.Rule, error)
	RemoveRule(context.Context, string) error
}
type RuleService struct{ store RuleRepository }

func NewRuleService(s RuleRepository) *RuleService { return &RuleService{s} }
func (s *RuleService) Add(ctx context.Context, r domain.Rule) (domain.Rule, error) {
	if r.Action != "ignore" && r.Action != "notify" {
		return r, fmt.Errorf("action must be ignore or notify")
	}
	if r.Priority < 0 || r.Priority > 5 {
		return r, fmt.Errorf("priority must be 1 to 5")
	}
	r.SenderDomain = strings.ToLower(r.SenderDomain)
	r.ID = ID()
	return r, s.store.AddRule(ctx, r)
}
func (s *RuleService) List(ctx context.Context) ([]domain.Rule, error) { return s.store.ListRules(ctx) }
func (s *RuleService) Remove(ctx context.Context, id string) error {
	return s.store.RemoveRule(ctx, id)
}
