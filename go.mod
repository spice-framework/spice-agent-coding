module github.com/spice-framework/spice-agent-coding

go 1.26.0

toolchain go1.26.5

tool (
	github.com/spice-framework/spice-agent/cmd/spice-agent-annotations
	github.com/spice-framework/toolchain/cmd/spice
	github.com/spice-framework/toolchain/cmd/spice-annotation-core
)

require (
	github.com/spice-framework/spice v0.1.0-preview.1.0.20260806200749-524424a04df0
	github.com/spice-framework/spice-agent v0.0.0-20260806204214-1f072842707a
	github.com/spice-framework/spice-agent-provider-openai v0.0.0-20260806210759-4beed3838dd2
	github.com/spice-framework/spice-agent-tools-coding v0.0.0-20260806210553-17cbef3b93fa
)

require (
	github.com/openai/openai-go/v3 v3.50.0 // indirect
	github.com/spice-framework/toolchain v0.1.0-preview.1.0.20260806203056-d0b9ac086bd6 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
)
