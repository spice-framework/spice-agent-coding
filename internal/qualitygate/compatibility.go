package main

type compatibility struct {
	Schema                   int     `json:"schema"`
	Go                       string  `json:"go"`
	Spice                    *string `json:"spice"`
	SpiceToolchain           *string `json:"spice_toolchain"`
	SpiceAgent               *string `json:"spice_agent"`
	SpiceAgentTUI            *string `json:"spice_agent_tui"`
	SpiceAgentProviderOpenAI *string `json:"spice_agent_provider_openai"`
	SpiceAgentToolsCoding    *string `json:"spice_agent_tools_coding"`
}
