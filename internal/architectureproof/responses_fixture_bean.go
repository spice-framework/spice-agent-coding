package architectureproof

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/spice-framework/spice/lifecycle"
)

// NewResponsesFixture creates a current-process TLS endpoint. No external
// network or credential is used by the architecture proof.
//
// @Bean(name="responsesFixture")
func NewResponsesFixture() (*ResponsesFixture, lifecycle.Cleanup, error) {
	fixture := &ResponsesFixture{}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	return fixture, func(context.Context) error {
		fixture.server.Close()
		return nil
	}, nil
}
