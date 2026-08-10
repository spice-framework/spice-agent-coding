package daemonprocess

type redactedError struct {
	message string
	cause   error
}

func (failure redactedError) Error() string { return failure.message }
func (failure redactedError) Unwrap() error { return failure.cause }

func (redactedError) wrap(message string, cause error) error {
	if cause == nil {
		return nil
	}
	return redactedError{message: message, cause: cause}
}
