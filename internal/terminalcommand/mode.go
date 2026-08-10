package terminalcommand

type Mode uint8

const (
	ModeManaged Mode = iota + 1
	ModeAttach
	ModeCheck
)
