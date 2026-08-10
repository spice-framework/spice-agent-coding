package daemoncommand

import "slices"

type Options struct {
	mode      Mode
	arguments []string
}

func (options Options) Mode() Mode { return options.mode }

func (options Options) Arguments() []string { return slices.Clone(options.arguments) }
