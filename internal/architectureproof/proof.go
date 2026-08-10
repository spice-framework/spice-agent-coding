package architectureproof

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/message"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// Proof owns the kernel assembled entirely from generated Spice injection.
type Proof struct {
	engine  *agent.Engine
	fixture *ResponsesFixture
	tools   []string
}

// Run executes OpenAI translation, a compiled read tool, continuation, and a
// final text response through the real deterministic kernel.
func (proof *Proof) Run(ctx context.Context) (Report, error) {
	if proof == nil || proof.engine == nil || proof.fixture == nil {
		return Report{}, fmt.Errorf("architecture proof is not initialized")
	}
	input, err := proof.input()
	if err != nil {
		return Report{}, err
	}
	definition, err := agent.NewDefinition("architecture-proof", "proof-model", 3)
	if err != nil {
		return Report{}, err
	}
	run, err := proof.engine.Start(ctx, definition, input)
	if err != nil {
		return Report{}, err
	}
	subscription, err := run.Subscribe(ctx, 0)
	if err != nil {
		run.Cancel()
		return Report{}, err
	}
	report := proof.reportForRun(run)
	for envelope := range subscription.Events() {
		report.Kinds = append(report.Kinds, envelope.Kind())
		report.SecretSeen = report.SecretSeen || strings.Contains(string(envelope.Data()), fixtureSecret)
		if envelope.Kind() == event.ModelDelta {
			var payload struct {
				Text string `json:"text"`
			}
			if err = json.Unmarshal(envelope.Data(), &payload); err != nil {
				run.Cancel()
				return Report{}, fmt.Errorf("decode model delta: %w", err)
			}
			report.FinalText += payload.Text
		}
	}
	if err = subscription.Wait(ctx); err != nil {
		return Report{}, err
	}
	if err = run.Wait(ctx); err != nil {
		return Report{}, err
	}
	var violation string
	report.Requests, report.Authorized, report.Continuation, violation = proof.fixture.snapshot()
	if violation != "" {
		return Report{}, fmt.Errorf("responses fixture rejected request: %s", violation)
	}
	return report, nil
}

// RunCancellation proves that caller cancellation reaches the real provider
// request and still produces one terminal run event.
func (proof *Proof) RunCancellation(ctx context.Context) (Report, error) {
	if proof == nil || proof.engine == nil || proof.fixture == nil {
		return Report{}, fmt.Errorf("architecture proof is not initialized")
	}
	if ctx == nil {
		return Report{}, fmt.Errorf("architecture proof cancellation context is nil")
	}
	run, subscription, started, cancel, err := proof.startCancellationRun(ctx)
	if err != nil {
		return Report{}, err
	}
	defer cancel()
	select {
	case <-ctx.Done():
		run.Cancel()
		return Report{}, ctx.Err()
	case <-started:
		cancel()
	}
	return proof.finishCancellation(ctx, run, subscription)
}

func (proof *Proof) startCancellationRun(ctx context.Context) (
	*agent.Run,
	*event.Subscription,
	<-chan struct{},
	context.CancelFunc,
	error,
) {
	started, err := proof.fixture.prepareCancellation()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	input, err := proof.input()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	definition, err := agent.NewDefinition("architecture-proof-cancellation", "proof-model", 1)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	runContext, cancel := context.WithCancel(ctx)
	run, err := proof.engine.Start(runContext, definition, input)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	subscription, err := run.Subscribe(ctx, 0)
	if err != nil {
		cancel()
		run.Cancel()
		return nil, nil, nil, nil, err
	}
	return run, subscription, started, cancel, nil
}

func (proof *Proof) finishCancellation(
	ctx context.Context,
	run *agent.Run,
	subscription *event.Subscription,
) (Report, error) {
	report := proof.reportForRun(run)
	for envelope := range subscription.Events() {
		report.Kinds = append(report.Kinds, envelope.Kind())
		report.SecretSeen = report.SecretSeen || strings.Contains(string(envelope.Data()), fixtureSecret)
	}
	if err := subscription.Wait(ctx); err != nil {
		return Report{}, err
	}
	err := run.Wait(ctx)
	if err == nil {
		return Report{}, fmt.Errorf("cancelled run completed without cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		return Report{}, fmt.Errorf("cancelled run returned %w", err)
	}
	if !proof.fixture.cancellationObserved() {
		return Report{}, fmt.Errorf("provider request did not observe cancellation")
	}
	return report, nil
}

func (proof *Proof) reportForRun(run *agent.Run) Report {
	identity := run.PlanIdentity()
	return Report{
		Tools:                         slices.Clone(proof.tools),
		CompiledPlanIdentities:        identity.CompiledIdentities(),
		SnapshotCompatibilityIdentity: identity.SnapshotCompatibilityIdentity(),
		ToolPlanID:                    identity.ToolPlanID().String(),
		PlanFingerprint:               identity.Fingerprint(),
	}
}

func (proof *Proof) input() (agent.Input, error) {
	part, err := message.Text("Read README.md and report completion.")
	if err != nil {
		return agent.Input{}, err
	}
	inputMessage, err := message.New("architecture-proof-input", message.RoleUser, part)
	if err != nil {
		return agent.Input{}, err
	}
	return agent.NewInput(inputMessage)
}
