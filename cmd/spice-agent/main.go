//go:build !spice_generate

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	applicationCommand := command{input: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	code := applicationCommand.execute(ctx, os.Args[1:])
	stopSignals()
	os.Exit(code) //nolint:forbidigo // The command entrypoint must propagate its parsed exit code.
}
