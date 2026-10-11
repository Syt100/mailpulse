package imap

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/Syt100/mailpulse/internal/domain"
	lib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	"mime"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Dialer func(context.Context, string) (net.Conn, error)
type session struct {
	c       *imapclient.Client
	conn    net.Conn
	changed chan struct{}
}
type Provider struct {
	secrets  domain.SecretStore
	dial     Dialer
	mu       sync.Mutex
	sessions map[string]*session
}

func NewProvider(secrets domain.SecretStore, dialer Dialer) *Provider {
	if dialer == nil {
		dialer = func(ctx context.Context, addr string) (net.Conn, error) {
			host, _, e := net.SplitHostPort(addr)
			if e != nil {
				return nil, e
			}
			d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 30 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}}
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	return &Provider{secrets: secrets, dial: dialer, sessions: map[string]*session{}}
}
func (p *Provider) CloseAccount(id string) {
	p.mu.Lock()
	s := p.sessions[id]
	delete(p.sessions, id)
	p.mu.Unlock()
	if s != nil {
		s.c.Close()
	}
}
func (p *Provider) Close() error {
	p.mu.Lock()
	ids := make([]string, 0, len(p.sessions))
	for id := range p.sessions {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.CloseAccount(id)
	}
	return nil
}
func (p *Provider) connect(ctx context.Context, a domain.Account) (*session, error) {
	p.mu.Lock()
	s := p.sessions[a.ID]
	p.mu.Unlock()
	if s != nil {
		select {
		case <-s.c.Closed():
			p.CloseAccount(a.ID)
		default:
			return s, nil
		}
	}
	host := "imap.gmail.com:993"
	if a.Provider == "qq" {
		host = "imap.qq.com:993"
	}
	secret, e := p.secrets.Load(ctx, a.ID)
	if e != nil {
		return nil, &domain.ProviderError{Code: "authentication_required"}
	}
	conn, e := p.dial(ctx, host)
	if e != nil {
		return nil, &domain.ProviderError{Code: "transient"}
	}
	s = &session{conn: conn, changed: make(chan struct{}, 1)}
	s.c = imapclient.New(conn, &imapclient.Options{WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}, UnilateralDataHandler: &imapclient.UnilateralDataHandler{Mailbox: func(*imapclient.UnilateralDataMailbox) {
		select {
		case s.changed <- struct{}{}:
		default:
		}
	}}})
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	stop := context.AfterFunc(ctx, func() { s.c.Close() })
	defer stop()
	if e = s.c.WaitGreeting(); e != nil {
		s.c.Close()
		return nil, &domain.ProviderError{Code: "transient"}
	}
	if e = s.c.Login(a.Address, string(secret)).Wait(); e != nil {
		s.c.Close()
		return nil, &domain.ProviderError{Code: "authentication_required"}
	}
	conn.SetDeadline(time.Time{})
	p.mu.Lock()
	p.sessions[a.ID] = s
	p.mu.Unlock()
	return s, nil
}
func (p *Provider) Scan(ctx context.Context, a domain.Account, cursor domain.Cursor) (domain.Batch, error) {
	s, e := p.connect(ctx, a)
	if e != nil {
		return domain.Batch{}, e
	}
	stop := context.AfterFunc(ctx, func() { s.c.Close() })
	defer stop()
	s.conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer s.conn.SetDeadline(time.Time{})
	selected, e := s.c.Select("INBOX", &lib.SelectOptions{ReadOnly: true}).Wait()
	if e != nil {
		p.CloseAccount(a.ID)
		return domain.Batch{}, &domain.ProviderError{Code: "transient"}
	}
	generation := strconv.FormatUint(uint64(selected.UIDValidity), 10)
	next := domain.Cursor{AccountID: a.ID, Folder: "INBOX", Generation: generation, Initialized: true}
	if selected.UIDValidity == 0 || selected.UIDNext == 0 {
		p.CloseAccount(a.ID)
		return domain.Batch{}, &domain.ProviderError{Code: "transient"}
	}
	top := uint64(selected.UIDNext) - 1
	if !cursor.Initialized {
		next.Position = strconv.FormatUint(top, 10)
		return domain.Batch{Next: next, Baseline: true}, nil
	}
	if cursor.Generation != generation {
		return domain.Batch{}, &domain.ProviderError{Code: "needs_resync"}
	}
	last, e := strconv.ParseUint(cursor.Position, 10, 32)
	if e != nil {
		return domain.Batch{}, &domain.ProviderError{Code: "needs_resync"}
	}
	next.Position = cursor.Position
	if last >= top {
		return domain.Batch{Next: next}, nil
	}
	end := last + 200
	if end > top {
		end = top
	}
	set := lib.UIDSet{}
	set.AddRange(lib.UID(last+1), lib.UID(end))
	msgs, e := s.c.Fetch(set, &lib.FetchOptions{UID: true, Envelope: true, InternalDate: true}).Collect()
	if e != nil {
		p.CloseAccount(a.ID)
		return domain.Batch{}, &domain.ProviderError{Code: "transient"}
	}
	next.Position = strconv.FormatUint(end, 10)
	return domain.Batch{Next: next, Events: events(a, generation, msgs, uint32(last), uint32(end)), More: end < top}, nil
}
func events(a domain.Account, generation string, msgs []*imapclient.FetchMessageBuffer, after, through uint32) []domain.MailEvent {
	out := []domain.MailEvent{}
	for _, m := range msgs {
		uid := uint32(m.UID)
		if uid <= after || uid > through {
			continue
		}
		e := domain.MailEvent{AccountID: a.ID, Folder: "INBOX", Key: fmt.Sprintf("%s:INBOX:%s:%d", a.ID, generation, uid), Kind: "added", ReceivedAt: m.InternalDate, ClickURL: "https://mail.google.com/mail/"}
		if a.Provider == "qq" {
			e.ClickURL = "https://mail.qq.com/"
		}
		if m.Envelope != nil {
			e.Subject = domain.Clean(m.Envelope.Subject, 500)
			if len(m.Envelope.From) > 0 {
				from := m.Envelope.From[0]
				e.SenderName = domain.Clean(from.Name, 100)
				e.SenderAddress = domain.Clean(from.Mailbox+"@"+from.Host, 200)
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		ui, _ := strconv.ParseUint(out[i].Key[len(a.ID)+len(generation)+8:], 10, 32)
		uj, _ := strconv.ParseUint(out[j].Key[len(a.ID)+len(generation)+8:], 10, 32)
		return ui < uj
	})
	return out
}
func (p *Provider) Wait(ctx context.Context, a domain.Account, interval time.Duration) error {
	p.mu.Lock()
	s := p.sessions[a.ID]
	p.mu.Unlock()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	if s == nil || !s.c.Caps().Has(lib.CapIdle) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	stop := context.AfterFunc(ctx, func() { s.c.Close() })
	defer stop()
	idle, e := s.c.Idle()
	if e != nil {
		p.CloseAccount(a.ID)
		return &domain.ProviderError{Code: "transient"}
	}
	select {
	case <-ctx.Done():
		p.CloseAccount(a.ID)
		return ctx.Err()
	case <-s.c.Closed():
		p.CloseAccount(a.ID)
		return &domain.ProviderError{Code: "transient"}
	case <-s.changed:
	case <-timer.C:
	}
	if e = idle.Close(); e == nil {
		e = idle.Wait()
	}
	if e != nil {
		p.CloseAccount(a.ID)
		return &domain.ProviderError{Code: "transient"}
	}
	return nil
}
