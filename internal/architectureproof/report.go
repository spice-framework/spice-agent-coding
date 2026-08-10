package architectureproof

import "github.com/spice-framework/spice-agent/event"

// Report is inspectable evidence from one architecture-proof run.
type Report struct {
	Kinds                         []event.Kind
	FinalText                     string
	Tools                         []string
	CompiledPlanIdentities        []string
	SnapshotCompatibilityIdentity string
	ToolPlanID                    string
	PlanFingerprint               string
	Requests                      int
	Authorized                    bool
	Continuation                  bool
	SecretSeen                    bool
}
