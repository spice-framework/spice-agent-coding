package releaseinstallation

// Expectation identifies the immutable release whose independently verified
// subjects are allowed into the installed-byte gate.
type Expectation struct {
	Repository string
	Module     string
	Version    string
}
