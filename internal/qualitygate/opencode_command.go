package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

type opencodeCommand struct{}

func (opencodeCommand) Output(
	ctx context.Context,
	directory string,
	environment []string,
	maximum int,
	executable string,
	arguments ...string,
) (string, error) {
	stdout := newOpenCodeBoundedBuffer(maximum)
	stderr := newOpenCodeBoundedBuffer(maximumOpenCodeDiagnosticBytes)
	command := exec.CommandContext(ctx, executable, arguments...) // #nosec G204 -- executable and discrete arguments are gate-owned.
	command.Dir = directory
	command.Env = environment
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.Exceeded() || stderr.Exceeded() {
		return "", errors.New("OpenCode subprocess exceeded its output bound")
	}
	if err != nil {
		return "", fmt.Errorf("OpenCode subprocess failed: %w", err)
	}
	return stdout.String(), nil
}

func boundedCommandOutput(
	ctx context.Context,
	directory string,
	environment []string,
	maximum int,
	executable string,
	arguments ...string,
) (string, error) {
	return (opencodeCommand{}).Output(ctx, directory, environment, maximum, executable, arguments...)
}
