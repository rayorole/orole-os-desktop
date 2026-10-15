//go:build !linux

package main

import (
	"errors"
	"net"
)

var errNotGuest = errors.New("orole-guest runs only inside the Linux guest")

func openPort() (net.Conn, error) { return nil, errNotGuest }

func powerOff() error { return errNotGuest }
