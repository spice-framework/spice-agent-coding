//go:build spice_acceptance && !spice_generate

package main

import (
	"net"
	"os"
	"sync"
	"time"
)

type faultingListener struct {
	net.Listener
	trigger string
	ack     string

	mu      sync.Mutex
	clients map[*faultingConnection]struct{}
	closed  chan struct{}
	once    sync.Once
	fault   sync.Once
}

func newFaultingListener(listener net.Listener, trigger, ack string) *faultingListener {
	return &faultingListener{
		Listener: listener,
		trigger:  trigger,
		ack:      ack,
		closed:   make(chan struct{}),
		clients:  make(map[*faultingConnection]struct{}),
	}
}

func (listener *faultingListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := newFaultingConnection(connection, listener)
	listener.mu.Lock()
	listener.clients[wrapped] = struct{}{}
	listener.mu.Unlock()
	return wrapped, nil
}

func (listener *faultingListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return listener.Listener.Close()
}

func (listener *faultingListener) observeTrigger() {
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

func (listener *faultingListener) faultConnections() {
	listener.mu.Lock()
	connections := make([]*faultingConnection, 0, len(listener.clients))
	for connection := range listener.clients {
		connections = append(connections, connection)
	}
	listener.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	_ = os.WriteFile(listener.ack, []byte("faulted\n"), 0o600)
}

func (listener *faultingListener) remove(connection *faultingConnection) {
	listener.mu.Lock()
	delete(listener.clients, connection)
	listener.mu.Unlock()
}
