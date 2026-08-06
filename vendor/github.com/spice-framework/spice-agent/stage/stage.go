package stage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/spice-framework/spice-agent/tool"
)

// Stage is one typed, constructor-injected pipeline transform. Each Input and
// Output instantiation is a distinct exact Go interface for Spice dependency
// resolution; it is not a runtime registry or universal middleware contract.
type Stage[Input, Output any] interface {
	Process(context.Context, Input) (Output, error)
}

// ToolDispatcher is the sole executable route for tool calls. Definition gives
// decorators an immutable capability snapshot before they delegate execution.
type ToolDispatcher interface {
	Definitions() []tool.Definition
	Definition(name string) (tool.Definition, bool)
	Dispatch(context.Context, tool.Call, tool.Reporter) (tool.Result, error)
}

// ToolDispatchDecorator wraps the canonical dispatcher. Spice supplies these
// as an ordered typed collection.
type ToolDispatchDecorator interface {
	Wrap(ToolDispatcher) ToolDispatcher
}

type toolEntry struct {
	definition     tool.Definition
	implementation tool.Tool
}

// Dispatcher is an immutable named tool snapshot constructed by Spice.
type Dispatcher struct {
	tools map[string]toolEntry
}

// NewDispatcher validates canonical bean names in sorted order and snapshots
// definitions and capabilities. Tool implementations remain trusted concurrent
// singleton beans.
func NewDispatcher(tools map[string]tool.Tool) (*Dispatcher, error) {
	result := make(map[string]toolEntry, len(tools))
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		implementation := tools[name]
		if implementation == nil {
			return nil, fmt.Errorf("tool bean %q is nil", name)
		}
		definition := implementation.Definition()
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("tool bean %q: %w", name, err)
		}
		if definition.Name() != name {
			return nil, fmt.Errorf("tool bean %q declares model name %q", name, definition.Name())
		}
		result[name] = toolEntry{definition: definition.Clone(), implementation: implementation}
	}
	return &Dispatcher{tools: result}, nil
}

// Definitions returns snapshotted definitions ordered by canonical bean name.
func (dispatcher *Dispatcher) Definitions() []tool.Definition {
	if dispatcher == nil {
		return []tool.Definition{}
	}
	names := slices.Sorted(maps.Keys(dispatcher.tools))
	result := make([]tool.Definition, 0, len(names))
	for _, name := range names {
		result = append(result, dispatcher.tools[name].definition.Clone())
	}
	return result
}

// Definition returns one immutable capability snapshot.
func (dispatcher *Dispatcher) Definition(name string) (tool.Definition, bool) {
	if dispatcher == nil {
		return tool.Definition{}, false
	}
	entry, found := dispatcher.tools[name]
	return entry.definition.Clone(), found
}

// Dispatch validates correlation and cancellation around one trusted in-process
// call. Cancellation is cooperative: a Tool that ignores ctx can still block its
// own goroutine and therefore must be treated as trusted code.
func (dispatcher *Dispatcher) Dispatch(ctx context.Context, call tool.Call, reporter tool.Reporter) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, errors.New("tool dispatch context must not be nil")
	}
	if dispatcher == nil {
		return tool.Result{}, errors.New("tool dispatcher is nil")
	}
	if err := call.Validate(); err != nil {
		return tool.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	entry, found := dispatcher.tools[call.Name()]
	if !found {
		return tool.Result{}, fmt.Errorf("tool %q is not available", call.Name())
	}
	scoped := &scopedReporter{callID: call.ID(), delegate: reporter}
	result := entry.implementation.Execute(ctx, call.Clone(), scoped)
	if err := scoped.Err(); err != nil {
		return tool.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if err := result.Validate(); err != nil {
		return tool.Result{}, fmt.Errorf("validate tool %q result: %w", call.Name(), err)
	}
	if result.CallID() != call.ID() {
		return tool.Result{}, fmt.Errorf("tool %q returned call ID %q for active call %q", call.Name(), result.CallID(), call.ID())
	}
	return result.Clone(), nil
}

type scopedReporter struct {
	mu       sync.Mutex
	callID   tool.CallID
	delegate tool.Reporter
	err      error
}

func (reporter *scopedReporter) Report(ctx context.Context, progress tool.Progress) error {
	err := reporter.report(ctx, progress)
	if err != nil {
		reporter.mu.Lock()
		if reporter.err == nil {
			reporter.err = err
		}
		reporter.mu.Unlock()
	}
	return err
}

func (reporter *scopedReporter) report(ctx context.Context, progress tool.Progress) error {
	if ctx == nil {
		return errors.New("tool progress context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := progress.Validate(); err != nil {
		return err
	}
	if progress.CallID() != reporter.callID {
		return fmt.Errorf("tool progress call ID %q does not match active call %q", progress.CallID(), reporter.callID)
	}
	if reporter.delegate == nil {
		return nil
	}
	return reporter.delegate.Report(ctx, progress)
}

func (reporter *scopedReporter) Err() error {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	return reporter.err
}
