package logging

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Scope is one exact logging ownership boundary. A component always belongs
// to a module; the zero value is the application root.
type Scope struct {
	Module    string
	Component string
}

// ID returns the canonical exact control identity for this scope.
func (scope Scope) ID() string {
	if scope.Module == "" {
		return "root"
	}
	if scope.Component == "" {
		return "module:" + scope.Module
	}
	return "component:" + scope.Module + "#" + scope.Component
}

func (scope Scope) validate() error {
	if scope.Module == "" {
		if scope.Component != "" {
			return errors.New("logging root scope cannot declare a component")
		}
		return nil
	}
	if strings.TrimSpace(scope.Module) != scope.Module || strings.ContainsAny(scope.Module, "\x00\r\n\t") {
		return fmt.Errorf("logging module scope %q is invalid", scope.Module)
	}
	if strings.TrimSpace(scope.Component) != scope.Component || strings.ContainsAny(scope.Component, "\x00\r\n\t#") {
		return fmt.Errorf("logging component scope %q is invalid", scope.Component)
	}
	return nil
}

// LevelRule sets one startup level for an exact non-root scope.
type LevelRule struct {
	Scope Scope
	Level Level
}

type scopeLevel struct {
	scope    Scope
	startup  *Level
	override *Level
}

// ScopeLevel is one immutable level-control snapshot item.
type ScopeLevel struct {
	Scope           string `json:"scope"`
	ConfiguredLevel Level  `json:"configured_level"`
	EffectiveLevel  Level  `json:"effective_level"`
	Overridden      bool   `json:"overridden"`
}

// LevelSnapshot is a deterministic snapshot of root and registered scopes.
type LevelSnapshot struct {
	Scopes []ScopeLevel `json:"scopes"`
}

// Controller owns the mutable level policy for one logger. It is safe for
// concurrent use and never changes process-global state.
type Controller struct {
	mu           sync.RWMutex
	rootStartup  Level
	rootOverride *Level
	scopes       map[string]scopeLevel
}

func newController(root Level, scopes []Scope, rules []LevelRule) (*Controller, error) {
	if !root.validConfiguredLevel() {
		return nil, fmt.Errorf("logging root level %d is invalid", root)
	}
	controller := &Controller{rootStartup: root, scopes: make(map[string]scopeLevel, len(scopes))}
	for index, scope := range scopes {
		if err := scope.validate(); err != nil {
			return nil, fmt.Errorf("logging scope %d: %w", index, err)
		}
		if scope.ID() == "root" {
			return nil, fmt.Errorf("logging scope %d duplicates the implicit root", index)
		}
		if _, duplicate := controller.scopes[scope.ID()]; duplicate {
			return nil, fmt.Errorf("logging scope %q is duplicated", scope.ID())
		}
		controller.scopes[scope.ID()] = scopeLevel{scope: scope}
	}
	for index, rule := range rules {
		if !rule.Level.validConfiguredLevel() {
			return nil, fmt.Errorf("logging level rule %d has invalid level %d", index, rule.Level)
		}
		item, found := controller.scopes[rule.Scope.ID()]
		if !found {
			return nil, fmt.Errorf("logging level rule %d references unknown scope %q", index, rule.Scope.ID())
		}
		if item.startup != nil {
			return nil, fmt.Errorf("logging level rule scope %q is duplicated", rule.Scope.ID())
		}
		value := rule.Level
		item.startup = &value
		controller.scopes[rule.Scope.ID()] = item
	}
	return controller, nil
}

// Set installs a runtime override for root or one registered exact scope.
func (controller *Controller) Set(scopeID string, level Level) error {
	if controller == nil {
		return errors.New("set logging level: controller is nil")
	}
	if !level.validConfiguredLevel() {
		return fmt.Errorf("set logging level: level %d is invalid", level)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	value := level
	if scopeID == "root" {
		controller.rootOverride = &value
		return nil
	}
	item, found := controller.scopes[scopeID]
	if !found {
		return fmt.Errorf("set logging level: scope %q is unknown", scopeID)
	}
	item.override = &value
	controller.scopes[scopeID] = item
	return nil
}

// Reset removes one runtime override and restores its startup policy.
func (controller *Controller) Reset(scopeID string) error {
	if controller == nil {
		return errors.New("reset logging level: controller is nil")
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if scopeID == "root" {
		controller.rootOverride = nil
		return nil
	}
	item, found := controller.scopes[scopeID]
	if !found {
		return fmt.Errorf("reset logging level: scope %q is unknown", scopeID)
	}
	item.override = nil
	controller.scopes[scopeID] = item
	return nil
}

func (controller *Controller) enabled(scope Scope, level Level) bool {
	return controller.enabledValue(scope, int(level))
}

func (controller *Controller) enabledValue(scope Scope, level int) bool {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	effective := controller.effectiveLocked(scope)
	return effective != LevelOff && level >= int(effective)
}

func (controller *Controller) registered(scope Scope) bool {
	if scope.ID() == "root" {
		return true
	}
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	_, found := controller.scopes[scope.ID()]
	return found
}

func (controller *Controller) effectiveLocked(scope Scope) Level {
	if item, found := controller.scopes[scope.ID()]; found {
		if item.override != nil {
			return *item.override
		}
		if item.startup != nil {
			return *item.startup
		}
	}
	if scope.Component != "" {
		moduleID := (Scope{Module: scope.Module}).ID()
		if item, found := controller.scopes[moduleID]; found {
			if item.override != nil {
				return *item.override
			}
			if item.startup != nil {
				return *item.startup
			}
		}
	}
	if controller.rootOverride != nil {
		return *controller.rootOverride
	}
	return controller.rootStartup
}

func (controller *Controller) configuredLocked(scope Scope) Level {
	if item, found := controller.scopes[scope.ID()]; found && item.startup != nil {
		return *item.startup
	}
	if scope.Component != "" {
		if item, found := controller.scopes[(Scope{Module: scope.Module}).ID()]; found && item.startup != nil {
			return *item.startup
		}
	}
	return controller.rootStartup
}

// Snapshot returns root followed by exact scopes in lexical ID order.
func (controller *Controller) Snapshot() LevelSnapshot {
	if controller == nil {
		return LevelSnapshot{}
	}
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	rootConfigured := controller.rootStartup
	rootEffective := controller.rootStartup
	rootOverridden := controller.rootOverride != nil
	if rootOverridden {
		rootEffective = *controller.rootOverride
	}
	result := LevelSnapshot{Scopes: []ScopeLevel{{
		Scope: "root", ConfiguredLevel: rootConfigured,
		EffectiveLevel: rootEffective, Overridden: rootOverridden,
	}}}
	ids := make([]string, 0, len(controller.scopes))
	for id := range controller.scopes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := controller.scopes[id]
		configured := controller.configuredLocked(item.scope)
		result.Scopes = append(result.Scopes, ScopeLevel{
			Scope: id, ConfiguredLevel: configured,
			EffectiveLevel: controller.effectiveLocked(item.scope),
			Overridden:     item.override != nil,
		})
	}
	return result
}

// ParseLevelRules parses comma-separated exact scope=level startup rules.
func ParseLevelRules(value string, scopes []Scope) ([]LevelRule, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	known := make(map[string]Scope, len(scopes))
	for _, scope := range scopes {
		if err := scope.validate(); err != nil {
			return nil, err
		}
		known[scope.ID()] = scope
	}
	parts := strings.Split(value, ",")
	result := make([]LevelRule, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for index, part := range parts {
		id, encodedLevel, found := strings.Cut(part, "=")
		id = strings.TrimSpace(id)
		if !found || id == "" || strings.TrimSpace(encodedLevel) == "" {
			return nil, fmt.Errorf("logging level rule %d must be scope=level", index)
		}
		scope, exists := known[id]
		if !exists {
			return nil, fmt.Errorf("logging level rule %d references unknown scope %q", index, id)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("logging level rule scope %q is duplicated", id)
		}
		level, err := ParseLevel(encodedLevel)
		if err != nil {
			return nil, fmt.Errorf("logging level rule %d: %w", index, err)
		}
		seen[id] = struct{}{}
		result = append(result, LevelRule{Scope: scope, Level: level})
	}
	slices.SortFunc(result, func(left, right LevelRule) int {
		return strings.Compare(left.Scope.ID(), right.Scope.ID())
	})
	return result, nil
}
