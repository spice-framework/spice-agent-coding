package releaseinstallation

type candidateMetadata struct {
	Schema     int    `json:"schema"`
	Profile    string `json:"profile"`
	Repository string `json:"repository"`
	Module     string `json:"module"`
	Version    string `json:"version"`
}
