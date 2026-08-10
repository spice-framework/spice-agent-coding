package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

const (
	accessibleTerminalWidth  = 120
	accessibleTerminalHeight = 24
)

type accessibleShell struct {
	session agenttui.Session
	initial agenttui.ViewData
	streams agenttui.TerminalIO
}

func (shell *accessibleShell) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("accessible shell context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	model := &accessibleModel{
		context: func() context.Context { return ctx }, session: shell.session,
		workspace: shell.initial.Workspace(), status: shell.initial.Status(),
		activity: shell.initial.Activity(),
	}
	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(bufio.NewReader(shell.streams.Input())),
		tea.WithOutput(shell.streams.Output()),
		tea.WithWindowSize(accessibleTerminalWidth, accessibleTerminalHeight),
	)
	_, err := program.Run()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

type sessionUpdateMessage struct {
	update agenttui.SessionUpdate
	err    error
}

type commandLane uint8

const (
	commandLaneOrdinary commandLane = iota + 1
	commandLaneCancel
)

type commandResultMessage struct {
	lane commandLane
	err  error
}

type accessibleModel struct {
	context    func() context.Context
	session    agenttui.Session
	workspace  agenttui.WorkspaceState
	status     agenttui.StatusState
	activity   []agenttui.Text
	history    []agenttui.Text
	prompt     string
	historyAt  int
	revision   uint64
	ready      bool
	performing bool
	cancelling bool
	terminals  uint64
}

func (model *accessibleModel) Init() tea.Cmd { return model.receive }

func (model *accessibleModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch current := message.(type) {
	case tea.KeyPressMsg:
		return model.updateKey(current)
	case sessionUpdateMessage:
		if current.err != nil {
			return model, tea.Quit
		}
		if err := model.apply(current.update); err != nil {
			return model, tea.Quit
		}
		return model, model.receive
	case commandResultMessage:
		switch current.lane {
		case commandLaneOrdinary:
			model.performing = false
		case commandLaneCancel:
			model.cancelling = false
		}
		if current.err != nil {
			model.appendActivity("operation failed; inspect application diagnostics")
		}
		return model, nil
	default:
		return model, nil
	}
}

func (model *accessibleModel) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := message.String()
	switch key {
	case "ctrl+c", "ctrl+q":
		return model, tea.Quit
	case "esc", "ctrl+x":
		return model.cancelActiveRun()
	case "enter":
		return model.submit()
	case "backspace":
		model.backspace()
	case "up":
		model.previousPrompt()
	case "down":
		model.nextPrompt()
	default:
		text := message.Key().Text
		if text != "" && len(model.prompt)+len(text) <= agenttui.MaximumTextBytes {
			model.prompt += text
		}
	}
	return model, nil
}

func (model *accessibleModel) cancelActiveRun() (tea.Model, tea.Cmd) {
	if model.cancelling {
		return model, nil
	}
	intent, err := agenttui.NewIntent(agenttui.IntentCancelActiveRun, nil)
	if err != nil {
		model.appendActivity("cancel is unavailable")
		return model, nil
	}
	model.cancelling = true
	return model, func() tea.Msg {
		_, performErr := model.session.Perform(model.context(), intent)
		return commandResultMessage{lane: commandLaneCancel, err: performErr}
	}
}

func (model *accessibleModel) submit() (tea.Model, tea.Cmd) {
	if model.performing || strings.TrimSpace(model.prompt) == "" {
		return model, nil
	}
	prompt, err := agenttui.NewText(model.prompt)
	if err != nil {
		model.appendActivity("prompt is invalid")
		return model, nil
	}
	intent, err := agenttui.NewIntent(agenttui.IntentSubmit, []agenttui.Text{prompt})
	if err != nil {
		model.appendActivity("prompt is invalid")
		return model, nil
	}
	model.prompt = ""
	model.historyAt = len(model.history)
	model.performing = true
	return model, func() tea.Msg {
		_, performErr := model.session.Perform(model.context(), intent)
		return commandResultMessage{lane: commandLaneOrdinary, err: performErr}
	}
}

func (model *accessibleModel) backspace() {
	if model.prompt == "" {
		return
	}
	_, size := utf8.DecodeLastRuneInString(model.prompt)
	model.prompt = model.prompt[:len(model.prompt)-size]
}

func (model *accessibleModel) previousPrompt() {
	if len(model.history) == 0 || model.historyAt == 0 {
		return
	}
	model.historyAt--
	model.prompt = model.history[model.historyAt].String()
}

func (model *accessibleModel) nextPrompt() {
	if model.historyAt+1 < len(model.history) {
		model.historyAt++
		model.prompt = model.history[model.historyAt].String()
		return
	}
	model.historyAt = len(model.history)
	model.prompt = ""
}

func (model *accessibleModel) View() tea.View {
	lines := []string{model.workspace.Title().String()}
	if len(model.activity) > 0 {
		lines = append(lines, "Activity:")
		for _, activity := range model.activity {
			for line := range strings.SplitSeq(activity.String(), "\n") {
				lines = append(lines, "- "+line)
			}
		}
	}
	lines = append(lines, "Prompt: "+model.prompt)
	lines = append(lines, fmt.Sprintf("Completed runs: %d", model.terminals))
	if model.ready {
		lines = append(lines, "[READY] connected to local Spice Agent daemon")
	} else {
		lines = append(lines, "[RECONNECTING] "+model.status.Message().String())
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = false
	return view
}

func (model *accessibleModel) receive() tea.Msg {
	update, err := model.session.Receive(model.context())
	return sessionUpdateMessage{update: update, err: err}
}

func (model *accessibleModel) apply(update agenttui.SessionUpdate) error {
	if err := update.Validate(); err != nil {
		return err
	}
	if update.Revision() <= model.revision {
		return errors.New("accessible terminal session revision is not increasing")
	}
	model.revision = update.Revision()
	switch update.Kind() {
	case agenttui.SessionUpdateSnapshot:
		snapshot, available := update.Snapshot()
		if !available {
			return errors.New("accessible terminal snapshot is unavailable")
		}
		model.workspace = snapshot.Workspace()
		model.status = snapshot.Status()
		model.activity = snapshot.Activity()
		model.history = snapshot.PromptHistory()
		model.historyAt = len(model.history)
		model.ready = true
	case agenttui.SessionUpdateActivity:
		activity, available := update.Activity()
		if !available {
			return errors.New("accessible terminal activity is unavailable")
		}
		model.activity = append(model.activity, activity)
		if activity.String() == "run.completed" {
			model.terminals++
		}
		if len(model.activity) > agenttui.MaximumActivityItems {
			model.activity = model.activity[len(model.activity)-agenttui.MaximumActivityItems:]
		}
	case agenttui.SessionUpdatePromptHistory:
		history, available := update.PromptHistory()
		if !available {
			return errors.New("accessible terminal prompt history is unavailable")
		}
		model.history = history
		model.historyAt = len(history)
	default:
		return fmt.Errorf("unsupported accessible terminal update %q", update.Kind())
	}
	return nil
}

func (model *accessibleModel) appendActivity(value string) {
	text, err := agenttui.NewText(value)
	if err != nil {
		return
	}
	model.activity = append(model.activity, text)
}

var _ agenttui.Shell = (*accessibleShell)(nil)
