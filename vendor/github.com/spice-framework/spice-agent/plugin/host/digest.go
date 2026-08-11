package pluginhost

import (
	"context"
	"errors"

	"github.com/spice-framework/spice-agent/process"
)

// SHA256 is the generic process executable-content identity retained here as a
// source-compatible plugin configuration name.
type SHA256 = process.SHA256

// ParseSHA256 preserves the plugin configuration error contract while using
// the generic process digest parser.
func ParseSHA256(value string) (SHA256, error) {
	digest, err := process.ParseSHA256(value)
	if err != nil {
		return SHA256{}, configFailure("sha256", -1, ProblemMalformed)
	}
	return digest, nil
}

func openVerifiedExecutable(
	ctx context.Context,
	executable Executable,
) (*process.ExecutableLease, error) {
	if err := executable.Validate(); err != nil {
		return nil, err
	}
	lease, err := process.VerifyExecutable(ctx, executable.Path(), executable.SHA256())
	if err != nil {
		return nil, pluginVerificationFailure(err)
	}
	return lease, nil
}

func recheckVerifiedExecutable(ctx context.Context, lease *process.ExecutableLease) error {
	if err := lease.Recheck(ctx); err != nil {
		return pluginVerificationFailure(err)
	}
	return nil
}

func closeVerifiedExecutable(lease *process.ExecutableLease) error {
	if err := lease.Close(); err != nil {
		return pluginVerificationFailure(err)
	}
	return nil
}

func pluginVerificationFailure(cause error) error {
	var failure *process.VerificationError
	if !errors.As(cause, &failure) {
		return verificationFailure(verificationInspect, cause)
	}
	operation := verificationInspect
	switch failure.Operation() {
	case process.VerificationOperationValidate, process.VerificationOperationOpen:
		operation = verificationOpen
	case process.VerificationOperationHash:
		operation = verificationHash
	case process.VerificationOperationRecheck:
		operation = verificationRecheck
	case process.VerificationOperationClose:
		operation = verificationClose
	case process.VerificationOperationInspect, process.VerificationOperationDuplicate:
		operation = verificationInspect
	}
	return verificationFailure(operation, cause)
}
