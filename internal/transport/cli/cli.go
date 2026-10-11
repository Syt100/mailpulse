package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/config"
	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/notify/ntfy"
	"github.com/Syt100/mailpulse/internal/transport/control"
	"golang.org/x/term"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

var Version = "dev"

type RPC interface {
	Call(context.Context, string, any, any) error
}

func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}
func parse(f *flag.FlagSet, args []string) error {
	if f.Parse(args) != nil || len(f.Args()) > 0 {
		return fmt.Errorf("invalid options for %s", f.Name())
	}
	return nil
}
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	root := flags("mailpulse")
	path := root.String("config", "", "runtime JSON configuration")
	data := root.String("data-dir", "", "data directory")
	key := root.String("key-file", "", "32-byte key file")
	socket := root.String("socket", "", "private control socket")
	listen := root.String("listen", "", "health listen address")
	if root.Parse(args) != nil {
		return fmt.Errorf("invalid global options")
	}
	cfg, e := config.Load(*path)
	if e != nil {
		return e
	}
	if *data != "" {
		cfg.DataDir = *data
	}
	if *key != "" {
		cfg.KeyFile = *key
	}
	if *socket != "" {
		cfg.Socket = *socket
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if e = cfg.Finalize(); e != nil {
		return e
	}
	rest := root.Args()
	if len(rest) == 0 || rest[0] == "help" {
		fmt.Fprintln(stdout, "MailPulse: init | serve | status | account add/list/pause/resume/remove/authorize/resync | rule add/list/remove | notify configure/test | outbox list/retry | version | healthcheck\nGlobal options must precede the command: --data-dir --key-file --socket --listen --config")
		return nil
	}
	switch rest[0] {
	case "version":
		fmt.Fprintln(stdout, Version)
		return nil
	case "init":
		if len(rest) != 1 {
			return fmt.Errorf("init takes no options")
		}
		return initialize(ctx, cfg, stdout)
	case "serve":
		if len(rest) != 1 {
			return fmt.Errorf("serve takes no options")
		}
		return serve(ctx, cfg)
	case "healthcheck":
		_, port, e := net.SplitHostPort(cfg.Listen)
		if e != nil {
			return fmt.Errorf("invalid health listen address")
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+port+"/healthz", nil)
		res, e := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		if e != nil {
			return fmt.Errorf("healthcheck failed")
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("unhealthy")
		}
		return nil
	}
	return run(ctx, rest, stdin, stdout, stderr, control.NewClient(cfg.Socket))
}
func output(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func secretInput(path string, stdin io.Reader, stderr io.Writer) ([]byte, error) {
	var raw []byte
	var e error
	if path != "" {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("secret file must be regular, private and at most 16 KiB")
		}
		raw, e = os.ReadFile(path)
	} else if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stderr, "Secret: ")
		raw, e = term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
	} else {
		raw, e = io.ReadAll(io.LimitReader(stdin, 16385))
	}
	if e != nil || len(raw) > 16384 {
		return nil, fmt.Errorf("secret input unavailable")
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, fmt.Errorf("secret input required; use a private secret file or stdin")
	}
	return raw, nil
}
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, rpc RPC) error {
	if len(args) == 0 {
		return fmt.Errorf("command required")
	}
	var result any
	call := func(method string, params any) error {
		if e := rpc.Call(ctx, method, params, &result); e != nil {
			return e
		}
		return output(stdout, result)
	}
	if args[0] == "status" {
		if len(args) != 1 {
			return fmt.Errorf("status takes no options")
		}
		return call("status", struct{}{})
	}
	if len(args) < 2 {
		return fmt.Errorf("subcommand required")
	}
	group, command, rest := args[0], args[1], args[2:]
	switch group {
	case "account":
		switch command {
		case "list":
			if len(rest) != 0 {
				return fmt.Errorf("list takes no options")
			}
			return call("account.list", struct{}{})
		case "add":
			f := flags("account add")
			provider := f.String("provider", "", "gmail, qq or outlook")
			address := f.String("address", "", "primary email address")
			alias := f.String("alias", "", "display alias")
			file := f.String("secret-file", "", "IMAP application password file")
			clientID := f.String("client-id", "", "Microsoft application client ID")
			if e := parse(f, rest); e != nil {
				return e
			}
			a := domain.Account{Provider: *provider, Address: *address, Alias: *alias}
			var secret []byte
			var e error
			if *provider == "outlook" {
				a.Settings, _ = json.Marshal(map[string]string{"client_id": *clientID})
			} else {
				secret, e = secretInput(*file, stdin, stderr)
				if e != nil {
					return e
				}
				if *provider == "gmail" {
					secret = []byte(strings.ReplaceAll(string(secret), " ", ""))
				}
			}
			return call("account.add", struct {
				Account domain.Account `json:"account"`
				Secret  []byte         `json:"secret"`
			}{a, secret})
		case "pause", "resume", "remove", "resync", "authorize":
			f := flags("account " + command)
			id := f.String("id", "", "account ID")
			yes := f.Bool("yes", false, "confirm removal of account and queue")
			ignore := f.Bool("ignore-history", false, "establish a fresh baseline")
			if e := parse(f, rest); e != nil {
				return e
			}
			if *id == "" {
				return fmt.Errorf("--id required")
			}
			if command == "remove" && !*yes {
				return fmt.Errorf("--yes required: removes account, credentials and queued notifications")
			}
			if command == "resync" && !*ignore {
				return fmt.Errorf("--ignore-history required: indeterminate missed events will be skipped")
			}
			params := map[string]any{"id": *id, "confirmed": *yes, "ignore_history": *ignore}
			if command != "authorize" {
				return call("account."+command, params)
			}
			var start app.AuthorizationStart
			if e := rpc.Call(ctx, "account.authorize", params, &start); e != nil {
				return e
			}
			fmt.Fprintf(stdout, "Open %s and enter code %s\n", start.VerificationURL, start.Code)
			for {
				var status app.AuthorizationStatus
				if e := rpc.Call(ctx, "authorization.status", map[string]string{"id": start.SessionID}, &status); e != nil {
					return e
				}
				if status.State == "complete" {
					return output(stdout, status)
				}
				if status.State == "failed" {
					return fmt.Errorf("authorization failed; verify account, app registration and consent")
				}
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
	case "rule":
		switch command {
		case "list":
			if len(rest) != 0 {
				return fmt.Errorf("list takes no options")
			}
			return call("rule.list", struct{}{})
		case "remove":
			f := flags("rule remove")
			id := f.String("id", "", "rule ID")
			if e := parse(f, rest); e != nil {
				return e
			}
			if *id == "" {
				return fmt.Errorf("--id required")
			}
			return call("rule.remove", map[string]string{"id": *id})
		case "add":
			f := flags("rule add")
			r := domain.Rule{}
			f.IntVar(&r.Order, "order", 100, "first matching rule wins")
			f.StringVar(&r.Action, "action", "notify", "notify or ignore")
			f.IntVar(&r.Priority, "priority", 3, "1 through 5")
			f.StringVar(&r.AccountID, "account", "", "account ID filter")
			f.StringVar(&r.SenderAddress, "sender", "", "sender address")
			f.StringVar(&r.SenderDomain, "domain", "", "sender domain")
			f.StringVar(&r.SubjectContains, "subject-contains", "", "subject substring")
			f.BoolVar(&r.HideSubject, "hide-subject", false, "hide subject")
			f.BoolVar(&r.HideSender, "hide-sender", false, "hide sender")
			if e := parse(f, rest); e != nil {
				return e
			}
			return call("rule.add", r)
		}
	case "notify":
		switch command {
		case "test":
			if len(rest) != 0 {
				return fmt.Errorf("test takes no options")
			}
			return call("notify.test", struct{}{})
		case "configure":
			f := flags("notify configure")
			url := f.String("url", "", "ntfy base URL")
			topic := f.String("topic", "emails", "ntfy topic")
			file := f.String("token-file", "", "ntfy access token file")
			anonymous := f.Bool("anonymous", false, "explicitly omit token")
			if e := parse(f, rest); e != nil {
				return e
			}
			var b []byte
			var e error
			if !*anonymous {
				b, e = secretInput(*file, stdin, stderr)
				if e != nil {
					return e
				}
			}
			return call("notify.configure", ntfy.Config{URL: *url, Topic: *topic, Token: string(b)})
		}
	case "outbox":
		switch command {
		case "list":
			if len(rest) != 0 {
				return fmt.Errorf("list takes no options")
			}
			return call("outbox.list", struct{}{})
		case "retry":
			f := flags("outbox retry")
			id := f.String("id", "", "job ID")
			if e := parse(f, rest); e != nil {
				return e
			}
			if *id == "" {
				return fmt.Errorf("--id required")
			}
			return call("outbox.retry", map[string]string{"id": *id})
		}
	}
	return fmt.Errorf("unknown command; use mailpulse help")
}
