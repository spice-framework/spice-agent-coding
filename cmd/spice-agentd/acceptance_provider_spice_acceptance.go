//go:build spice_acceptance && !spice_generate

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"

	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/tool"
)

const acceptanceProviderRecoveryText = "cancellation-recovery-complete"

type AcceptanceProvider struct {
	directory            string
	prefix               string
	cancellationScenario acceptanceScenario
	shellHelper          string
	mu                   sync.Mutex
	initialRequests      int
}

func NewAcceptanceProvider(configuration acceptanceProviderConfiguration) *AcceptanceProvider {
	return &AcceptanceProvider{
		directory:            configuration.directory,
		prefix:               configuration.prefix,
		cancellationScenario: configuration.scenario,
		shellHelper:          configuration.shellHelper,
	}
}

func (provider *AcceptanceProvider) Stream(ctx context.Context, request model.Request) (model.Stream, error) {
	if ctx == nil || provider == nil {
		return nil, errors.New("acceptance provider is unavailable")
	}
	if provider.cancellationScenario != acceptanceScenarioNone {
		return provider.cancellationStream(request)
	}
	toolResult := false
	for _, current := range request.Messages() {
		for _, part := range current.Parts() {
			if part.Kind() == message.PartToolResult {
				toolResult = true
				if !bytes.Contains(part.Data(), []byte("installed-acceptance-workspace-marker")) {
					return nil, errors.New("acceptance provider did not receive the compiled read result")
				}
			}
		}
	}
	if !toolResult {
		call, err := tool.NewCall(tool.CallID("acceptance-read"), "read", json.RawMessage(`{"path":"README.md"}`))
		if err != nil {
			return nil, err
		}
		callEvent, err := model.ToolCallEvent(call)
		if err != nil {
			return nil, err
		}
		completed, err := model.Completed(model.NewUsage(1, 1))
		if err != nil {
			return nil, err
		}
		return &acceptanceStream{events: []model.StreamEvent{callEvent, completed}}, nil
	}
	first := provider.prefix + "-one"
	second := provider.prefix + "-two"
	if provider.prefix == "replacement" {
		first = "replacement-complete"
		second = "replacement-finished"
	}
	firstEvent, err := model.TextDelta(first)
	if err != nil {
		return nil, err
	}
	secondEvent, err := model.TextDelta(second)
	if err != nil {
		return nil, err
	}
	completed, err := model.Completed(model.NewUsage(2, 2))
	if err != nil {
		return nil, err
	}
	return &acceptanceStream{
		events:     []model.StreamEvent{firstEvent, secondEvent, completed},
		checkpoint: filepath.Join(provider.directory, "checkpoint"),
		release:    filepath.Join(provider.directory, "release"),
	}, nil
}

func (provider *AcceptanceProvider) cancellationStream(request model.Request) (model.Stream, error) {
	if provider.hasToolResult(request) {
		return provider.recoveryStream()
	}
	provider.mu.Lock()
	provider.initialRequests++
	requestNumber := provider.initialRequests
	provider.mu.Unlock()
	if requestNumber > 1 {
		if provider.cancellationScenario == acceptanceScenarioPlugin {
			return provider.toolStream(
				"acceptance-plugin-recovery", "fixture.echo", json.RawMessage(`{"value":"slot-reused"}`),
			)
		}
		return provider.recoveryStream()
	}
	switch provider.cancellationScenario {
	case acceptanceScenarioProvider:
		return NewBlockingAcceptanceStream(provider.directory), nil
	case acceptanceScenarioShell:
		arguments, err := json.Marshal(struct {
			Argv []string `json:"argv"`
		}{Argv: []string{provider.shellHelper, "root", provider.directory}})
		if err != nil {
			return nil, err
		}
		return provider.toolStream("acceptance-shell-cancel", "shell", arguments)
	case acceptanceScenarioPlugin:
		return provider.toolStream("acceptance-plugin-cancel", "fixture.block", json.RawMessage(`{}`))
	default:
		return nil, errors.New("acceptance cancellation scenario is unsupported")
	}
}

func (provider *AcceptanceProvider) hasToolResult(request model.Request) bool {
	for _, current := range request.Messages() {
		for _, part := range current.Parts() {
			if part.Kind() == message.PartToolResult {
				return true
			}
		}
	}
	return false
}

func (provider *AcceptanceProvider) toolStream(
	id, name string,
	arguments json.RawMessage,
) (model.Stream, error) {
	call, err := tool.NewCall(tool.CallID(id), name, arguments)
	if err != nil {
		return nil, err
	}
	callEvent, err := model.ToolCallEvent(call)
	if err != nil {
		return nil, err
	}
	completed, err := model.Completed(model.NewUsage(1, 1))
	if err != nil {
		return nil, err
	}
	return &acceptanceStream{events: []model.StreamEvent{callEvent, completed}}, nil
}

func (provider *AcceptanceProvider) recoveryStream() (model.Stream, error) {
	textEvent, err := model.TextDelta(acceptanceProviderRecoveryText)
	if err != nil {
		return nil, err
	}
	completed, err := model.Completed(model.NewUsage(1, 1))
	if err != nil {
		return nil, err
	}
	return &acceptanceStream{events: []model.StreamEvent{textEvent, completed}}, nil
}
