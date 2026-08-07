package architectureproof

import (
	"context"
	"testing"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

func TestArchitectureProofOwnsExplicitProcessAdapters(t *testing.T) {
	t.Parallel()
	if resolver := NewExecutableResolver(); resolver == nil {
		t.Fatal("executable resolver is nil")
	}
	launcher, err := NewProcessLauncher()
	if err != nil || launcher == nil {
		t.Fatalf("NewProcessLauncher() = %v, %v", launcher, err)
	}
	if _, err = launcher.Start(context.Background(), agentprocess.Spec{}); err == nil {
		t.Fatal("launcher accepted an invalid specification")
	}
}
