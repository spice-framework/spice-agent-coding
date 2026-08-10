package daemoncommand

import "slices"

type Parser struct{}

func (Parser) Parse(arguments []string) (Options, bool, bool) {
	exact := slices.Clone(arguments)
	switch {
	case len(exact) == 1 && exact[0] == "serve":
		return Options{mode: ModeServe, arguments: exact}, false, true
	case len(exact) == 1 && exact[0] == "--check":
		return Options{mode: ModeCheck, arguments: exact}, false, true
	case len(exact) == 1 && (exact[0] == "help" || exact[0] == "--help" || exact[0] == "-h"):
		return Options{}, true, true
	default:
		return Options{}, false, false
	}
}
