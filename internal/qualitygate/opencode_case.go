package main

import (
	"errors"
	"slices"
)

type opencodeCase struct {
	Name         string
	Agent        string
	Marker       string
	MaximumTools int
	MaximumSteps int
	AllowedTools []string
}

func (evaluation opencodeCase) Prompt() (string, error) {
	var prompt string
	switch evaluation.Name {
	case "audit":
		prompt = "Perform a bounded read-only audit of this public Spice coding-distribution repository. Read exactly these five evidence files and do not search or inspect other paths: AGENTS.md, ARCHITECTURE.md, docs/security.md, internal/daemon/properties.go, and internal/terminal/terminal_managed_connector_bean.go. Check these required facts: APP_BOUNDARY means daemon and TUI are independent generated applications joined only through authenticated local IPC; CREDENTIAL_REDACTION means OPENAI_API_KEY is secret-redacted and excluded from observable artifacts and public errors; PROCESS_OWNERSHIP means managed launch cleans up only a daemon it started. Use only read; make no changes and do not run tests. If all facts are supported, end with exactly SPICE_AUDIT_V1 PASS. Otherwise end with SPICE_AUDIT_V1 FINDINGS followed by the failed fact identifiers. Do not reproduce file contents."
	case "seeded-defect":
		prompt = "A single comparison-operator regression was introduced into production logic under internal/terminalcommand. The required behavior is that endpoint strings of exactly 4096 bytes are valid and only longer endpoint strings are rejected. Diagnose the regression from source and existing tests, then repair only the defective production file. Do not edit tests or any other file and do not run shell commands. End with exactly SPICE_DEFECT_V1 FIXED."
	default:
		return "", errors.New("unknown OpenCode evaluation case")
	}
	if len(prompt) > maximumOpenCodePromptBytes {
		return "", errors.New("OpenCode evaluation prompt exceeds its bound")
	}
	return prompt, nil
}

func (evaluation opencodeCase) Allows(tool string) bool {
	return slices.Contains(evaluation.AllowedTools, tool)
}

func openCodeEvaluationCases() []opencodeCase {
	readTools := []string{"glob", "grep", "list", "read"}
	return []opencodeCase{
		{
			Name: "audit", Agent: openCodeAuditAgent, Marker: openCodeAuditMarker,
			MaximumTools: maximumOpenCodeAuditTools, MaximumSteps: maximumOpenCodeAuditSteps,
			AllowedTools: []string{"read"},
		},
		{
			Name: "seeded-defect", Agent: openCodeDefectAgent, Marker: "SPICE_DEFECT_V1 FIXED",
			MaximumTools: maximumOpenCodeDefectTools, MaximumSteps: maximumOpenCodeDefectSteps,
			AllowedTools: append(slices.Clone(readTools), "edit"),
		},
	}
}
