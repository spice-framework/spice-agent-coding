module github.com/spice-framework/spice-agent-coding

go 1.26.0

toolchain go1.26.5

tool (
	github.com/spice-framework/spice-agent-tui/cmd/spice-agent-tui-annotations
	github.com/spice-framework/spice-agent/cmd/spice-agent-annotations
	github.com/spice-framework/toolchain/cmd/spice
	github.com/spice-framework/toolchain/cmd/spice-annotation-core
)

require (
	github.com/spice-framework/spice v0.1.0-preview.1.0.20260806200749-524424a04df0
	github.com/spice-framework/spice-agent v0.0.0-20260807185918-0dad639cba64
	github.com/spice-framework/spice-agent-provider-openai v0.0.0-20260806230257-a6962fe2dabc
	github.com/spice-framework/spice-agent-tools-coding v0.0.0-20260807150540-eeacf58875c5
	github.com/spice-framework/spice-agent-tui v0.0.0-20260807044421-a0d48242cd4f
	golang.org/x/sys v0.47.0
	google.golang.org/grpc v1.83.0
)

require (
	charm.land/bubbletea/v2 v2.0.8 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260703014108-f5a850f9c2b7 // indirect
	github.com/charmbracelet/x/ansi v0.11.7 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/openai/openai-go/v3 v3.50.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/spice-framework/toolchain v0.1.0-preview.1.0.20260806203056-d0b9ac086bd6 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
