package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type opencodeInvocation struct{}

func (opencodeInvocation) Run(
	ctx context.Context,
	executable string,
	environment []string,
	repository string,
	model opencodeModel,
	evaluation opencodeCase,
) (opencodeEventSummary, time.Duration, string) {
	prompt, err := evaluation.Prompt()
	if err != nil {
		return opencodeEventSummary{}, 0, "infrastructure-prompt"
	}
	invocationContext, cancel := context.WithTimeout(ctx, maximumOpenCodeInvocationDuration)
	defer cancel()
	capture := (&opencodeEventCapture{}).newOpenCodeEventCapture(cancel, evaluation.MaximumTools, evaluation.MaximumSteps)
	stderr := (&opencodeBoundedBuffer{}).newOpenCodeBoundedBuffer(maximumOpenCodeDiagnosticBytes)
	command := exec.CommandContext( // #nosec G204 -- exact executable, model, agent, and discrete flags are gate-owned.
		invocationContext,
		executable,
		"run", "--pure", "--format", "json", "--model", model.OpenCodeID(), "--agent", evaluation.Agent,
		"--title", "spice-evaluation-"+evaluation.Name, "--dir", repository,
	)
	command.Dir = repository
	command.Env = environment
	command.Stdin = strings.NewReader(prompt)
	command.Stdout = capture
	command.Stderr = stderr
	started := time.Now()
	runErr := command.Run()
	duration := time.Since(started)
	summary := capture.Summary()
	if summary.SafetyFailure != "" || stderr.Exceeded() {
		if summary.SafetyFailure == "" {
			summary.SafetyFailure = "diagnostic output cap exceeded"
		}
		return summary, duration, "safety-failed"
	}
	if summary.ErrorClass != "" {
		return summary, duration, summary.ErrorClass
	}
	if errors.Is(invocationContext.Err(), context.DeadlineExceeded) {
		return summary, duration, "infrastructure-timeout"
	}
	if runErr != nil {
		return summary, duration, "infrastructure-cli"
	}
	return summary, duration, ""
}
