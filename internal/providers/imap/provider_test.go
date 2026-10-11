package imap

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"github.com/Syt100/mailpulse/internal/domain"
	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type secret struct{}

func (secret) Load(context.Context, string) ([]byte, error) { return []byte("fake"), nil }
func (secret) Save(context.Context, string, []byte) error   { return nil }

type spy struct {
	imapserver.Session
	mu       *sync.Mutex
	readOnly *bool
	bad      *bool
}

func (s *spy) Select(m string, o *imaplib.SelectOptions) (*imaplib.SelectData, error) {
	s.mu.Lock()
	*s.readOnly = o != nil && o.ReadOnly
	s.mu.Unlock()
	return s.Session.Select(m, o)
}
func (s *spy) Fetch(w *imapserver.FetchWriter, set imaplib.NumSet, o *imaplib.FetchOptions) error {
	s.mu.Lock()
	if _, ok := set.(imaplib.UIDSet); !ok || len(o.BodySection) > 0 || o.BodyStructure != nil {
		*s.bad = true
	}
	s.mu.Unlock()
	return s.Session.Fetch(w, set, o)
}
func (s *spy) Store(w *imapserver.FetchWriter, set imaplib.NumSet, f *imaplib.StoreFlags, o *imaplib.StoreOptions) error {
	s.mu.Lock()
	*s.bad = true
	s.mu.Unlock()
	return s.Session.Store(w, set, f, o)
}

type fixture struct {
	p             *Provider
	user          *imapmemserver.User
	a             domain.Account
	mu            sync.Mutex
	readOnly, bad bool
}

func setup(t *testing.T, idle bool) *fixture {
	t.Helper()
	f := &fixture{user: imapmemserver.NewUser("demo@example.com", "fake"), a: domain.Account{ID: "a", Address: "demo@example.com", Provider: "gmail"}}
	f.user.Create("INBOX", nil)
	httpTLS := httptest.NewTLSServer(nil)
	cert := httpTLS.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(httpTLS.Certificate())
	httpTLS.Close()
	listener, e := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if e != nil {
		t.Fatal(e)
	}
	caps := imaplib.CapSet{imaplib.CapIMAP4rev1: {}}
	if idle {
		caps[imaplib.CapIdle] = struct{}{}
	}
	srv := imapserver.New(&imapserver.Options{Caps: caps, NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
		return &spy{imapmemserver.NewUserSession(f.user), &f.mu, &f.readOnly, &f.bad}, nil, nil
	}})
	go srv.Serve(listener)
	t.Cleanup(func() { f.p.Close(); srv.Close(); listener.Close() })
	f.p = NewProvider(secret{}, func(ctx context.Context, _ string) (net.Conn, error) {
		d := tls.Dialer{Config: &tls.Config{RootCAs: roots, ServerName: "example.com"}}
		return d.DialContext(ctx, "tcp", listener.Addr().String())
	})
	return f
}
func (f *fixture) append(t *testing.T, subject string) {
	t.Helper()
	r := strings.NewReader("From: Tester <sender@example.com>\r\nTo: demo@example.com\r\nSubject: " + subject + "\r\n\r\nbody")
	_, e := f.user.Append("INBOX", r, &imaplib.AppendOptions{Flags: []imaplib.Flag{imaplib.FlagSeen}, Time: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
}
func TestReadOnlyBaseline(t *testing.T) {
	f := setup(t, true)
	f.append(t, "old")
	b, e := f.p.Scan(context.Background(), f.a, domain.Cursor{})
	if e != nil || !b.Baseline || len(b.Events) != 0 || !b.Next.Initialized {
		t.Fatalf("%+v %v", b, e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.readOnly || f.bad {
		t.Fatal("non-readonly protocol")
	}
}
func TestUIDScanIncludesReadMail(t *testing.T) {
	f := setup(t, true)
	b, _ := f.p.Scan(context.Background(), f.a, domain.Cursor{})
	f.append(t, "new")
	b, e := f.p.Scan(context.Background(), f.a, b.Next)
	if e != nil || len(b.Events) != 1 || b.Events[0].Subject != "new" {
		t.Fatalf("%+v %v", b, e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bad {
		t.Fatal("body/write/sequence request")
	}
}
func TestUIDValidityChanged(t *testing.T) {
	f := setup(t, true)
	b, _ := f.p.Scan(context.Background(), f.a, domain.Cursor{})
	b.Next.Generation = "different"
	_, e := f.p.Scan(context.Background(), f.a, b.Next)
	var p *domain.ProviderError
	if !errors.As(e, &p) || p.Code != "needs_resync" {
		t.Fatal(e)
	}
}
func TestIdleDisconnectBackfills(t *testing.T) {
	f := setup(t, true)
	b, _ := f.p.Scan(context.Background(), f.a, domain.Cursor{})
	f.p.CloseAccount("a")
	f.append(t, "after reconnect")
	b, e := f.p.Scan(context.Background(), f.a, b.Next)
	if e != nil || len(b.Events) != 1 {
		t.Fatalf("%+v %v", b, e)
	}
}
func TestNoIdleFallsBackToPoll(t *testing.T) {
	f := setup(t, false)
	f.p.Scan(context.Background(), f.a, domain.Cursor{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if e := f.p.Wait(ctx, f.a, time.Hour); e != context.DeadlineExceeded {
		t.Fatal(e)
	}
}
func TestUnorderedUIDs(t *testing.T) {
	a := domain.Account{ID: "a"}
	msgs := []*imapclient.FetchMessageBuffer{{UID: 5}, {UID: 3}, {UID: 2}}
	out := events(a, "1", msgs, 2, 5)
	if len(out) != 2 || !strings.HasSuffix(out[0].Key, ":3") || !strings.HasSuffix(out[1].Key, ":5") {
		t.Fatalf("%+v", out)
	}
}
func TestMalformedMIMESubject(t *testing.T) {
	f := setup(t, true)
	b, _ := f.p.Scan(context.Background(), f.a, domain.Cursor{})
	f.append(t, "=?UTF-8?B?invalid?=")
	b, e := f.p.Scan(context.Background(), f.a, b.Next)
	if e != nil || len(b.Events) != 1 || !bytes.Equal([]byte(b.Events[0].Subject), []byte(strings.ToValidUTF8(b.Events[0].Subject, "�"))) {
		t.Fatalf("%+v %v", b, e)
	}
}
