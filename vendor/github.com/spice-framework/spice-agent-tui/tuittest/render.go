package tuittest

import (
	"fmt"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent-tui/terminal"
)

// RenderOptions configures a pure renderer capture without a Driver.
type RenderOptions struct {
	Width    int
	Height   int
	Theme    agenttui.Theme
	Renderer agenttui.Renderer
	Name     string
}

// RenderScreen renders ViewData through the production FixedRenderer path.
// Use this for pure pixel-perfect chrome tests that do not need keyboard state.
func RenderScreen(data agenttui.ViewData, options RenderOptions) (Screen, error) {
	if err := data.Validate(); err != nil {
		return Screen{}, fmt.Errorf("view data: %w", err)
	}
	if options.Width <= 0 {
		options.Width = 48
	}
	if options.Height <= 0 {
		options.Height = 12
	}
	if options.Theme == nil {
		options.Theme = agenttui.DarkTheme()
	}
	if options.Renderer == nil {
		options.Renderer = terminal.NewFixedRenderer()
	}
	size := agenttui.BoundedSize(options.Width, options.Height)
	frame, err := options.Renderer.Render(data, size, options.Theme)
	if err != nil {
		return Screen{}, fmt.Errorf("render: %w", err)
	}
	return FromFrame(frame, WithName(options.Name))
}
