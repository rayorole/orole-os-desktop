package guestagent

import (
	"context"
	"net"
	"time"

	"golang.org/x/net/http2"
)

// ServeConn serves HTTP/2 without TLS (prior knowledge, no upgrade) on c until c fails or ctx ends.
func (a *Agent) ServeConn(ctx context.Context, c net.Conn) {
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	(&http2.Server{}).ServeConn(c, &http2.ServeConnOpts{Context: ctx, Handler: a.Handler()})
}

// ServeLoop serves one connection at a time from open, opening the next when one ends, until ctx ends. The
// virtio-serial port reports the host side going away as EOF; reopening picks up the next host connection.
func (a *Agent) ServeLoop(ctx context.Context, open func() (net.Conn, error), retry time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := open()
		if err != nil {
			a.cfg.Logf("open guest channel: %v", err)
		} else {
			a.ServeConn(ctx, c)
			_ = c.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retry):
		}
	}
}
