package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/notify/ntfy"
	"github.com/Syt100/mailpulse/internal/store/sqlite"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

type Store interface {
	ListOutbox(context.Context) ([]sqlite.OutboxItem, error)
	Retry(context.Context, string) error
	Unblock(context.Context) error
	Statuses(context.Context) ([]domain.AccountStatus, error)
	CountPending(context.Context) (int, error)
	Save(context.Context, string, []byte) error
}
type Supervisor interface {
	Wake(string)
	Ready() bool
}
type Services struct {
	Accounts      *app.AccountService
	Rules         *app.RuleService
	Authorization *app.AuthorizationService
	Store         Store
	Supervisor    Supervisor
	Publisher     domain.Publisher
}
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type Response struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}
type Server struct {
	path     string
	services Services
}

func NewServer(path string, services Services) *Server { return &Server{path, services} }
func (s *Server) Serve(ctx context.Context) error {
	if info, e := os.Lstat(s.path); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("control path is not a socket")
		}
		if e = os.Remove(s.path); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	listener, e := net.Listen("unix", s.path)
	if e != nil {
		return e
	}
	defer listener.Close()
	defer os.Remove(s.path)
	if e = os.Chmod(s.path, 0600); e != nil {
		return e
	}
	server := &http.Server{Handler: http.HandlerFunc(s.handle), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(c)
		case <-done:
		}
	}()
	e = server.Serve(listener)
	close(done)
	if e == http.ErrServerClosed || ctx.Err() != nil {
		return nil
	}
	return e
}
func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fmt.Errorf("invalid parameters")
	}
	var trailing any
	if e := d.Decode(&trailing); e != io.EOF {
		return fmt.Errorf("trailing parameters")
	}
	return nil
}
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" || r.URL.Path != "/rpc" {
		http.Error(w, "not found", 404)
		return
	}
	var q Request
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if e != nil || decode(raw, &q) != nil {
		json.NewEncoder(w).Encode(Response{Error: "invalid_request"})
		return
	}
	value, e := s.dispatch(r.Context(), q)
	if e != nil {
		json.NewEncoder(w).Encode(Response{Error: "operation_failed"})
		return
	}
	data, e := json.Marshal(value)
	if e != nil {
		json.NewEncoder(w).Encode(Response{Error: "serialization_failed"})
		return
	}
	json.NewEncoder(w).Encode(Response{Data: data})
}
func (s *Server) dispatch(ctx context.Context, q Request) (any, error) {
	var id struct {
		ID            string `json:"id"`
		Confirmed     bool   `json:"confirmed,omitempty"`
		IgnoreHistory bool   `json:"ignore_history,omitempty"`
	}
	sv := s.services
	switch q.Method {
	case "account.list":
		return sv.Accounts.List(ctx)
	case "account.add":
		var p struct {
			Account domain.Account `json:"account"`
			Secret  []byte         `json:"secret"`
		}
		if e := decode(q.Params, &p); e != nil {
			return nil, e
		}
		a, e := sv.Accounts.Add(ctx, p.Account, p.Secret)
		if e == nil && sv.Supervisor != nil {
			sv.Supervisor.Wake(a.ID)
		}
		return a, e
	case "account.pause", "account.resume", "account.remove", "account.resync":
		if e := decode(q.Params, &id); e != nil {
			return nil, e
		}
		var e error
		switch q.Method {
		case "account.pause":
			e = sv.Accounts.Pause(ctx, id.ID)
		case "account.resume":
			e = sv.Accounts.Resume(ctx, id.ID)
		case "account.remove":
			e = sv.Accounts.Remove(ctx, id.ID, id.Confirmed)
		case "account.resync":
			e = sv.Accounts.Resync(ctx, id.ID, id.IgnoreHistory)
		}
		if e == nil && sv.Supervisor != nil {
			sv.Supervisor.Wake(id.ID)
		}
		return map[string]bool{"ok": e == nil}, e
	case "account.authorize":
		if e := decode(q.Params, &id); e != nil {
			return nil, e
		}
		return sv.Authorization.Start(ctx, id.ID)
	case "authorization.status":
		if e := decode(q.Params, &id); e != nil {
			return nil, e
		}
		status, e := sv.Authorization.Status(ctx, id.ID)
		return status, e
	case "rule.list":
		return sv.Rules.List(ctx)
	case "rule.add":
		var p domain.Rule
		if e := decode(q.Params, &p); e != nil {
			return nil, e
		}
		return sv.Rules.Add(ctx, p)
	case "rule.remove":
		if e := decode(q.Params, &id); e != nil {
			return nil, e
		}
		return nil, sv.Rules.Remove(ctx, id.ID)
	case "outbox.list":
		return sv.Store.ListOutbox(ctx)
	case "outbox.retry":
		if e := decode(q.Params, &id); e != nil {
			return nil, e
		}
		return nil, sv.Store.Retry(ctx, id.ID)
	case "notify.configure":
		var cfg ntfy.Config
		if e := decode(q.Params, &cfg); e != nil {
			return nil, e
		}
		if _, e := ntfy.NewClient(cfg.URL, cfg.Topic, nil, nil); e != nil {
			return nil, e
		}
		raw, _ := json.Marshal(cfg)
		if e := sv.Store.Save(ctx, "system:ntfy", raw); e != nil {
			return nil, e
		}
		return nil, sv.Store.Unblock(ctx)
	case "notify.test":
		return nil, sv.Publisher.Publish(ctx, domain.Notification{Title: "MailPulse 测试通知", Message: "邮箱通知推送连接正常。", Priority: 3})
	case "status":
		statuses, e := sv.Store.Statuses(ctx)
		if e != nil {
			return nil, e
		}
		n, e := sv.Store.CountPending(ctx)
		return struct {
			Accounts []domain.AccountStatus `json:"accounts"`
			Pending  int                    `json:"pending_notifications"`
			Ready    bool                   `json:"ready"`
		}{statuses, n, sv.Supervisor.Ready()}, e
	}
	return nil, fmt.Errorf("unknown method")
}

type Client struct {
	path string
	http *http.Client
}

func NewClient(path string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	return &Client{path, &http.Client{Transport: transport, Timeout: 40 * time.Second}}
}
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	raw, e := json.Marshal(params)
	if e != nil {
		return e
	}
	q, _ := json.Marshal(Request{Method: method, Params: raw})
	req, e := http.NewRequestWithContext(ctx, "POST", "http://mailpulse/rpc", bytes.NewReader(q))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := c.http.Do(req)
	if e != nil {
		return fmt.Errorf("MailPulse daemon unavailable; start serve and check socket permissions")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("control request rejected")
	}
	var response Response
	if e = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); e != nil {
		return fmt.Errorf("invalid control response")
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if result != nil && len(response.Data) > 0 {
		return json.Unmarshal(response.Data, result)
	}
	return nil
}
