package processplatform

type terminalContainmentError struct{ cause error }

func (*terminalContainmentError) Error() string   { return "platform process containment cleanup failed" }
func (*terminalContainmentError) Retryable() bool { return false }
func (failure *terminalContainmentError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}
