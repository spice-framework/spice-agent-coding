package main

type opencodeProviderConfiguration struct {
	Whitelist []string                              `json:"whitelist"`
	Models    map[string]opencodeModelConfiguration `json:"models"`
}
