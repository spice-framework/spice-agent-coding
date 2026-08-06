package architectureproof

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
	"github.com/spice-framework/spice/lifecycle"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// Proof owns the kernel assembled entirely from generated Spice injection.
type Proof struct {
	engine  *agent.Engine
	fixture *ResponsesFixture
	tools   []string
}

// Report is inspectable evidence from one architecture-proof run.
type Report struct {
	Kinds        []event.Kind
	FinalText    string
	Tools        []string
	Requests     int
	Authorized   bool
	Continuation bool
}

// NewProof consumes the exact provider and canonical named tool map selected
// by Spice. It does not discover or register implementations at runtime.
//
// @Bean(name="proof")
func NewProof(
	provider model.Provider,
	tools map[string]tool.Tool,
	fixture *ResponsesFixture,
) (*Proof, lifecycle.Cleanup, error) {
	dispatcher, err := stage.NewDispatcher(tools)
	if err != nil {
		return nil, nil, fmt.Errorf("construct architecture-proof dispatcher: %w", err)
	}
	options := agent.DefaultEngineOptions()
	options.MetadataNamespaces = []string{"github.com/spice-framework/spice-agent-provider-openai"}
	options.StaticPlanIdentities = []string{
		"broker:unavailable",
		"provider:architecture-proof-openai",
		"stage:kernel",
	}
	engine, err := agent.NewEngineWithOptions(
		provider,
		dispatcher,
		&agent.AtomicIDSource{},
		time.Now,
		nil,
		nil,
		options,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("construct architecture-proof engine: %w", err)
	}
	names := make([]string, 0, len(tools))
	for _, definition := range dispatcher.Definitions() {
		names = append(names, definition.Name())
	}
	return &Proof{engine: engine, fixture: fixture, tools: names}, engine.Shutdown, nil
}

// Run executes OpenAI translation, a compiled read tool, continuation, and a
// final text response through the real deterministic kernel.
func (proof *Proof) Run(ctx context.Context) (Report, error) {
	if proof == nil || proof.engine == nil || proof.fixture == nil {
		return Report{}, fmt.Errorf("architecture proof is not initialized")
	}
	input, err := architectureProofInput()
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
	report := Report{Tools: slices.Clone(proof.tools)}
	for envelope := range subscription.Events() {
		report.Kinds = append(report.Kinds, envelope.Kind())
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

func architectureProofInput() (agent.Input, error) {
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
