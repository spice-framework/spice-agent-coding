//go:build spice_acceptance && !spice_generate

package main

import (
	"net"
	"sync"
)

type FaultingConnection struct {
	net.Conn
	owner *FaultingListener
	once  sync.Once
	err   error
}

func NewFaultingConnection(connection net.Conn, owner *FaultingListener) *FaultingConnection {
	return &FaultingConnection{Conn: connection, owner: owner}
}

func (connection *FaultingConnection) Close() error {
	connection.once.Do(func() {
		connection.err = connection.Conn.Close()
		connection.owner.remove(connection)
	})
	return connection.err
}
