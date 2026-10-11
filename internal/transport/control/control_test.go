package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestControlSocketPermissions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir, err := os.MkdirTemp("/tmp", "mp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	s := NewServer(path, Services{})
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	for range 100 {
		info, e := os.Stat(path)
		if e == nil {
			if info.Mode().Perm() != 0600 {
				t.Fatal("socket not private")
			}
			cancel()
			<-done
			return
		}
		select {
		case e := <-done:
			if errors.Is(e, syscall.EPERM) {
				t.Skip("sandbox prohibits Unix socket creation; exercised in Linux CI")
			}
			t.Fatalf("server failed: %v", e)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no socket")
}
