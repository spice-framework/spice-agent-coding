package pluginhost

import (
	"encoding/json"
	"fmt"
	"io"
)

// ConfigProblem classifies one secret-safe executable configuration failure.
type ConfigProblem string

const (
	ProblemRequired     ConfigProblem = "required"
	ProblemMalformed    ConfigProblem = "malformed"
	ProblemNotAbsolute  ConfigProblem = "not_absolute"
	ProblemNotCanonical ConfigProblem = "not_canonical"
	ProblemInvalidUTF8  ConfigProblem = "invalid_utf8"
	ProblemContainsNUL  ConfigProblem = "contains_nul"
	ProblemDuplicate    ConfigProblem = "duplicate"
	ProblemTooMany      ConfigProblem = "too_many"
	ProblemTooLarge     ConfigProblem = "too_large"
	ProblemOutOfRange   ConfigProblem = "out_of_range"
)

// ConfigError identifies a field and optional list element without retaining
// or formatting the rejected value. Index returns -1 for scalar fields.
type ConfigError struct {
	field   string
	index   int
	problem ConfigProblem
}

func (failure *ConfigError) Error() string {
	if failure == nil {
		return "invalid plugin executable configuration"
	}
	if failure.index >= 0 {
		return fmt.Sprintf(
			"invalid plugin executable configuration: %s[%d] is %s",
			failure.field,
			failure.index,
			failure.problem,
		)
	}
	return fmt.Sprintf(
		"invalid plugin executable configuration: %s is %s",
		failure.field,
		failure.problem,
	)
}

func (failure *ConfigError) Field() string {
	if failure == nil {
		return ""
	}
	return failure.field
}

func (failure *ConfigError) Index() int {
	if failure == nil {
		return -1
	}
	return failure.index
}

func (failure *ConfigError) Problem() ConfigProblem {
	if failure == nil {
		return ""
	}
	return failure.problem
}

func (failure *ConfigError) MarshalJSON() ([]byte, error) {
	return json.Marshal(failure.Error())
}

func configFailure(field string, index int, problem ConfigProblem) error {
	return &ConfigError{field: field, index: index, problem: problem}
}

type verificationOperation string

const (
	verificationOpen    verificationOperation = "open"
	verificationInspect verificationOperation = "inspect"
	verificationHash    verificationOperation = "hash"
	verificationRecheck verificationOperation = "recheck"
	verificationClose   verificationOperation = "close"
)

// verificationError deliberately preserves its cause only for programmatic
// cancellation and platform classification. Its text never includes a path,
// digest, environment entry, or operating-system error string.
type verificationError struct {
	operation verificationOperation
	cause     error
}

func (failure *verificationError) Error() string {
	if failure == nil || failure.operation == "" {
		return "plugin executable verification failed"
	}
	return "plugin executable verification failed: " + string(failure.operation)
}

func (failure *verificationError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func (failure *verificationError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, failure.Error())
}

func verificationFailure(operation verificationOperation, cause error) error {
	return &verificationError{operation: operation, cause: cause}
}
