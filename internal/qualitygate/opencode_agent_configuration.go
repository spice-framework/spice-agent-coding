package main

type opencodeAgentConfiguration struct {
	Description string            `json:"description"`
	Mode        string            `json:"mode"`
	Steps       int               `json:"steps"`
	Temperature float64           `json:"temperature"`
	Permission  map[string]string `json:"permission"`
}
