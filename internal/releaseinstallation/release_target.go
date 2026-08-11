package releaseinstallation

type releaseTarget struct {
	GOOS     string   `json:"goos"`
	GOARCH   string   `json:"goarch"`
	Archive  string   `json:"archive"`
	Binaries []string `json:"binaries"`
}
