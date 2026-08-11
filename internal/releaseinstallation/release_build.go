package releaseinstallation

type releaseBuild struct {
	ModuleMode     string          `json:"module_mode"`
	CGOEnabled     bool            `json:"cgo_enabled"`
	Trimpath       bool            `json:"trimpath"`
	BuildVCS       bool            `json:"build_vcs"`
	BuildID        string          `json:"build_id"`
	Environment    string          `json:"environment"`
	CacheIsolation bool            `json:"cache_isolation"`
	Source         string          `json:"source"`
	GOAMD64        string          `json:"goamd64"`
	GOARM64        string          `json:"goarm64"`
	Identity       releaseIdentity `json:"identity"`
}
