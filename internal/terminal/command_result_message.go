package terminal

type commandResultMessage struct {
	lane commandLane
	err  error
}
