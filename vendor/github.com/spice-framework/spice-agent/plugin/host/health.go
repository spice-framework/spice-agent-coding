package pluginhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/spice-framework/spice-agent/stage"
)

type HealthState string

const (
	HealthStateReady       HealthState = "ready"
	HealthStateDegraded    HealthState = "degraded"
	HealthStateRecovering  HealthState = "recovering"
	HealthStateUnavailable HealthState = "unavailable"
	HealthStateStopping    HealthState = "stopping"
	HealthStateStopped     HealthState = "stopped"
)

type HealthIssue string

const (
	HealthIssueCurrentGenerationStopped HealthIssue = "current_generation_stopped"
	HealthIssueRestartFailed            HealthIssue = "restart_failed"
	HealthIssueRestartExhausted         HealthIssue = "restart_exhausted"
	HealthIssueCleanupFailed            HealthIssue = "cleanup_failed"
)

// Health is an immutable secret-safe snapshot of host availability and owned
// resources. It deliberately contains no process error, path, endpoint,
// environment, digest, manifest, or handshake material.
type Health struct {
	state               HealthState
	issues              []HealthIssue
	currentPlanID       stage.PlanID
	restartAttempts     uint32
	restartLimit        uint32
	activeLeases        int
	retainedGenerations int
}

func (health Health) State() HealthState          { return health.state }
func (health Health) Issues() []HealthIssue       { return slices.Clone(health.issues) }
func (health Health) CurrentPlanID() stage.PlanID { return health.currentPlanID }
func (health Health) RestartAttempts() uint32     { return health.restartAttempts }
func (health Health) RestartLimit() uint32        { return health.restartLimit }
func (health Health) ActiveLeases() int           { return health.activeLeases }
func (health Health) RetainedGenerations() int    { return health.retainedGenerations }

func (health Health) Validate() error {
	switch health.state {
	case HealthStateReady, HealthStateDegraded, HealthStateRecovering,
		HealthStateUnavailable, HealthStateStopping, HealthStateStopped:
	default:
		return errors.New("runtime plugin health state is invalid")
	}
	if health.restartAttempts > health.restartLimit || health.restartLimit > MaximumRestartAttempts {
		return errors.New("runtime plugin health restart count is invalid")
	}
	if health.activeLeases < 0 || health.retainedGenerations < 0 {
		return errors.New("runtime plugin health ownership count is invalid")
	}
	if health.state != HealthStateStopped {
		if err := health.currentPlanID.Validate(); err != nil {
			return errors.New("runtime plugin health current generation is invalid")
		}
	} else if health.currentPlanID != "" || health.activeLeases != 0 || health.retainedGenerations != 0 {
		return errors.New("stopped runtime plugin health retains ownership")
	}
	for index, issue := range health.issues {
		switch issue {
		case HealthIssueCurrentGenerationStopped, HealthIssueRestartFailed,
			HealthIssueRestartExhausted, HealthIssueCleanupFailed:
		default:
			return errors.New("runtime plugin health issue is invalid")
		}
		if index > 0 && health.issues[index-1] >= issue {
			return errors.New("runtime plugin health issues are not canonical")
		}
	}
	return nil
}

func (health Health) String() string {
	return fmt.Sprintf(
		"pluginhost.Health(state=%s,issues=%v,restarts=%d/%d,leases=%d,generations=%d)",
		health.state,
		health.issues,
		health.restartAttempts,
		health.restartLimit,
		health.activeLeases,
		health.retainedGenerations,
	)
}

func (health Health) GoString() string { return health.String() }

func (health Health) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, health.String())
}

func (health Health) MarshalJSON() ([]byte, error) {
	if err := health.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		State               HealthState   `json:"state"`
		Issues              []HealthIssue `json:"issues"`
		CurrentPlanID       stage.PlanID  `json:"current_plan_id,omitempty"`
		RestartAttempts     uint32        `json:"restart_attempts"`
		RestartLimit        uint32        `json:"restart_limit"`
		ActiveLeases        int           `json:"active_leases"`
		RetainedGenerations int           `json:"retained_generations"`
	}{
		State: health.state, Issues: slices.Clone(health.issues), CurrentPlanID: health.currentPlanID,
		RestartAttempts: health.restartAttempts, RestartLimit: health.restartLimit,
		ActiveLeases: health.activeLeases, RetainedGenerations: health.retainedGenerations,
	})
}

// Health returns one immutable host snapshot. Arbitrary dependency failures
// are reduced to fixed issue codes before crossing this boundary.
func (host *Host) Health() Health {
	if host == nil {
		return Health{state: HealthStateStopped}
	}
	host.mu.Lock()
	defer host.mu.Unlock()

	activeLeases := 0
	cleanupFailed := false
	for generation := range host.owned {
		activeLeases += generation.refs
		cleanupFailed = cleanupFailed || generation.cleanupErr != nil
	}
	health := Health{
		state: HealthStateReady, restartLimit: host.restart.maximumAttempts,
		activeLeases: activeLeases, retainedGenerations: len(host.owned),
	}
	if host.stopped {
		health.state = HealthStateStopped
		return health
	}
	if host.current != nil {
		health.currentPlanID = host.current.id
	}
	if host.closing {
		health.state = HealthStateStopping
	} else if host.current != nil && host.current.unhealthy != nil {
		health.issues = append(health.issues, HealthIssueCurrentGenerationStopped)
		if host.recovery == nil {
			health.state = HealthStateUnavailable
		} else {
			health.restartAttempts = host.recovery.attempts
			if host.recovery.failed {
				health.issues = append(health.issues, HealthIssueRestartFailed)
			}
			if host.recovery.exhausted {
				health.state = HealthStateUnavailable
				health.issues = append(health.issues, HealthIssueRestartExhausted)
			} else {
				health.state = HealthStateRecovering
			}
		}
	}
	if cleanupFailed {
		health.issues = append(health.issues, HealthIssueCleanupFailed)
		if health.state == HealthStateReady {
			health.state = HealthStateDegraded
		}
	}
	slices.Sort(health.issues)
	return health
}
