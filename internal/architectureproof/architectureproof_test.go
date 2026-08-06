package architectureproof_test

import (
	"context"
	"slices"
	"testing"
	"time"

	architectureproof "github.com/spice-framework/spice-agent-coding/internal/architectureproof"
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
	if report.Requests != 2 || !report.Authorized || !report.Continuation || report.SecretSeen {
		t.Fatalf("provider facts = requests=%d authorized=%v continuation=%v", report.Requests, report.Authorized, report.Continuation)
	}
	if !slices.Equal(report.Tools, []string{"read", "replace", "shell"}) {
		t.Fatalf("generated named tools = %v", report.Tools)
	}
	metadata := architectureproof.NewExecutionPlanMetadata()
	if !slices.Equal(report.CompiledPlanIdentities, metadata.CompiledPlanIdentities) ||
		report.SnapshotCompatibilityIdentity != metadata.SnapshotCompatibilityIdentity ||
		report.ToolPlanID == "" || report.PlanFingerprint == "" {
		t.Fatalf(
			"generated plan = compiled %v, compatibility %q, tool plan %q, fingerprint %q",
			report.CompiledPlanIdentities,
			report.SnapshotCompatibilityIdentity,
			report.ToolPlanID,
			report.PlanFingerprint,
		)
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

func TestGeneratedArchitectureProofPropagatesProviderCancellation(t *testing.T) {
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
	report, err := application.Components().Proof.RunCancellation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.SecretSeen {
		t.Fatal("cancellation events exposed the fixture credential")
	}
	if countKind(report.Kinds, event.RunCancelled) != 1 ||
		countKind(report.Kinds, event.ModelFailed) != 1 ||
		countKind(report.Kinds, event.TurnFailed) != 1 {
		t.Fatalf("cancellation terminal events = %v", report.Kinds)
	}
}

func countKind(values []event.Kind, expected event.Kind) int {
	count := 0
	for _, value := range values {
		if value == expected {
			count++
		}
	}
	return count
}
