package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/config"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/notify/ntfy"
	"github.com/Syt100/mailpulse/internal/providers/graph"
	imap "github.com/Syt100/mailpulse/internal/providers/imap"
	runner "github.com/Syt100/mailpulse/internal/runtime"
	"github.com/Syt100/mailpulse/internal/security"
	"github.com/Syt100/mailpulse/internal/store/sqlite"
	"github.com/Syt100/mailpulse/internal/transport/control"
	health "github.com/Syt100/mailpulse/internal/transport/http"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func initialize(ctx context.Context, cfg config.Config, out io.Writer) error {
	if e := os.MkdirAll(cfg.DataDir, 0700); e != nil {
		return e
	}
	lock, e := runner.AcquireLock(cfg.Database() + ".lock")
	if e != nil {
		return e
	}
	defer lock.Close()
	_, dbErr := os.Stat(cfg.Database())
	_, keyErr := os.Stat(cfg.KeyFile)
	if keyErr != nil {
		if !os.IsNotExist(keyErr) || !os.IsNotExist(dbErr) {
			return fmt.Errorf("existing database requires its original key")
		}
		if e = os.MkdirAll(filepath.Dir(cfg.KeyFile), 0700); e != nil {
			return e
		}
		f, e := os.OpenFile(cfg.KeyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		key := make([]byte, 32)
		if _, e = rand.Read(key); e == nil {
			_, e = f.Write(key)
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	key, e := security.LoadKey(cfg.KeyFile)
	if e != nil {
		return e
	}
	s, e := sqlite.Open(ctx, cfg.Database(), cfg.Capacity)
	if e != nil {
		return e
	}
	defer s.Close()
	if e = s.BindKey(ctx, key); e != nil {
		return e
	}
	fmt.Fprintln(out, "MailPulse initialized. Keep the master key in a separate, protected backup.")
	return nil
}
func serve(ctx context.Context, cfg config.Config) error {
	key, e := security.LoadKey(cfg.KeyFile)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(cfg.DataDir, 0700); e != nil {
		return e
	}
	lock, e := runner.AcquireLock(cfg.Database() + ".lock")
	if e != nil {
		return e
	}
	defer lock.Close()
	s, e := sqlite.Open(ctx, cfg.Database(), cfg.Capacity)
	if e != nil {
		return e
	}
	defer s.Close()
	if e = s.BindKey(ctx, key); e != nil {
		return e
	}
	publisher := &ntfy.Dynamic{Secrets: s}
	deliveries := app.NewDeliveryService(s, publisher)
	oauth := &graph.OAuth{Secrets: s, Accounts: s}
	providers := map[string]domain.Provider{"gmail": imap.NewProvider(s, nil), "qq": imap.NewProvider(s, nil), "outlook": graph.NewProvider(oauth, nil)}
	super := runner.NewSupervisor(s, providers, app.NewProcessor(s), deliveries)
	var level slog.Level
	level.UnmarshalText([]byte(cfg.LogLevel))
	super.Logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	super.Interval = time.Duration(cfg.PollSeconds) * time.Second
	auth := app.NewAuthorizationService(oauth, s.GetAccount)
	auth.OnComplete = super.Wake
	defer auth.Close()
	controlServer := control.NewServer(cfg.Socket, control.Services{Accounts: app.NewAccountService(s, s), Rules: app.NewRuleService(s), Authorization: auth, Store: s, Supervisor: super, Publisher: publisher})
	httpServer := &http.Server{Addr: cfg.Listen, Handler: health.Handler(super), ReadHeaderTimeout: 5 * time.Second}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	start := func(fn func() error) { wg.Add(1); go func() { defer wg.Done(); errs <- fn() }() }
	start(func() error { return super.Run(ctx) })
	start(func() error { return controlServer.Serve(ctx) })
	start(func() error {
		e := httpServer.ListenAndServe()
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	})
	var result error
	select {
	case <-ctx.Done():
	case result = <-errs:
	}
	cancel()
	auth.Close()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	httpServer.Shutdown(shutdown)
	wg.Wait()
	return result
}
