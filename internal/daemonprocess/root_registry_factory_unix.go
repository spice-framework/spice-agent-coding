//go:build linux || darwin

package daemonprocess

// RootRegistryFactory owns platform-specific root-registry adoption.
type RootRegistryFactory struct{}

// Adopt returns the daemon-owned explicit descendant registry when the managed
// supervisor endpoint is present.
func (RootRegistryFactory) Adopt() (RootRegistry, error) {
	return (&DescendantRegistry{}).adoptRoot()
}
