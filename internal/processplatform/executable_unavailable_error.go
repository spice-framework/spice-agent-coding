package processplatform

type executableUnavailableError string

const executableUnavailableErrorValue executableUnavailableError = "requested executable is unavailable"

func (problem executableUnavailableError) Error() string { return string(problem) }
