package architectureproof_test

import (
	"context"
	"slices"
	"testing"
	"time"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/architectureproof"
	"github.com/spice-framework/spice-agent/event"
)

func TestGeneratedArchitectureProofExecutesProviderToolContinuation(t *testing.T) {
	application, err := spicegen.NewApplication(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopContext, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelStop()
		if stopErr := application.Stop(stopContext); stopErr != nil {
			t.Error(stopErr)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	report, err := application.Components().Proof.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.FinalText != "architecture proof complete" {
		t.Fatalf("final text = %q", report.FinalText)
	}
	if report.Requests != 2 || !report.Authorized || !report.Continuation {
		t.Fatalf("provider facts = requests=%d authorized=%v continuation=%v", report.Requests, report.Authorized, report.Continuation)
	}
	if !slices.Equal(report.Tools, []string{"read", "replace", "shell"}) {
		t.Fatalf("generated named tools = %v", report.Tools)
	}
	for _, kind := range []event.Kind{
		event.RunStarted,
		event.ModelStarted,
		event.ToolStarted,
		event.ToolCompleted,
		event.ModelDelta,
		event.RunCompleted,
	} {
		if !slices.Contains(report.Kinds, kind) {
			t.Fatalf("events %v do not contain %s", report.Kinds, kind)
		}
	}
}
