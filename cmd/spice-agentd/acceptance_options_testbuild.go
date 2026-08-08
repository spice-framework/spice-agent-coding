//go:build spice_acceptance && !spice_generate

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/daemon"
	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/tool"
	spicebean "github.com/spice-framework/spice/bean"
)

const (
	acceptanceScopeEnvironment        = "SPICE_AGENT_ACCEPTANCE_SCOPE_DIRECTORY"
	acceptanceFaultTriggerEnvironment = "SPICE_AGENT_ACCEPTANCE_FAULT_TRIGGER"
	acceptanceFaultAckEnvironment     = "SPICE_AGENT_ACCEPTANCE_FAULT_ACK"
	acceptanceDiagnosticEnvironment   = "SPICE_AGENT_ACCEPTANCE_DIAGNOSTIC"
	acceptanceProviderEnvironment     = "SPICE_AGENT_ACCEPTANCE_PROVIDER_DIRECTORY"
	acceptanceResponseEnvironment     = "SPICE_AGENT_ACCEPTANCE_RESPONSE_PREFIX"
)

func acceptanceApplicationOptions(options spicegen.ApplicationOptions) (spicegen.ApplicationOptions, error) {
	directory := os.Getenv(acceptanceScopeEnvironment)
	if directory == "" {
		return spicegen.ApplicationOptions{}, errors.New("acceptance endpoint scope directory is required")
	}
	scope, err := endpoint.AcceptanceUserScope(directory)
	if err != nil {
		return spicegen.ApplicationOptions{}, err
	}
	options.Overrides.EndpointScope = spicebean.Replace(scope)

	trigger := os.Getenv(acceptanceFaultTriggerEnvironment)
	ack := os.Getenv(acceptanceFaultAckEnvironment)
	if trigger == "" || ack == "" {
		if trigger != "" || ack != "" {
			return spicegen.ApplicationOptions{}, errors.New("acceptance fault trigger and acknowledgement must be configured together")
		}
	} else {
		override := &faultingListenerFactory{trigger: trigger, ack: ack}
		options.Overrides.DaemonListenerFactory = spicebean.Replace[daemon.ListenerFactory](override)
	}
	providerDirectory := os.Getenv(acceptanceProviderEnvironment)
	responsePrefix := os.Getenv(acceptanceResponseEnvironment)
	if providerDirectory != "" || responsePrefix != "" {
		if !filepath.IsAbs(providerDirectory) || responsePrefix == "" || strings.TrimSpace(responsePrefix) != responsePrefix {
			return spicegen.ApplicationOptions{}, errors.New("acceptance provider configuration is invalid")
		}
		provider := &acceptanceProvider{directory: providerDirectory, prefix: responsePrefix}
		options.Overrides.OpenAIModelProvider = spicebean.Replace[model.Provider](provider)
	}
	return options, nil
}

func acceptanceDaemonRunner(runner daemoncommand.Runner) daemoncommand.Runner {
	path := os.Getenv(acceptanceDiagnosticEnvironment)
	if path == "" {
		return runner
	}
	return daemoncommand.RunnerFunc(func(ctx context.Context, options daemoncommand.Options) error {
		err := runner.Run(ctx, options)
		if err != nil {
			_ = os.WriteFile(path, []byte(err.Error()+"\n"), 0o600)
		}
		return err
	})
}

type faultingListenerFactory struct {
	trigger string
	ack     string
}

func (factory *faultingListenerFactory) Listen(address string) (net.Listener, error) {
	listener, err := daemon.NewListenerFactory().Listen(address)
	if err != nil {
		return nil, err
	}
	wrapped := &faultingListener{
		Listener: listener,
		trigger:  factory.trigger,
		ack:      factory.ack,
		closed:   make(chan struct{}),
		clients:  make(map[*faultingConnection]struct{}),
	}
	go wrapped.observeTrigger()
	return wrapped, nil
}

type faultingListener struct {
	net.Listener
	trigger string
	ack     string

	mu      sync.Mutex
	clients map[*faultingConnection]struct{}
	closed  chan struct{}
	once    sync.Once
	fault   sync.Once
}

func (listener *faultingListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &faultingConnection{Conn: connection, owner: listener}
	listener.mu.Lock()
	listener.clients[wrapped] = struct{}{}
	listener.mu.Unlock()
	return wrapped, nil
}

func (listener *faultingListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return listener.Listener.Close()
}

func (listener *faultingListener) observeTrigger() {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-listener.closed:
			return
		case <-ticker.C:
			if _, err := os.Stat(listener.trigger); err != nil {
				continue
			}
			listener.fault.Do(listener.faultConnections)
			return
		}
	}
}

func (listener *faultingListener) faultConnections() {
	listener.mu.Lock()
	connections := make([]*faultingConnection, 0, len(listener.clients))
	for connection := range listener.clients {
		connections = append(connections, connection)
	}
	listener.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	_ = os.WriteFile(listener.ack, []byte("faulted\n"), 0o600)
}

func (listener *faultingListener) remove(connection *faultingConnection) {
	listener.mu.Lock()
	delete(listener.clients, connection)
	listener.mu.Unlock()
}

type faultingConnection struct {
	net.Conn
	owner *faultingListener
	once  sync.Once
	err   error
}

func (connection *faultingConnection) Close() error {
	connection.once.Do(func() {
		connection.err = connection.Conn.Close()
		connection.owner.remove(connection)
	})
	return connection.err
}

var _ daemon.ListenerFactory = (*faultingListenerFactory)(nil)

type acceptanceProvider struct {
	directory string
	prefix    string
}

func (provider *acceptanceProvider) Stream(ctx context.Context, request model.Request) (model.Stream, error) {
	if ctx == nil || provider == nil {
		return nil, errors.New("acceptance provider is unavailable")
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

type acceptanceStream struct {
	mu         sync.Mutex
	events     []model.StreamEvent
	next       int
	checkpoint string
	release    string
	closed     bool
}

func (stream *acceptanceStream) Recv(ctx context.Context) (model.StreamEvent, error) {
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return model.StreamEvent{}, io.EOF
	}
	index := stream.next
	if index >= len(stream.events) {
		stream.mu.Unlock()
		return model.StreamEvent{}, io.EOF
	}
	if index == 0 && stream.checkpoint != "" {
		if err := os.WriteFile(stream.checkpoint, []byte("checkpoint\n"), 0o600); err != nil {
			stream.mu.Unlock()
			return model.StreamEvent{}, fmt.Errorf("write acceptance checkpoint: %w", err)
		}
	}
	if index == 1 && stream.release != "" {
		release := stream.release
		stream.mu.Unlock()
		if err := waitForAcceptanceRelease(ctx, release); err != nil {
			return model.StreamEvent{}, err
		}
		stream.mu.Lock()
		if stream.closed || stream.next != index {
			stream.mu.Unlock()
			return model.StreamEvent{}, io.EOF
		}
	}
	event := stream.events[index]
	stream.next++
	stream.mu.Unlock()
	return event, nil
}

func (stream *acceptanceStream) Close() error {
	stream.mu.Lock()
	stream.closed = true
	stream.mu.Unlock()
	return nil
}

func waitForAcceptanceRelease(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

var (
	_ model.Provider = (*acceptanceProvider)(nil)
	_ model.Stream   = (*acceptanceStream)(nil)
)
