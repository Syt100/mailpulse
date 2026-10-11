package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Syt100/mailpulse/internal/domain"
	"github.com/Syt100/mailpulse/internal/store/sqlite"
)

func TestContainerHealthAsNonRoot(t *testing.T)     { containerCheck(t, 10001) }
func TestCustomUIDCanWriteMountedData(t *testing.T) { containerCheck(t, 12345) }

func containerCheck(t *testing.T, uid int) {
	t.Helper()
	image := os.Getenv("MAILPULSE_TEST_IMAGE")
	if image == "" {
		t.Skip("set MAILPULSE_TEST_IMAGE to a locally built image; CI runs this check")
	}
	if _, e := exec.LookPath("docker"); e != nil {
		t.Fatal("Docker required when MAILPULSE_TEST_IMAGE is set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		b, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if e != nil {
			t.Fatalf("docker %s: %v\n%s", args[0], e, b)
		}
		return strings.TrimSpace(string(b))
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if e := os.Mkdir(data, 0700); e != nil {
		t.Fatal(e)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	if e := os.WriteFile(filepath.Join(dir, "master.key"), key, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := sqlite.Open(ctx, filepath.Join(data, "mailpulse.db"), 10000)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.BindKey(ctx, key); e != nil {
		t.Fatal(e)
	}
	b := domain.Batch{Next: domain.Cursor{AccountID: "fake", Folder: "INBOX", Position: "1", Initialized: true}, Events: []domain.MailEvent{{Key: "queued", AccountID: "fake", Kind: "added"}}}
	if e = s.CommitBatch(ctx, b, []domain.Decision{{EventKey: "queued", Notify: true, Payload: domain.Notification{Title: "fake", Message: "fake", Priority: 3}}}); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	owner := fmt.Sprintf("%d:%d", uid, uid)
	docker("run", "--rm", "--user", "0", "--entrypoint", "chown", "-v", dir+":/test", image, "-R", owner, "/test")
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if out, e := exec.CommandContext(c, "docker", "run", "--rm", "--user", "0", "--entrypoint", "chown", "-v", dir+":/test", image, "-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "/test").CombinedOutput(); e != nil {
			t.Errorf("restore mount ownership: %v %s", e, out)
		}
	})
	name := fmt.Sprintf("mailpulse-test-%d-%d", uid, time.Now().UnixNano())
	docker("run", "-d", "--name", name, "--read-only", "--user", owner, "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "-v", data+":/data", "-v", filepath.Join(dir, "master.key")+":/run/secrets/mailpulse_key:ro", image)
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		exec.CommandContext(c, "docker", "rm", "-f", name).Run()
	})
	ready := false
	for range 100 {
		if docker("inspect", "--format", "{{.State.Health.Status}}", name) == "healthy" {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("health timeout")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !ready {
		t.Fatalf("container unhealthy: %s", docker("logs", name))
	}
	t.Logf("image bytes=%s; memory with 0 active mailboxes and 1 queued fake job=%s", docker("image", "inspect", "--format", "{{.Size}}", image), docker("stats", "--no-stream", "--format", "{{.MemUsage}}", name))
	if got := docker("exec", name, "id", "-u"); got != fmt.Sprint(uid) {
		t.Fatalf("uid=%s", got)
	}
	docker("exec", name, "mailpulse", "healthcheck")
	status := docker("exec", name, "mailpulse", "status")
	if strings.Contains(status, "fake") {
		t.Fatal("status leaked notification payload")
	}
	docker("stop", "-t", "10", name)
	if got := docker("inspect", "--format", "{{.State.ExitCode}}", name); got != "0" {
		t.Fatalf("unclean stop: %s", got)
	}
	// Read the persisted queue inside a fresh container after restart.
	docker("start", name)
	var out string
	for range 50 {
		b, e := exec.CommandContext(ctx, "docker", "exec", name, "mailpulse", "outbox", "list").CombinedOutput()
		if e == nil {
			out = string(b)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(out, `"account_id": "fake"`) || strings.Contains(out, `"status": "sent"`) {
		t.Fatalf("unsent queue not preserved: %s", out)
	}
	docker("stop", "-t", "10", name)
	docker("run", "--rm", "--read-only", "--entrypoint", "sh", image, "-c", "test ! -e /run/secrets/mailpulse_key && test ! -e /data/mailpulse.db")
}
