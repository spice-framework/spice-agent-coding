package tuisession

import (
	"errors"
	"fmt"
	"time"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
)

// Config contains immutable construction inputs for a TUI client adapter.
type Config struct {
	InitializeRequest client.InitializeRequest
	Definition        client.DefinitionRef
	Workspace         agenttui.WorkspaceState
	InitialStatus     agenttui.StatusState
	ReplayLimit       uint32
	UpdateCapacity    uint32
	ReconnectDelay    time.Duration
}

// NewConfig constructs production defaults for a bounded TUI adapter.
func NewConfig(
	initialize client.InitializeRequest,
	definition client.DefinitionRef,
	workspace agenttui.WorkspaceState,
	status agenttui.StatusState,
) (Config, error) {
	config := Config{
		InitializeRequest: initialize,
		Definition:        definition,
		Workspace:         workspace,
		InitialStatus:     status,
		ReplayLimit:       defaultReplayLimit,
		UpdateCapacity:    defaultUpdateCapacity,
		ReconnectDelay:    defaultReconnectDelay,
	}
	return config, config.Validate()
}

// Validate reports whether construction inputs are complete and bounded.
func (config Config) Validate() error {
	if err := config.InitializeRequest.Validate(); err != nil {
		return fmt.Errorf("initialize request: %w", err)
	}
	if err := config.Definition.Validate(); err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	if err := config.Workspace.Validate(); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if err := config.InitialStatus.Validate(); err != nil {
		return fmt.Errorf("initial status: %w", err)
	}
	if config.ReplayLimit == 0 {
		return errors.New("event replay limit must be positive")
	}
	if config.UpdateCapacity == 0 || config.UpdateCapacity > maximumUpdateCapacity {
		return fmt.Errorf("update capacity must be between 1 and %d", maximumUpdateCapacity)
	}
	if config.ReconnectDelay < 0 || config.ReconnectDelay > maximumReconnectDelay {
		return fmt.Errorf("reconnect delay must be between zero and %s", maximumReconnectDelay)
	}
	return nil
}
