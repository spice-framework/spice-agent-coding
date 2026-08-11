package releaseinstallation

type releaseMetadata struct {
	Schema          int             `json:"schema"`
	Profile         string          `json:"profile"`
	Repository      string          `json:"repository"`
	Module          string          `json:"module"`
	Source          string          `json:"source"`
	Version         string          `json:"version"`
	Commit          string          `json:"commit"`
	SourceDateEpoch int64           `json:"source_date_epoch"`
	Go              string          `json:"go"`
	Toolchain       string          `json:"toolchain"`
	Build           releaseBuild    `json:"build"`
	Targets         []releaseTarget `json:"targets"`
	Payloads        []releaseFile   `json:"payloads"`
	Artifacts       []releaseFile   `json:"artifacts"`
}
