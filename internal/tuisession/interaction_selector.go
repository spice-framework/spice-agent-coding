package tuisession

import (
	"slices"
	"strings"

	"github.com/spice-framework/spice-agent/client"
)

type interactionSelector struct{}

func (interactionSelector) selectCurrent(
	values map[interactionKey]client.PendingInteraction,
	activeRun client.RunRef,
	hasActiveRun bool,
) (client.PendingInteraction, bool) {
	keys := make([]interactionKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(left, right interactionKey) int {
		leftActive := hasActiveRun && left.run == activeRun.ID()
		rightActive := hasActiveRun && right.run == activeRun.ID()
		if leftActive != rightActive {
			if leftActive {
				return -1
			}
			return 1
		}
		if comparison := strings.Compare(left.run, right.run); comparison != 0 {
			return comparison
		}
		return strings.Compare(left.id, right.id)
	})
	if len(keys) == 0 {
		return client.PendingInteraction{}, false
	}
	return values[keys[0]], true
}

func (interactionSelector) same(
	left client.PendingInteraction,
	hasLeft bool,
	right client.PendingInteraction,
	hasRight bool,
) bool {
	if hasLeft != hasRight {
		return false
	}
	if !hasLeft {
		return true
	}
	return left.Run().ID() == right.Run().ID() && left.ID() == right.ID() &&
		left.Kind() == right.Kind() && left.Prompt() == right.Prompt()
}
