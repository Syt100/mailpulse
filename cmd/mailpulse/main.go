package main

import (
	"context"
	"fmt"
	"github.com/Syt100/mailpulse/internal/transport/cli"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); e != nil {
		fmt.Fprintln(os.Stderr, "mailpulse:", e)
		os.Exit(1)
	}
}
