package main

type opencodeModelConfiguration struct {
	Limit   opencodeModelLimit   `json:"limit"`
	Options opencodeModelOptions `json:"options"`
}

type opencodeModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type opencodeModelOptions struct {
	Provider opencodeProviderRouting `json:"provider"`
}

type opencodeProviderRouting struct {
	AllowFallbacks bool `json:"allow_fallbacks"`
}
