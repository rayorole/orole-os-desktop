package main

import (
	"net"
	"os"
)

// fileConn adapts the virtio-serial character device to net.Conn for http2.Server.ServeConn.
type fileConn struct{ *os.File }

var _ net.Conn = (*fileConn)(nil)

func (c *fileConn) LocalAddr() net.Addr  { return portAddr{} }
func (c *fileConn) RemoteAddr() net.Addr { return portAddr{} }

type portAddr struct{}

func (portAddr) Network() string { return "virtio-serial" }
func (portAddr) String() string  { return "org.orole.guest.0" }
