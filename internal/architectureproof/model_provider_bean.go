package architectureproof

// @import { ModelProvider } from "github.com/spice-framework/spice-agent/annotation/agent"
// @import { Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"time"

	openaiprovider "github.com/spice-framework/spice-agent-provider-openai"
	"github.com/spice-framework/spice-agent/model"
)

// NewModelProvider replaces the starter's fallback client with a real adapter
// configured against the application-owned deterministic endpoint.
//
// @ModelProvider(name="architecture-proof-openai")
// @Singleton
func NewModelProvider(fixture *ResponsesFixture) (model.Provider, error) {
	return openaiprovider.New(
		openaiprovider.Config{
			APIKey:     fixtureSecret,
			BaseURL:    fixture.server.URL + "/v1",
			Timeout:    5 * time.Second,
			MaxRetries: 0,
		},
		openaiprovider.WithHTTPClient(fixture.server.Client()),
	)
}
