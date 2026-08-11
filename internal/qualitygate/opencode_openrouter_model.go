package main

type opencodeOpenRouterModel struct {
	ID                  string                    `json:"id"`
	Pricing             opencodeOpenRouterPricing `json:"pricing"`
	SupportedParameters []string                  `json:"supported_parameters"`
}
