package terminalcommand

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Parser struct{}

func (Parser) Parse(arguments []string) (Options, bool, bool) {
	exact := slices.Clone(arguments)
	parser := Parser{}
	switch {
	case len(exact) == 0:
		return Options{mode: ModeManaged, arguments: exact}, false, true
	case len(exact) == 1 && exact[0] == "--check":
		return Options{mode: ModeCheck, arguments: exact}, false, true
	case len(exact) == 1 && (exact[0] == "help" || exact[0] == "--help" || exact[0] == "-h"):
		return Options{}, true, true
	case len(exact) == 3 && exact[0] == "attach" && exact[1] == "--endpoint" && parser.localOpaqueEndpoint(exact[2]):
		return Options{mode: ModeAttach, endpoint: exact[2], arguments: exact}, false, true
	default:
		return Options{}, false, false
	}
}

func (parser Parser) localOpaqueEndpoint(endpoint string) bool {
	if endpoint == "" || len(endpoint) > 4096 || !utf8.ValidString(endpoint) || strings.TrimSpace(endpoint) != endpoint {
		return false
	}
	for _, value := range endpoint {
		if unicode.IsControl(value) {
			return false
		}
	}
	lower := strings.ToLower(endpoint)
	if strings.Contains(lower, "://") {
		return false
	}
	for _, prefix := range []string{"dns:", "grpc:", "http:", "https:", "tcp:"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	if strings.HasPrefix(endpoint, "//") {
		return false
	}
	if strings.HasPrefix(endpoint, `\\`) && !strings.HasPrefix(lower, `\\.\pipe\`) {
		return false
	}
	return !parser.looksLikeHostPort(endpoint)
}

func (parser Parser) looksLikeHostPort(endpoint string) bool {
	if len(endpoint) >= 3 && endpoint[1] == ':' && parser.asciiLetter(endpoint[0]) &&
		(endpoint[2] == '\\' || endpoint[2] == '/') {
		return false
	}
	separator := strings.LastIndexByte(endpoint, ':')
	if separator < 0 {
		return false
	}
	host := endpoint[:separator]
	port := endpoint[separator+1:]
	if host == "" {
		return port != ""
	}
	if strings.ContainsAny(host, `/\`) {
		return strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")
	}
	return true
}

func (Parser) asciiLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
