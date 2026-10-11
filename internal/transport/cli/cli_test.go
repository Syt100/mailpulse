package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Syt100/mailpulse/internal/app"
	"github.com/Syt100/mailpulse/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRPC struct {
	methods []string
	secret  string
	poll    int
	id      string
	params  []byte
}

func (f *fakeRPC) Call(ctx context.Context, method string, params, result any) error {
	f.methods = append(f.methods, method)
	raw, _ := json.Marshal(params)
	f.params = raw
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	json.Unmarshal(m["id"], &f.id)
	switch method {
	case "account.add":
		var p struct {
			Account domain.Account
			Secret  []byte
		}
		json.Unmarshal(raw, &p)
		f.secret = string(p.Secret)
		b, _ := json.Marshal(p.Account)
		json.Unmarshal(b, result)
	case "account.authorize":
		b, _ := json.Marshal(app.AuthorizationStart{SessionID: "session", Code: "fake-code", VerificationURL: "https://example.com"})
		json.Unmarshal(b, result)
	case "authorization.status":
		f.poll++
		b, _ := json.Marshal(app.AuthorizationStatus{State: "complete"})
		json.Unmarshal(b, result)
	case "status":
		b := []byte(`{"accounts":[{"account_id":"a","state":"auth_required"}],"pending_notifications":1}`)
		json.Unmarshal(b, result)
	}
	return nil
}

func TestOutboxListPaginationFlags(t *testing.T) {
	f := &fakeRPC{}
	var out bytes.Buffer
	err := run(context.Background(), []string{"outbox", "list", "--limit", "20", "--after", "cursor", "--status", "failed", "--account", "a"}, strings.NewReader(""), &out, &out, f)
	var q struct {
		Limit     int    `json:"limit"`
		After     string `json:"after"`
		Status    string `json:"status"`
		AccountID string `json:"account_id"`
	}
	if err != nil || json.Unmarshal(f.params, &q) != nil || q.Limit != 20 || q.After != "cursor" || q.Status != "failed" || q.AccountID != "a" {
		t.Fatalf("paging options not forwarded: %s %v", f.params, err)
	}
}
func TestSecretFileInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(p, []byte("fake-secret\n"), 0600)
	f := &fakeRPC{}
	var out bytes.Buffer
	e := run(context.Background(), []string{"account", "add", "--provider", "gmail", "--address", "demo@example.com", "--secret-file", p}, strings.NewReader(""), &out, &out, f)
	if e != nil || f.secret != "fake-secret" || strings.Contains(out.String(), "fake-secret") {
		t.Fatalf("%v %s", e, out.String())
	}
}
func TestPasswordNotAcceptedAsFlag(t *testing.T) {
	f := &fakeRPC{}
	var out bytes.Buffer
	e := run(context.Background(), []string{"account", "add", "--password=fake-sensitive"}, strings.NewReader(""), &out, &out, f)
	if e == nil || strings.Contains(out.String(), "fake-sensitive") {
		t.Fatal("password flag accepted or leaked")
	}
}
func TestCLIStatusRedacted(t *testing.T) {
	f := &fakeRPC{}
	var out bytes.Buffer
	e := run(context.Background(), []string{"status"}, strings.NewReader(""), &out, &out, f)
	if e != nil || !strings.Contains(out.String(), "auth_required") || strings.Contains(out.String(), "token") {
		t.Fatalf("%v %s", e, out.String())
	}
}
func TestAuthorizeSessionPolling(t *testing.T) {
	f := &fakeRPC{}
	var out bytes.Buffer
	e := run(context.Background(), []string{"account", "authorize", "--id", "a"}, strings.NewReader(""), &out, &out, f)
	if e != nil || f.poll < 1 || !strings.Contains(out.String(), "fake-code") {
		t.Fatalf("%v %s", e, out.String())
	}
}
func TestAccountRemoveConfirmation(t *testing.T) {
	f := &fakeRPC{}
	var out bytes.Buffer
	e := run(context.Background(), []string{"account", "remove", "--id", "a"}, strings.NewReader(""), &out, &out, f)
	if e == nil || len(f.methods) > 0 {
		t.Fatal("removed without --yes")
	}
}
func TestNotifyTestPublishesWithoutMail(t *testing.T) {
	f := &fakeRPC{}
	var out bytes.Buffer
	e := run(context.Background(), []string{"notify", "test"}, strings.NewReader(""), &out, &out, f)
	if e != nil || len(f.methods) != 1 || f.methods[0] != "notify.test" {
		t.Fatal(e)
	}
}
func TestOutboxRetry(t *testing.T) {
	f := &fakeRPC{}
	var out bytes.Buffer
	e := run(context.Background(), []string{"outbox", "retry", "--id", "job"}, strings.NewReader(""), &out, &out, f)
	if e != nil || f.id != "job" {
		t.Fatal(e)
	}
}
func TestDaemonRequiredForMutation(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	e := Run(context.Background(), []string{"--data-dir", dir, "account", "list"}, strings.NewReader(""), &out, &out)
	if e == nil {
		t.Fatal("no daemon accepted")
	}
	if _, e = os.Stat(filepath.Join(dir, "mailpulse.db")); !os.IsNotExist(e) {
		t.Fatal("CLI wrote DB directly")
	}
}
