package tuittest

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
	agenttui "github.com/spice-framework/spice-agent-tui"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	// EmbeddedFont identifies the repository-pinned human-artifact font.
	EmbeddedFont    = "Go Mono from golang.org/x/image v0.39.0"
	pngCellWidth    = 9
	pngCellHeight   = 18
	pngPadding      = 8
	pngFontPoints   = 14
	maximumPNGScale = 2
)

// PNGOptions configure deterministic human-review rendering. The PNG never
// replaces Screen, trace, golden, or VT evidence as an acceptance authority.
type PNGOptions struct {
	ThemeMode agenttui.ThemeMode
	Scale     int
}

// RenderPNG renders one Screen with the embedded pinned Go Mono font. It adds
// no metadata or platform font discovery, so identical inputs produce
// byte-identical PNG files across supported operating systems.
func RenderPNG(screen Screen, options PNGOptions) ([]byte, error) {
	options = normalizePNGOptions(options)
	if err := validatePNGInput(screen, options); err != nil {
		return nil, err
	}
	theme := agenttui.DarkTheme()
	if options.ThemeMode == agenttui.ThemeLight {
		theme = agenttui.LightTheme()
	}
	background, err := referenceBackground(options.ThemeMode)
	if err != nil {
		return nil, err
	}
	parsed, err := opentype.Parse(gomono.TTF)
	if err != nil {
		return nil, fmt.Errorf("parse embedded font: %w", err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{
		Size: pngFontPoints * float64(options.Scale), DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("construct embedded font face: %w", err)
	}
	defer face.Close() //nolint:errcheck // In-memory font face close cannot affect encoded output.

	canvas := newPNGCanvas(screen, options.Scale, background)
	renderPNGLines(canvas, screen, theme.Palette(), face, options.Scale)
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&encoded, canvas); err != nil {
		return nil, fmt.Errorf("encode deterministic PNG: %w", err)
	}
	return encoded.Bytes(), nil
}

func normalizePNGOptions(options PNGOptions) PNGOptions {
	if options.ThemeMode == "" {
		options.ThemeMode = agenttui.ThemeDark
	}
	if options.Scale == 0 {
		options.Scale = 1
	}
	return options
}

func validatePNGInput(screen Screen, options PNGOptions) error {
	if screen.width < 1 || screen.width > agenttui.MaximumWidth ||
		screen.height < 1 || screen.height > agenttui.MaximumHeight {
		return fmt.Errorf("PNG screen size must be within 1x1 and %dx%d", agenttui.MaximumWidth, agenttui.MaximumHeight)
	}
	lineCountValid := len(screen.plainLines) == screen.height
	if screen.accessible {
		lineCountValid = len(screen.plainLines) > 0 && len(screen.plainLines) <= screen.height
	}
	if !lineCountValid {
		return fmt.Errorf("PNG screen has %d lines for height %d", len(screen.plainLines), screen.height)
	}
	if options.ThemeMode != agenttui.ThemeDark && options.ThemeMode != agenttui.ThemeLight {
		return fmt.Errorf("unsupported PNG theme mode %q", options.ThemeMode)
	}
	if options.Scale < 1 || options.Scale > maximumPNGScale {
		return fmt.Errorf("PNG scale must be within [1,%d]", maximumPNGScale)
	}
	return nil
}

func newPNGCanvas(screen Screen, scale int, background agenttui.Color) *image.NRGBA {
	width := (2*pngPadding + screen.width*pngCellWidth) * scale
	height := (2*pngPadding + screen.height*pngCellHeight) * scale
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(nrgba(background)), image.Point{}, draw.Src)
	return canvas
}

func renderPNGLines(
	canvas *image.NRGBA,
	screen Screen,
	palette agenttui.Palette,
	face font.Face,
	scale int,
) {
	for row, line := range screen.plainLines {
		textColor := pngLineColor(screen, palette, row, line)
		drawer := font.Drawer{Dst: canvas, Src: image.NewUniform(nrgba(textColor)), Face: face}
		cell := 0
		graphemes := uniseg.NewGraphemes(line)
		for graphemes.Next() && cell < screen.width {
			cluster := graphemes.Str()
			cellWidth := max(CellWidth(cluster), 1)
			x := (pngPadding + cell*pngCellWidth) * scale
			y := (pngPadding + row*pngCellHeight) * scale
			if cluster != " " {
				renderPNGCluster(canvas, &drawer, face, cluster, x, y, cellWidth, scale, textColor)
			}
			cell += cellWidth
		}
	}
	if screen.cursorVisible {
		drawPNGCursor(canvas, screen, palette.Accent(), scale)
	}
}

func renderPNGCluster(
	canvas *image.NRGBA,
	drawer *font.Drawer,
	face font.Face,
	cluster string,
	x, y, cellWidth, scale int,
	textColor agenttui.Color,
) {
	if faceContainsCluster(face, cluster) {
		drawer.Dot = fixed.P(x, y+(pngFontPoints+1)*scale)
		drawer.DrawString(cluster)
		return
	}
	drawMissingCluster(canvas, cluster, x, y, cellWidth, scale, textColor)
}

func faceContainsCluster(face font.Face, cluster string) bool {
	for _, character := range cluster {
		if unicode.Is(unicode.Mn, character) || unicode.Is(unicode.Me, character) ||
			character == '\u200d' || character == '\ufe0f' {
			continue
		}
		if _, exists := face.GlyphAdvance(character); !exists {
			return false
		}
	}
	return true
}

func drawMissingCluster(
	canvas *image.NRGBA,
	cluster string,
	x, y, cellWidth, scale int,
	textColor agenttui.Color,
) {
	foreground := nrgba(textColor)
	width := max(cellWidth*pngCellWidth*scale-2*scale, 3*scale)
	height := (pngCellHeight - 4) * scale
	rectangle := image.Rect(x+scale, y+2*scale, x+scale+width, y+2*scale+height)
	drawOutline(canvas, rectangle, foreground, scale)
	digest := sha256.Sum256([]byte(cluster))
	for index := range 16 {
		if digest[index/8]&(1<<uint(index%8)) == 0 {
			continue
		}
		pixelX := rectangle.Min.X + (1+index%4)*scale
		pixelY := rectangle.Min.Y + (1+index/4)*scale
		draw.Draw(canvas, image.Rect(pixelX, pixelY, pixelX+scale, pixelY+scale), image.NewUniform(foreground), image.Point{}, draw.Src)
	}
}

func drawOutline(canvas *image.NRGBA, rectangle image.Rectangle, foreground color.NRGBA, thickness int) {
	draw.Draw(canvas, image.Rect(rectangle.Min.X, rectangle.Min.Y, rectangle.Max.X, rectangle.Min.Y+thickness), image.NewUniform(foreground), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(rectangle.Min.X, rectangle.Max.Y-thickness, rectangle.Max.X, rectangle.Max.Y), image.NewUniform(foreground), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(rectangle.Min.X, rectangle.Min.Y, rectangle.Min.X+thickness, rectangle.Max.Y), image.NewUniform(foreground), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(rectangle.Max.X-thickness, rectangle.Min.Y, rectangle.Max.X, rectangle.Max.Y), image.NewUniform(foreground), image.Point{}, draw.Src)
}

func drawPNGCursor(canvas *image.NRGBA, screen Screen, cursorColor agenttui.Color, scale int) {
	x := (pngPadding + screen.cursorX*pngCellWidth) * scale
	y := (pngPadding + (screen.cursorY+1)*pngCellHeight) * scale
	cursor := image.Rect(x, y-2*scale, x+pngCellWidth*scale, y)
	draw.Draw(canvas, cursor, image.NewUniform(nrgba(cursorColor)), image.Point{}, draw.Src)
}

func pngLineColor(screen Screen, palette agenttui.Palette, row int, line string) agenttui.Color {
	if row == 0 || strings.HasPrefix(line, "> ") || strings.HasPrefix(line, "Prompt: ") {
		return palette.Accent()
	}
	if strings.HasPrefix(line, "Activity") {
		return palette.Muted()
	}
	if strings.Contains(line, "["+strings.ToUpper(screen.statusLevel)+"]") {
		switch agenttui.StatusLevel(screen.statusLevel) {
		case agenttui.StatusBusy, agenttui.StatusReconnecting:
			return palette.Accent()
		case agenttui.StatusDisconnected, agenttui.StatusWarning:
			return palette.Warning()
		case agenttui.StatusError:
			return palette.Failure()
		}
	}
	return palette.Foreground()
}

func nrgba(value agenttui.Color) color.NRGBA {
	red, green, blue := value.RGB()
	return color.NRGBA{R: red, G: green, B: blue, A: 255}
}
