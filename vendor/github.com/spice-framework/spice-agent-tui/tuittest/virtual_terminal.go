package tuittest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/x/vt"
)

const (
	defaultVirtualTerminalWidth      = 80
	defaultVirtualTerminalHeight     = 24
	defaultVirtualTerminalTranscript = 1 << 20
	maximumVirtualTerminalWidth      = 512
	maximumVirtualTerminalHeight     = 256
	maximumVirtualTerminalTranscript = 16 << 20
	maximumVirtualTerminalResizes    = 4096
)

// VirtualTerminal interprets bounded terminal output into deterministic Screen
// captures. It owns no process, PTY, daemon, clock, or network connection.
type VirtualTerminal struct {
	mu sync.Mutex

	transcript    bytes.Buffer
	transcriptMax int
	segments      []virtualTerminalSegment
	notify        chan struct{}
	closed        bool
}

// NewVirtualTerminal constructs an output-only modern terminal emulator.
func NewVirtualTerminal(options VirtualTerminalOptions) (*VirtualTerminal, error) {
	width := options.Width
	if width == 0 {
		width = defaultVirtualTerminalWidth
	}
	height := options.Height
	if height == 0 {
		height = defaultVirtualTerminalHeight
	}
	limit := options.MaxTranscriptBytes
	if limit == 0 {
		limit = defaultVirtualTerminalTranscript
	}
	if err := validateVirtualTerminalDimensions(width, height); err != nil {
		return nil, err
	}
	if limit < 1 || limit > maximumVirtualTerminalTranscript {
		return nil, fmt.Errorf(
			"virtual terminal transcript limit must be in [1,%d] bytes",
			maximumVirtualTerminalTranscript,
		)
	}
	terminal := &VirtualTerminal{
		transcriptMax: limit,
		segments: []virtualTerminalSegment{{
			width:  width,
			height: height,
		}},
		notify: make(chan struct{}),
	}
	return terminal, nil
}

// Write interprets one output chunk atomically and retains its exact bytes.
func (terminal *VirtualTerminal) Write(content []byte) (int, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.closed {
		return 0, errors.New("virtual terminal is closed")
	}
	if len(content) > terminal.transcriptMax-terminal.transcript.Len() {
		return 0, errors.New("virtual terminal transcript limit exceeded")
	}
	_, _ = terminal.transcript.Write(content)
	current := &terminal.segments[len(terminal.segments)-1]
	_, _ = current.output.Write(content)
	terminal.signalLocked()
	return len(content), nil
}

// WriteString interprets one string output chunk.
func (terminal *VirtualTerminal) WriteString(content string) (int, error) {
	return terminal.Write([]byte(content))
}

// Resize changes both terminal buffers and wakes capture waiters.
func (terminal *VirtualTerminal) Resize(width, height int) error {
	if err := validateVirtualTerminalDimensions(width, height); err != nil {
		return err
	}
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.closed {
		return errors.New("virtual terminal is closed")
	}
	if len(terminal.segments) >= maximumVirtualTerminalResizes+1 {
		return errors.New("virtual terminal resize limit exceeded")
	}
	terminal.segments = append(terminal.segments, virtualTerminalSegment{
		width:  width,
		height: height,
	})
	terminal.signalLocked()
	return nil
}

// Screen captures the interpreted cell grid, styles, cursor, and screen mode.
func (terminal *VirtualTerminal) Screen(name string) (Screen, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.screenLocked(name)
}

// Transcript returns an exact defensive copy of all accepted output bytes.
func (terminal *VirtualTerminal) Transcript() []byte {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return bytes.Clone(terminal.transcript.Bytes())
}

// WaitFor waits without polling or sleeps until predicate accepts a capture.
func (terminal *VirtualTerminal) WaitFor(
	ctx context.Context,
	name string,
	predicate func(Screen) bool,
) (Screen, error) {
	if ctx == nil {
		return Screen{}, errors.New("virtual terminal wait context must not be nil")
	}
	if predicate == nil {
		return Screen{}, errors.New("virtual terminal predicate must not be nil")
	}
	for {
		terminal.mu.Lock()
		screen, captureErr := terminal.screenLocked(name)
		wait := terminal.notify
		closed := terminal.closed
		terminal.mu.Unlock()
		if captureErr != nil {
			return Screen{}, captureErr
		}
		matched, err := evaluateTerminalPredicate(predicate, screen)
		if err != nil {
			return Screen{}, err
		}
		if matched {
			return screen, nil
		}
		if closed {
			return Screen{}, errors.New("virtual terminal closed before expected screen")
		}
		select {
		case <-ctx.Done():
			return Screen{}, fmt.Errorf("wait for virtual terminal screen: %w", context.Cause(ctx))
		case <-wait:
		}
	}
}

// Close rejects later output and wakes every waiter. Captures remain available.
func (terminal *VirtualTerminal) Close() error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.closed {
		return nil
	}
	terminal.closed = true
	terminal.signalLocked()
	return nil
}

func (terminal *VirtualTerminal) screenLocked(name string) (Screen, error) {
	first := terminal.segments[0]
	emulator := vt.NewEmulator(first.width, first.height)
	cursorVisible := true
	altScreen := false
	emulator.SetCallbacks(vt.Callbacks{
		AltScreen: func(enabled bool) {
			altScreen = enabled
		},
		CursorVisibility: func(visible bool) {
			cursorVisible = visible
		},
	})
	for index := range terminal.segments {
		segment := &terminal.segments[index]
		if index > 0 {
			emulator.Resize(segment.width, segment.height)
		}
		if segment.output.Len() == 0 {
			continue
		}
		written, err := emulator.Write(segment.output.Bytes())
		if err != nil {
			interpretErr := fmt.Errorf("interpret virtual terminal output: %w", err)
			if closeErr := emulator.Close(); closeErr != nil {
				interpretErr = errors.Join(
					interpretErr,
					fmt.Errorf("close failed virtual terminal capture: %w", closeErr),
				)
			}
			return Screen{}, interpretErr
		}
		if written != segment.output.Len() {
			interpretErr := errors.New("virtual terminal interpreted a partial output segment")
			if closeErr := emulator.Close(); closeErr != nil {
				interpretErr = errors.Join(
					interpretErr,
					fmt.Errorf("close partial virtual terminal capture: %w", closeErr),
				)
			}
			return Screen{}, interpretErr
		}
	}
	width := emulator.Width()
	height := emulator.Height()
	position := emulator.CursorPosition()
	plain := plainVirtualTerminal(emulator, width, height)
	screen := Screen{
		name:          name,
		width:         width,
		height:        height,
		styled:        NormalizeStyled(emulator.Render()),
		plain:         plain,
		plainLines:    strings.Split(plain, "\n"),
		cursorX:       position.X,
		cursorY:       position.Y,
		cursorVisible: cursorVisible,
		altScreen:     altScreen,
	}
	if err := emulator.Close(); err != nil {
		return Screen{}, fmt.Errorf("close virtual terminal capture: %w", err)
	}
	return screen, nil
}

func plainVirtualTerminal(emulator *vt.Emulator, width, height int) string {
	var plain strings.Builder
	plain.Grow((width + 1) * height)
	for y := range height {
		for x := 0; x < width; {
			cell := emulator.CellAt(x, y)
			if cell == nil || cell.String() == "" {
				plain.WriteByte(' ')
				x++
				continue
			}
			plain.WriteString(cell.String())
			cellWidth := max(cell.Width, 1)
			x += cellWidth
		}
		if y+1 < height {
			plain.WriteByte('\n')
		}
	}
	return plain.String()
}

func (terminal *VirtualTerminal) signalLocked() {
	close(terminal.notify)
	terminal.notify = make(chan struct{})
}

func validateVirtualTerminalDimensions(width, height int) error {
	if width < 1 || width > maximumVirtualTerminalWidth {
		return fmt.Errorf("virtual terminal width must be in [1,%d] cells", maximumVirtualTerminalWidth)
	}
	if height < 1 || height > maximumVirtualTerminalHeight {
		return fmt.Errorf("virtual terminal height must be in [1,%d] lines", maximumVirtualTerminalHeight)
	}
	return nil
}

func evaluateTerminalPredicate(predicate func(Screen) bool, screen Screen) (matched bool, err error) {
	defer func() {
		if recover() != nil {
			matched = false
			err = errors.New("virtual terminal predicate panicked")
		}
	}()
	return predicate(screen), nil
}

var _ interface {
	Write([]byte) (int, error)
	Close() error
} = (*VirtualTerminal)(nil)
