package main

import "time"

const (
	openCodeVersion                        = "1.18.16"
	maximumOpenCodePackageBytes            = 128 << 20
	maximumOpenCodeExecutableBytes         = 256 << 20
	maximumOpenCodeRepositoryBytes         = 512 << 20
	maximumOpenCodeRepositoryFileBytes     = 32 << 20
	maximumOpenCodeInventoryBytes          = 4 << 20
	maximumOpenCodeCatalogBytes            = 1 << 20
	maximumOpenCodeEventBytes              = 256 << 10
	maximumOpenCodeDiagnosticBytes         = 32 << 10
	maximumOpenCodePromptBytes             = 2048
	maximumOpenCodeOutputTokens            = 2048
	maximumOpenCodeAuditTools              = 8
	maximumOpenCodeDefectTools             = 12
	maximumOpenCodeAuditSteps              = 5
	maximumOpenCodeDefectSteps             = 6
	maximumOpenCodeAttempts                = 2
	maximumOpenCodeInvocationDuration      = 2 * time.Minute
	maximumOpenCodeEvaluationDuration      = 25 * time.Minute
	openCodeCredentialType                 = "api"
	openCodeProvider                       = "openrouter"
	openCodeAuditAgent                     = "spice-audit"
	openCodeDefectAgent                    = "spice-defect"
	openCodeSeededDefectPath               = "internal/terminalcommand/parser.go"
	openCodeSeededDefectOriginal           = "len(endpoint) > 4096"
	openCodeSeededDefectReplacement        = "len(endpoint) >= 4096"
	openCodeAuditMarker                    = "SPICE_AUDIT_V1"
	openCodeFreeModelsEndpoint             = "https://openrouter.ai/api/v1/models"
	openCodeReviewedWorkflowPackageVersion = "opencode-ai@1.18.16"
)
