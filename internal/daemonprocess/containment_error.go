package daemonprocess

// ContainmentError reports that the daemon process boundary could not prove
// complete cleanup. It is terminal and not retryable.
type ContainmentError struct{ cause error }

func (*ContainmentError) Error() string   { return "release managed daemon process isolation" }
func (*ContainmentError) Retryable() bool { return false }
func (failure *ContainmentError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (*ContainmentError) wrap(cause error) error {
	if cause == nil {
		return nil
	}
	return &ContainmentError{cause: cause}
}
