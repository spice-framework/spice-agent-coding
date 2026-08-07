package processplatform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

func TestAdapterBoundaryFailuresAndFormatting(t *testing.T) {
	t.Parallel()

	resolver := NewResolver()
	if _, err := resolver.Resolve(nil, agentprocess.Lookup{}); err == nil { //nolint:staticcheck // Boundary deliberately verifies nil-context rejection.
		t.Fatal("nil resolver context succeeded")
	}
	if _, err := resolver.Resolve(t.Context(), agentprocess.Lookup{}); err == nil {
		t.Fatal("zero lookup resolved")
	}

	var launcher *Launcher
	if owned, err := launcher.Start(t.Context(), agentprocess.Spec{}); err == nil || owned != nil {
		t.Fatalf("nil launcher = %T, %v", owned, err)
	}
	launcher = &Launcher{registrar: noopRegistrar{}, start: func(
		context.Context, agentprocess.Spec, ChildRegistrar,
	) (agentprocess.Process, error) {
		t.Fatal("invalid specification reached platform adapter")
		return nil, errors.New("unreachable")
	}}
	if owned, err := launcher.Start(t.Context(), agentprocess.Spec{}); err == nil || owned != nil {
		t.Fatalf("zero specification launch = %T, %v", owned, err)
	}

	cause := errors.New("private terminal containment cause")
	failure := terminalContainmentFailure(cause)
	var classified interface{ Retryable() bool }
	if !errors.As(failure, &classified) || classified.Retryable() || !errors.Is(failure, cause) ||
		strings.Contains(failure.Error(), "private") {
		t.Fatalf("terminal containment classification = %T, %v", failure, failure)
	}
	var nilContainment *terminalContainmentError
	if nilContainment.Unwrap() != nil || terminalContainmentFailure(nil) != nil {
		t.Fatal("nil containment failure was not nil-safe")
	}

	constructed, err := NewLauncher(noopRegistrar{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{
		fmt.Sprint(constructed), fmt.Sprintf("%#v", constructed), fmt.Sprintf("%+v", constructed),
		constructed.LogValue().String(), fmt.Sprint(resolver), fmt.Sprintf("%#v", resolver),
	} {
		if !strings.Contains(rendered, "[REDACTED]") {
			t.Fatalf("adapter formatting = %q", rendered)
		}
	}
}
