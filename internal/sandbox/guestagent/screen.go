package guestagent

import (
	"context"
	"io"
	"net"
	"net/http"
)

// ScreenDialer opens a connection to the guest's VNC server. Config.Screen is one; nil answers 501.
type ScreenDialer func(ctx context.Context) (net.Conn, error)

// screenHandler serves POST /screen: the request body is copied to the VNC server and the server's bytes are
// streamed back, flushed per read, until either side closes. Only the handler goroutine writes to the response,
// so nothing touches it after the handler returns.
func screenHandler(dial ScreenDialer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := dial(r.Context())
		if err != nil {
			http.Error(w, "the screen is not available: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(r.Context(), func() { _ = conn.Close() })
		defer stop()

		rc := http.NewResponseController(w)
		_ = rc.EnableFullDuplex() // HTTP/1.1 needs it to read the body after responding; HTTP/2 always can
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if err := rc.Flush(); err != nil {
			return
		}

		// Client to VNC. When the client stops sending, hang up so the read below ends.
		go func() {
			_, _ = io.Copy(conn, r.Body)
			_ = conn.Close()
		}()

		buf := make([]byte, 32<<10)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil || rc.Flush() != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
}
