//go:build !windows && !linux && !darwin

package userstorage

type (
	stableLock      struct{}
	secureDirectory struct{}
)

func bindSecureDirectory(string) (*secureDirectory, error) { return nil, ErrUnavailable }
func acquireStableLock(string) (*stableLock, error)        { return nil, ErrUnavailable }
func (*stableLock) close() error                           { return nil }
func (*secureDirectory) acquireStableLock(string) (*stableLock, error) {
	return nil, ErrUnavailable
}

func (*secureDirectory) acquireInitializationLock(string) (*stableLock, error) {
	return nil, ErrUnavailable
}
func (*secureDirectory) readFile(string, int) ([]byte, error) { return nil, ErrUnavailable }
func (*secureDirectory) writeFileAtomic(string, []byte) error { return ErrUnavailable }
func (*secureDirectory) removeFile(string) error              { return ErrUnavailable }
func (*secureDirectory) close() error                         { return nil }
func readSecureFile(string, int) ([]byte, error)              { return nil, ErrUnavailable }
func writeSecureFileAtomic(string, []byte) error              { return ErrUnavailable }
