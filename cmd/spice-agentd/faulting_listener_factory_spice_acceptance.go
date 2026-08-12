//go:build spice_acceptance && !spice_generate

package main

import (
	"net"

	"github.com/spice-framework/spice-agent-coding/internal/daemon"
)

type faultingListenerFactory struct {
	trigger string
	ack     string
}

func (factory *faultingListenerFactory) Listen(address string) (net.Listener, error) {
	listener, err := daemon.NewListenerFactory().Listen(address)
	if err != nil {
		return nil, err
	}
	wrapped := NewFaultingListener(listener, factory.trigger, factory.ack)
	go wrapped.observeTrigger()
	return wrapped, nil
}
