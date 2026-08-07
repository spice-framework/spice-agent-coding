package agenttui

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
)

const (
	// MaximumCommandArguments bounds one command invocation.
	MaximumCommandArguments = 32
	// MaximumIntentValues bounds one semantic effect request.
	MaximumIntentValues  = 32
	maximumIdentityBytes = 128
)

// Invocation is one immutable, bounded command invocation. It contains only
// user-supplied arguments and an operation identity; injected services belong
// on the command constructor.
type Invocation struct {
	operationID string
	arguments   []Text
}

// NewInvocation constructs an immutable command invocation.
func NewInvocation(operationID string, arguments []Text) (Invocation, error) {
	invocation := Invocation{operationID: operationID, arguments: slices.Clone(arguments)}
	return invocation, invocation.Validate()
}

// OperationID returns the caller-owned idempotency identity.
func (invocation Invocation) OperationID() string { return invocation.operationID }

// Arguments returns a defensive copy in source order.
func (invocation Invocation) Arguments() []Text { return slices.Clone(invocation.arguments) }

// Validate reports whether the invocation is initialized and bounded.
func (invocation Invocation) Validate() error {
	if err := validateIdentity(invocation.operationID, "invocation operation ID"); err != nil {
		return err
	}
	if len(invocation.arguments) > MaximumCommandArguments {
		return fmt.Errorf("command arguments exceed %d", MaximumCommandArguments)
	}
	for index, argument := range invocation.arguments {
		if err := argument.Validate(); err != nil {
			return fmt.Errorf("command argument %d: %w", index, err)
		}
	}
	return nil
}

// IntentKind identifies one UI-requested semantic operation without exposing a
// client, protocol, daemon, or service registry.
type IntentKind string

const (
	// IntentSubmit requests submission of the current prompt.
	IntentSubmit IntentKind = "submit"
	// IntentCancelActiveRun requests cancellation of the active run.
	IntentCancelActiveRun IntentKind = "cancel-active-run"
	// IntentRespond requests a response to the current pending interaction.
	IntentRespond IntentKind = "respond"
)

// Intent is an immutable, bounded semantic operation for the later client
// adapter. Values are ordered and deliberately UI-neutral.
type Intent struct {
	kind   IntentKind
	values []Text
}

// NewIntent constructs a bounded semantic operation.
func NewIntent(kind IntentKind, values []Text) (Intent, error) {
	intent := Intent{kind: kind, values: slices.Clone(values)}
	return intent, intent.Validate()
}

// Kind returns the semantic operation kind.
func (intent Intent) Kind() IntentKind { return intent.kind }

// Values returns a defensive copy in source order.
func (intent Intent) Values() []Text { return slices.Clone(intent.values) }

// Validate reports whether the semantic operation is supported and bounded.
func (intent Intent) Validate() error {
	if intent.kind != IntentSubmit && intent.kind != IntentCancelActiveRun && intent.kind != IntentRespond {
		return fmt.Errorf("unsupported intent kind %q", intent.kind)
	}
	if len(intent.values) > MaximumIntentValues {
		return fmt.Errorf("intent values exceed %d", MaximumIntentValues)
	}
	for index, value := range intent.values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("intent value %d: %w", index, err)
		}
	}
	if intent.kind == IntentCancelActiveRun && len(intent.values) != 0 {
		return errors.New("cancel-active-run intent must not contain values")
	}
	if (intent.kind == IntentSubmit || intent.kind == IntentRespond) && len(intent.values) != 1 {
		return fmt.Errorf("%s intent requires exactly one value", intent.kind)
	}
	return nil
}

// CommandResult is one immutable, bounded command result. An optional intent
// lets the presentation request later application work without granting the
// command access to a registry or transport.
type CommandResult struct {
	message   Text
	intent    Intent
	hasIntent bool
}

// NewCommandResult constructs a result with an optional semantic intent.
func NewCommandResult(message Text, intent *Intent) (CommandResult, error) {
	result := CommandResult{message: message}
	if intent != nil {
		result.intent = *intent
		result.hasIntent = true
	}
	return result, result.Validate()
}

// Message returns bounded user-facing result text.
func (result CommandResult) Message() Text { return result.message }

// Intent returns the optional semantic intent.
func (result CommandResult) Intent() (Intent, bool) { return result.intent, result.hasIntent }

// Validate reports whether the result is terminal-safe and complete.
func (result CommandResult) Validate() error {
	if err := result.message.Validate(); err != nil {
		return fmt.Errorf("command result message: %w", err)
	}
	if result.hasIntent {
		if err := result.intent.Validate(); err != nil {
			return fmt.Errorf("command result intent: %w", err)
		}
	} else if result.intent.kind != "" || len(result.intent.values) != 0 {
		return errors.New("command result contains an unmarked intent")
	}
	return nil
}

// ValidateCommands validates command metadata and rejects duplicate stable IDs.
// Diagnostics are deterministic in injection order.
func ValidateCommands(commands []Command) error {
	seen := make(map[string]int, len(commands))
	for index, command := range commands {
		if command == nil {
			return fmt.Errorf("command %d must not be nil", index)
		}
		if err := validateIdentity(command.ID(), "command ID"); err != nil {
			return fmt.Errorf("command %d: %w", index, err)
		}
		if err := command.Summary().Validate(); err != nil {
			return fmt.Errorf("command %q summary: %w", command.ID(), err)
		}
		if previous, exists := seen[command.ID()]; exists {
			return fmt.Errorf("command ID %q collides at indexes %d and %d", command.ID(), previous, index)
		}
		seen[command.ID()] = index
	}
	return nil
}

// TerminalIO contains caller-owned terminal streams. Constructing this value
// never opens a terminal or acquires a daemon.
type TerminalIO struct {
	input  io.Reader
	output io.Writer
}

// NewTerminalIO validates caller-owned terminal streams.
func NewTerminalIO(input io.Reader, output io.Writer) (TerminalIO, error) {
	terminal := TerminalIO{input: input, output: output}
	return terminal, terminal.Validate()
}

// Input returns the caller-owned terminal input.
func (terminal TerminalIO) Input() io.Reader { return terminal.input }

// Output returns the caller-owned terminal output.
func (terminal TerminalIO) Output() io.Writer { return terminal.output }

// Validate reports whether both streams are present.
func (terminal TerminalIO) Validate() error {
	if terminal.input == nil {
		return errors.New("terminal input must not be nil")
	}
	if terminal.output == nil {
		return errors.New("terminal output must not be nil")
	}
	return nil
}

// TerminalConfig is immutable presentation-only startup policy. Definition
// selection, reconnect, and shutdown policy belong to the distribution's
// client runner rather than the Bubble Tea shell.
type TerminalConfig struct {
	accessible bool
}

// NewTerminalConfig constructs terminal presentation policy.
func NewTerminalConfig(accessible bool) TerminalConfig { return TerminalConfig{accessible: accessible} }

// Accessible reports whether line-oriented accessible presentation is enabled.
func (config TerminalConfig) Accessible() bool { return config.accessible }

func validateIdentity(value, name string) error {
	if value == "" || len(value) > maximumIdentityBytes || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be a non-empty identity of at most %d bytes", name, maximumIdentityBytes)
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune("-._:/", character) {
			return fmt.Errorf("%s contains unsupported character %q", name, character)
		}
	}
	return nil
}
