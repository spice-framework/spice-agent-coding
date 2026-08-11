package workspace

import (
	"reflect"
	"testing"
)

func TestPropertiesPreserveWorkspaceConfigurationContract(t *testing.T) {
	t.Parallel()

	field, found := reflect.TypeFor[Properties]().FieldByName("Workspace")
	if !found {
		t.Fatal("Properties.Workspace is missing")
	}
	if got, want := field.Tag.Get("spice"), "workspace,default=.,env=SPICE_AGENT_WORKSPACE"; got != want {
		t.Fatalf("workspace property tag = %q, want %q", got, want)
	}
}
