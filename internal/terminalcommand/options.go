package terminalcommand

import "slices"

type Options struct {
	mode      Mode
	endpoint  string
	arguments []string
}

func (options Options) Mode() Mode { return options.mode }

func (options Options) Endpoint() string { return options.endpoint }

func (options Options) Arguments() []string { return slices.Clone(options.arguments) }
