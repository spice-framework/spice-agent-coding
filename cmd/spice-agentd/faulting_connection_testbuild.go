//go:build spice_acceptance && !spice_generate

package main

import (
	"net"
	"sync"
)

type faultingConnection struct {
	net.Conn
	owner *faultingListener
	once  sync.Once
	err   error
}

func newFaultingConnection(connection net.Conn, owner *faultingListener) *faultingConnection {
	return &faultingConnection{Conn: connection, owner: owner}
}

func (connection *faultingConnection) Close() error {
	connection.once.Do(func() {
		connection.err = connection.Conn.Close()
		connection.owner.remove(connection)
	})
	return connection.err
}
