package main

type opencodeConfigurationValue struct {
	Autoupdate       bool                                     `json:"autoupdate"`
	Share            string                                   `json:"share"`
	Snapshot         bool                                     `json:"snapshot"`
	EnabledProviders []string                                 `json:"enabled_providers"`
	Provider         map[string]opencodeProviderConfiguration `json:"provider"`
	MCP              map[string]any                           `json:"mcp"`
	Plugin           []string                                 `json:"plugin"`
	SubagentDepth    int                                      `json:"subagent_depth"`
	Permission       map[string]string                        `json:"permission"`
	Agent            map[string]opencodeAgentConfiguration    `json:"agent"`
}
