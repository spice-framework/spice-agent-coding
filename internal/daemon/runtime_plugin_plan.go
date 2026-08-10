package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// RuntimePluginPlan is an immutable, validated complete desired configuration.
// It contains either no executable or exactly one explicitly configured
// executable; it performs no discovery or filesystem access.
type RuntimePluginPlan struct {
	enabled        bool
	required       bool
	cleanupTimeout time.Duration
	set            pluginhost.Set
}

// Enabled reports whether the application explicitly configured a plugin.
func (plan RuntimePluginPlan) Enabled() bool { return plan.enabled }

// Required reports whether activation failure must prevent endpoint publication.
func (plan RuntimePluginPlan) Required() bool { return plan.required }

// Set returns the immutable complete desired set. The host API exposes only
// independently backed executable snapshots from that value.
func (plan RuntimePluginPlan) Set() pluginhost.Set {
	return plan.set
}

// String intentionally exposes only bounded admission state. In particular,
// formatting a plan must never disclose its executable path or digest.
func (plan RuntimePluginPlan) String() string {
	return fmt.Sprintf(
		"RuntimePluginPlan{enabled:%t required:%t plugins:%d}",
		plan.enabled,
		plan.required,
		plan.set.Len(),
	)
}

// GoString preserves the same redaction contract for diagnostic %#v output.
func (plan RuntimePluginPlan) GoString() string { return plan.String() }

// Format preserves redaction for every fmt formatting verb and flag.
func (plan RuntimePluginPlan) Format(state fmt.State, _ rune) {
	if _, err := io.WriteString(state, plan.String()); err != nil {
		return
	}
}

// MarshalJSON exposes bounded state rather than private executable metadata.
func (plan RuntimePluginPlan) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Enabled  bool `json:"enabled"`
		Required bool `json:"required"`
		Plugins  int  `json:"plugins"`
	}{
		Enabled:  plan.enabled,
		Required: plan.required,
		Plugins:  plan.set.Len(),
	})
}

// Validate rejects zero/corrupted enabled plans without performing I/O.
func (plan RuntimePluginPlan) Validate() error {
	if err := plan.set.Validate(); err != nil {
		return errors.New("runtime plugin plan set is invalid")
	}
	if plan.enabled != (plan.set.Len() == 1) || plan.set.Len() > 1 || plan.required && !plan.enabled ||
		plan.cleanupTimeout <= 0 {
		return errors.New("runtime plugin plan state is invalid")
	}
	return nil
}
