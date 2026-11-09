package main

import (
	"context"
	"github.com/ReanSn0w/horizon/internal/manager"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := manager.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, nil)
	stop()
	os.Exit(code)
}
