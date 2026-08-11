package releaseinstallation

type releaseIdentity struct {
	VersionSymbol string `json:"version_symbol"`
	VersionValue  string `json:"version_value"`
	CommitSymbol  string `json:"commit_symbol"`
	CommitValue   string `json:"commit_value"`
}
