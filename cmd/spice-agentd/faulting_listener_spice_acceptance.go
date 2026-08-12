//go:build spice_acceptance && !spice_generate

package main

import (
	"net"
	"os"
	"sync"
	"time"
)

type FaultingListener struct {
	net.Listener
	trigger string
	ack     string

	mu      sync.Mutex
	clients map[*FaultingConnection]struct{}
	closed  chan struct{}
	once    sync.Once
	fault   sync.Once
}

func NewFaultingListener(listener net.Listener, trigger, ack string) *FaultingListener {
	return &FaultingListener{
		Listener: listener,
		trigger:  trigger,
		ack:      ack,
		closed:   make(chan struct{}),
		clients:  make(map[*FaultingConnection]struct{}),
	}
}

func (listener *FaultingListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := NewFaultingConnection(connection, listener)
	listener.mu.Lock()
	listener.clients[wrapped] = struct{}{}
	listener.mu.Unlock()
	return wrapped, nil
}

func (listener *FaultingListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return listener.Listener.Close()
}

func (listener *FaultingListener) observeTrigger() {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-listener.closed:
			return
		case <-ticker.C:
			if _, err := os.Stat(listener.trigger); err != nil {
				continue
			}
			listener.fault.Do(listener.faultConnections)
			return
		}
	}
}

func (listener *FaultingListener) faultConnections() {
	listener.mu.Lock()
	connections := make([]*FaultingConnection, 0, len(listener.clients))
	for connection := range listener.clients {
		connections = append(connections, connection)
	}
	listener.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	_ = os.WriteFile(listener.ack, []byte("faulted\n"), 0o600)
}

func (listener *FaultingListener) remove(connection *FaultingConnection) {
	listener.mu.Lock()
	delete(listener.clients, connection)
	listener.mu.Unlock()
}
