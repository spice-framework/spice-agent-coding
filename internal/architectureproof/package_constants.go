package architectureproof

const (
	agentModuleSelection    = "v0.1.0-preview.4"
	providerModuleSelection = "v0.1.0-preview.1"
	toolsModuleSelection    = "v0.1.0-preview.1"

	// snapshotCompatibilityIdentity is application-owned semantic compatibility,
	// not a hash of machine paths or timestamps. Changing executable snapshot
	// semantics requires a new value.
	snapshotCompatibilityIdentity = "github.com/spice-framework/spice-agent-coding/architectureproof:v1"
	fixtureSecret                 = "architecture-proof-secret"
	fixtureDocument               = "Spice Agent architecture proof\n"
)
