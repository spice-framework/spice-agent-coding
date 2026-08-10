//go:build windows

package daemonprocess

// RootRegistryFactory owns platform-specific root-registry adoption.
type RootRegistryFactory struct{}

// Adopt returns the inactive registry used when the supervisor Job Object owns
// descendant containment.
func (RootRegistryFactory) Adopt() (RootRegistry, error) {
	return inactiveRootRegistry{}, nil
}
