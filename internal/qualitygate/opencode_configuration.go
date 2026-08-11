package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
)

type opencodeConfiguration struct {
	catalog opencodeCatalog
}

func newOpenCodeConfiguration() opencodeConfiguration {
	return opencodeConfiguration{catalog: opencodeCatalog{}}
}

func (configuration opencodeConfiguration) Encode() ([]byte, error) {
	value := configuration.expected()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, errors.New("encode isolated OpenCode configuration")
	}
	return append(encoded, '\n'), nil
}

func (configuration opencodeConfiguration) ValidateResolved(content []byte) error {
	if len(content) == 0 || len(content) > maximumOpenCodeEventBytes {
		return errors.New("resolved OpenCode configuration has an invalid size")
	}
	var resolved map[string]json.RawMessage
	if err := json.Unmarshal(content, &resolved); err != nil {
		return errors.New("decode resolved OpenCode configuration")
	}
	if err := rejectOpenCodeIntegrations(resolved); err != nil {
		return err
	}
	var value opencodeConfigurationValue
	if err := json.Unmarshal(content, &value); err != nil {
		return errors.New("decode resolved OpenCode safety configuration")
	}
	if !matchesOpenCodeConfiguration(value, configuration.expected()) {
		return errors.New("resolved OpenCode configuration differs from the isolated contract")
	}
	return nil
}

func rejectOpenCodeIntegrations(resolved map[string]json.RawMessage) error {
	for _, forbidden := range []string{"formatter", "instructions", "lsp", "tools"} {
		raw, exists := resolved[forbidden]
		if exists && !disabledOpenCodeIntegration(raw) {
			return errors.New("resolved OpenCode configuration enabled a forbidden integration")
		}
	}
	return nil
}

func disabledOpenCodeIntegration(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) ||
		bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte("false"))
}

func matchesOpenCodeConfiguration(value, want opencodeConfigurationValue) bool {
	return !value.Autoupdate && value.Share == "disabled" && !value.Snapshot && value.SubagentDepth == 0 &&
		slices.Equal(value.EnabledProviders, want.EnabledProviders) && len(value.MCP) == 0 && len(value.Plugin) == 0 &&
		maps.Equal(value.Permission, want.Permission) && equalOpenCodeAgents(value.Agent, want.Agent) &&
		equalOpenCodeProviders(value.Provider, want.Provider)
}

func (configuration opencodeConfiguration) expected() opencodeConfigurationValue {
	models := configuration.catalog.Models()
	whitelist := make([]string, 0, len(models))
	configuredModels := make(map[string]opencodeModelConfiguration, len(models))
	for _, model := range models {
		whitelist = append(whitelist, model.Route)
		configuredModels[model.Route] = opencodeModelConfiguration{
			Limit:   opencodeModelLimit{Context: model.ContextTokens, Output: maximumOpenCodeOutputTokens},
			Options: opencodeModelOptions{Provider: opencodeProviderRouting{AllowFallbacks: false}},
		}
	}
	permissions := map[string]string{
		"*": "deny", "read": "allow", "glob": "allow", "grep": "allow", "list": "allow",
		"edit": "deny", "bash": "deny", "task": "deny", "external_directory": "deny",
		"webfetch": "deny", "websearch": "deny", "lsp": "deny", "skill": "deny",
		"question": "deny", "todowrite": "deny",
	}
	auditPermissions := map[string]string{"*": "deny", "read": "allow", "glob": "allow", "grep": "allow", "list": "allow"}
	defectPermissions := maps.Clone(auditPermissions)
	defectPermissions["edit"] = "allow"
	return opencodeConfigurationValue{
		Share: "disabled", EnabledProviders: []string{openCodeProvider},
		Provider: map[string]opencodeProviderConfiguration{
			openCodeProvider: {Whitelist: whitelist, Models: configuredModels},
		},
		MCP: map[string]any{}, Plugin: []string{}, Permission: permissions,
		Agent: map[string]opencodeAgentConfiguration{
			openCodeAuditAgent: {
				Description: "Bounded read-only Spice audit", Mode: "primary", Steps: maximumOpenCodeAuditSteps,
				Permission: auditPermissions,
			},
			openCodeDefectAgent: {
				Description: "Bounded disposable Spice defect repair", Mode: "primary", Steps: maximumOpenCodeDefectSteps,
				Permission: defectPermissions,
			},
		},
	}
}

func equalOpenCodeAgents(left, right map[string]opencodeAgentConfiguration) bool {
	if len(left) != len(right) {
		return false
	}
	for name, want := range right {
		got, exists := left[name]
		if !exists || got.Description != want.Description || got.Mode != want.Mode || got.Steps != want.Steps ||
			got.Temperature != want.Temperature || !maps.Equal(got.Permission, want.Permission) {
			return false
		}
	}
	return true
}

func equalOpenCodeProviders(left, right map[string]opencodeProviderConfiguration) bool {
	if len(left) != 1 || len(right) != 1 {
		return false
	}
	got, exists := left[openCodeProvider]
	want := right[openCodeProvider]
	if !exists || !slices.Equal(got.Whitelist, want.Whitelist) || len(got.Models) != len(want.Models) {
		return false
	}
	for route, wantModel := range want.Models {
		if gotModel, present := got.Models[route]; !present || gotModel != wantModel {
			return false
		}
	}
	return true
}
