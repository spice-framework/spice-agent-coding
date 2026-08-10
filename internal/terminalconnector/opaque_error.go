package terminalconnector

type opaqueError struct {
	message string
	cause   error
}

func (failure *opaqueError) Error() string { return failure.message }

func (failure *opaqueError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}
