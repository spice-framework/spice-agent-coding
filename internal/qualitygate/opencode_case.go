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
		prompt = "Perform a bounded read-only architecture audit of this public Spice coding-distribution repository. Inspect AGENTS.md, ARCHITECTURE.md, and only the source needed to verify daemon/TUI separation, credential redaction, and process ownership. Use only read, glob, grep, or list and make no changes. End with one concise line beginning SPICE_AUDIT_V1 followed by PASS or FINDINGS and category identifiers; do not reproduce file contents."
	case "seeded-defect":
		prompt = "A single one-token boundary regression was introduced into production logic under internal/terminalcommand. Diagnose it from source and tests, then repair only the defective production file. Do not edit tests or any other file. End with the exact marker SPICE_DEFECT_V1 FIXED."
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
			AllowedTools: slices.Clone(readTools),
		},
		{
			Name: "seeded-defect", Agent: openCodeDefectAgent, Marker: "SPICE_DEFECT_V1 FIXED",
			MaximumTools: maximumOpenCodeDefectTools, MaximumSteps: maximumOpenCodeDefectSteps,
			AllowedTools: append(slices.Clone(readTools), "edit"),
		},
	}
}
