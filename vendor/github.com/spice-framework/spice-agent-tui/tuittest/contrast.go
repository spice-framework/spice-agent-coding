package tuittest

import (
	"errors"
	"fmt"
	"math"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

// MinimumTextContrast is the WCAG 2.2 AA ratio for ordinary text.
const MinimumTextContrast = 4.5

// ContrastCheck records one semantic palette role against its reference
// background. It is evidence for shipped palettes, not a claim about a user's
// terminal background overrides.
type ContrastCheck struct {
	role       string
	foreground agenttui.Color
	background agenttui.Color
	ratio      float64
}

// Role returns the semantic palette role.
func (check ContrastCheck) Role() string { return check.role }

// Foreground returns the checked text color.
func (check ContrastCheck) Foreground() agenttui.Color { return check.foreground }

// Background returns the documented reference background.
func (check ContrastCheck) Background() agenttui.Color { return check.background }

// Ratio returns the WCAG relative-luminance contrast ratio.
func (check ContrastCheck) Ratio() float64 { return check.ratio }

// ContrastReport is one immutable theme audit.
type ContrastReport struct {
	themeName string
	mode      agenttui.ThemeMode
	checks    []ContrastCheck
}

// ThemeName returns the audited immutable theme name.
func (report ContrastReport) ThemeName() string { return report.themeName }

// Mode returns the audited light or dark mode.
func (report ContrastReport) Mode() agenttui.ThemeMode { return report.mode }

// Checks returns a defensive copy in stable semantic-role order.
func (report ContrastReport) Checks() []ContrastCheck {
	return append([]ContrastCheck(nil), report.checks...)
}

// Validate requires every semantic role to meet minimum.
func (report ContrastReport) Validate(minimum float64) error {
	if math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < 1 || minimum > 21 {
		return errors.New("contrast minimum must be finite and within [1,21]")
	}
	if report.themeName == "" || len(report.checks) == 0 {
		return errors.New("contrast report is empty")
	}
	for _, check := range report.checks {
		if check.ratio < minimum {
			return fmt.Errorf(
				"theme %q %s contrast %.2f:1 is below %.2f:1",
				report.themeName, check.role, check.ratio, minimum,
			)
		}
	}
	return nil
}

// AuditThemeContrast snapshots a theme and computes all shipped semantic text
// roles against the documented light/dark reference background.
func AuditThemeContrast(theme agenttui.Theme) (ContrastReport, error) {
	if theme == nil {
		return ContrastReport{}, errors.New("contrast theme must not be nil")
	}
	snapshot, err := agenttui.NewTheme(theme.Name(), theme.Mode(), theme.Palette())
	if err != nil {
		return ContrastReport{}, fmt.Errorf("contrast theme: %w", err)
	}
	background, err := referenceBackground(snapshot.Mode())
	if err != nil {
		return ContrastReport{}, err
	}
	palette := snapshot.Palette()
	roles := []struct {
		name  string
		color agenttui.Color
	}{
		{name: "foreground", color: palette.Foreground()},
		{name: "muted", color: palette.Muted()},
		{name: "accent", color: palette.Accent()},
		{name: "warning", color: palette.Warning()},
		{name: "failure", color: palette.Failure()},
	}
	checks := make([]ContrastCheck, 0, len(roles))
	for _, role := range roles {
		checks = append(checks, ContrastCheck{
			role: role.name, foreground: role.color, background: background,
			ratio: ContrastRatio(role.color, background),
		})
	}
	return ContrastReport{themeName: snapshot.Name(), mode: snapshot.Mode(), checks: checks}, nil
}

// ContrastRatio computes the WCAG relative-luminance ratio in [1,21].
func ContrastRatio(first, second agenttui.Color) float64 {
	firstLuminance := relativeLuminance(first)
	secondLuminance := relativeLuminance(second)
	brighter, darker := firstLuminance, secondLuminance
	if darker > brighter {
		brighter, darker = darker, brighter
	}
	return (brighter + 0.05) / (darker + 0.05)
}

func referenceBackground(mode agenttui.ThemeMode) (agenttui.Color, error) {
	switch mode {
	case agenttui.ThemeLight:
		return agenttui.NewColor(255, 255, 255), nil
	case agenttui.ThemeDark:
		return agenttui.NewColor(2, 6, 23), nil
	default:
		return agenttui.Color{}, fmt.Errorf("unsupported contrast theme mode %q", mode)
	}
}

func relativeLuminance(color agenttui.Color) float64 {
	red, green, blue := color.RGB()
	return 0.2126*linearChannel(red) + 0.7152*linearChannel(green) + 0.0722*linearChannel(blue)
}

func linearChannel(value uint8) float64 {
	channel := float64(value) / 255
	if channel <= 0.04045 {
		return channel / 12.92
	}
	return math.Pow((channel+0.055)/1.055, 2.4)
}
