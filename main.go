package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"kubeops.dev/quota-locker/cmd"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := cmd.Execute(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
