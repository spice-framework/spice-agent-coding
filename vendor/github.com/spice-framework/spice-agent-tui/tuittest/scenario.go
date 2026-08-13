package tuittest

import (
	"fmt"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

// Step is one scripted interaction in a scenario.
type Step struct {
	kind      stepKind
	stroke    string
	text      string
	action    agenttui.Action
	update    agenttui.SessionUpdate
	name      string
	width     int
	height    int
	hasSize   bool
	hasUpdate bool
}

type stepKind int

const (
	stepType stepKind = iota + 1
	stepKey
	stepAction
	stepUpdate
	stepResize
	stepSnapshot
)

// TypeStep types text into the prompt.
func TypeStep(text string) Step { return Step{kind: stepType, text: text} }

// KeyStep presses one keystroke.
func KeyStep(stroke string, text ...string) Step {
	payload := ""
	if len(text) > 0 {
		payload = text[0]
	}
	return Step{kind: stepKey, stroke: stroke, text: payload}
}

// ActionStep invokes a semantic action through its first standard binding.
func ActionStep(action agenttui.Action) Step {
	return Step{kind: stepAction, action: action}
}

// UpdateStep injects a session update.
func UpdateStep(update agenttui.SessionUpdate) Step {
	return Step{kind: stepUpdate, update: update, hasUpdate: true}
}

// ResizeStep changes the canvas size.
func ResizeStep(width, height int) Step {
	return Step{kind: stepResize, width: width, height: height, hasSize: true}
}

// SnapshotStep captures a named golden-ready screen.
func SnapshotStep(name string) Step { return Step{kind: stepSnapshot, name: name} }

func (step Step) label() string {
	switch step.kind {
	case stepType:
		return "type"
	case stepKey:
		return "key:" + step.stroke
	case stepAction:
		return "action:" + string(step.action)
	case stepUpdate:
		return "update"
	case stepResize:
		return "resize"
	case stepSnapshot:
		return "snapshot:" + step.name
	default:
		return "unknown"
	}
}

func (step Step) apply(driver *Driver) error {
	switch step.kind {
	case stepType:
		return driver.Type(step.text)
	case stepKey:
		if step.text != "" {
			return driver.Key(step.stroke, step.text)
		}
		return driver.Key(step.stroke)
	case stepAction:
		return driver.Action(step.action)
	case stepUpdate:
		if !step.hasUpdate {
			return fmt.Errorf("missing session update")
		}
		return driver.InjectUpdate(step.update)
	case stepResize:
		if !step.hasSize {
			return fmt.Errorf("missing size")
		}
		return driver.Resize(step.width, step.height)
	case stepSnapshot:
		_, err := driver.Snapshot(step.name)
		return err
	default:
		return fmt.Errorf("unsupported step kind %d", step.kind)
	}
}
