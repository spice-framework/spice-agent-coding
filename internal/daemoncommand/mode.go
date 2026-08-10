package daemoncommand

type Mode uint8

const (
	ModeServe Mode = iota + 1
	ModeCheck
)
