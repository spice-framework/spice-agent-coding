package daemon

import "time"

// @import { ConfigurationProperties } from "github.com/spice-framework/spice/annotation/core"

// Properties is the complete typed daemon configuration surface. Secrets are
// redacted by Spice configuration metadata and never copied into diagnostics.
//
// @ConfigurationProperties(prefix="agent")
type Properties struct {
	APIKey                string        `spice:"openai.api-key,required,secret,env=OPENAI_API_KEY"`
	BaseURL               string        `spice:"openai.base-url,default=https://api.openai.com/v1,env=OPENAI_BASE_URL"`
	Organization          string        `spice:"openai.organization,env=OPENAI_ORGANIZATION"`
	Project               string        `spice:"openai.project,env=OPENAI_PROJECT"`
	ProviderTimeout       time.Duration `spice:"openai.timeout,default=2m,env=OPENAI_TIMEOUT"`
	ProviderRetries       int           `spice:"openai.max-retries,default=0,env=OPENAI_MAX_RETRIES"`
	Model                 string        `spice:"model,required,env=OPENAI_MODEL"`
	Workspace             string        `spice:"workspace,default=.,env=SPICE_AGENT_WORKSPACE"`
	RunAuthorityDirectory string        `spice:"run-authority-directory,env=SPICE_AGENT_RUN_AUTHORITY_DIRECTORY"`
}
