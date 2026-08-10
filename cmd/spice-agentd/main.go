//go:build !spice_generate

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	applicationCommand := command{input: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	ctx, stopParentControl := applicationCommand.withParentControl(signalContext, applicationCommand.isTerminal())
	code := applicationCommand.execute(ctx, os.Args[1:])
	stopParentControl()
	stopSignals()
	os.Exit(code) //nolint:forbidigo // The command entrypoint must propagate its parsed exit code.
}
