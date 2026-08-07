package daemon

import (
	"context"
	"testing"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

func TestProcessAdaptersAreExplicitGeneratedBeans(t *testing.T) {
	t.Parallel()
	resolver := NewExecutableResolver()
	if resolver == nil {
		t.Fatal("executable resolver is nil")
	}
	if _, err := resolver.Resolve(context.Background(), agentprocess.Lookup{}); err == nil {
		t.Fatal("resolver accepted an invalid lookup")
	}
	launcher, err := NewProcessLauncher(&rootRegistryFixture{})
	if err != nil || launcher == nil {
		t.Fatalf("NewProcessLauncher() = %v, %v", launcher, err)
	}
	if _, err = NewProcessLauncher(nil); err == nil {
		t.Fatal("NewProcessLauncher() accepted nil containment registry")
	}
}
