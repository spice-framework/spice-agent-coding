package terminal

type commandLane uint8

const (
	commandLaneOrdinary commandLane = iota + 1
	commandLaneCancel
)
