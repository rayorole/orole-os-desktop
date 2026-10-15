package guestagent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func screenServer(t *testing.T, dial guestagent.ScreenDialer) *httptest.Server {
	t.Helper()
	a, _ := newAgent(t, func(c *guestagent.Config) { c.Screen = dial })
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func screenRequest(t *testing.T, srv *httptest.Server, body io.Reader) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/screen", body)
	req.Header.Set(wire.TokenHeader, token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestScreenRelaysBothWays(t *testing.T) {
	guestSide, vnc := net.Pipe()
	srv := screenServer(t, func(context.Context) (net.Conn, error) { return guestSide, nil })
	go func() { // a VNC server that greets, then answers in upper case
		_, _ = vnc.Write([]byte("RFB 003.008\n"))
		buf := make([]byte, 64)
		n, err := vnc.Read(buf)
		if err == nil {
			_, _ = vnc.Write(bytes.ToUpper(buf[:n]))
		}
	}()
	body, input := io.Pipe()
	resp := screenRequest(t, srv, body)
	defer resp.Body.Close()
	greet := make([]byte, 12)
	if _, err := io.ReadFull(resp.Body, greet); err != nil || string(greet) != "RFB 003.008\n" {
		t.Fatalf("greeting %q %v", greet, err)
	}
	if _, err := input.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, echo); err != nil || string(echo) != "HELLO" {
		t.Fatalf("echo %q %v", echo, err)
	}
	input.Close()
}

func TestScreenClosesTheVNCConnectionWhenTheClientLeaves(t *testing.T) {
	guestSide, vnc := net.Pipe()
	srv := screenServer(t, func(context.Context) (net.Conn, error) { return guestSide, nil })
	body, input := io.Pipe()
	resp := screenRequest(t, srv, body)
	input.Close()
	resp.Body.Close()
	_ = vnc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := vnc.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("the relay must close the VNC side, got %v", err)
	}
}

func TestScreenEndsWhenTheVNCServerHangsUp(t *testing.T) {
	guestSide, vnc := net.Pipe()
	srv := screenServer(t, func(context.Context) (net.Conn, error) { return guestSide, nil })
	body, input := io.Pipe()
	defer input.Close()
	resp := screenRequest(t, srv, body)
	defer resp.Body.Close()
	_ = vnc.Close()
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, resp.Body); done <- err }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the response did not end after the VNC server hung up")
	}
}

func TestScreenWithoutVNCIsUnavailable(t *testing.T) {
	a, _ := newAgent(t, func(c *guestagent.Config) {
		c.Screen = func(context.Context) (net.Conn, error) { return nil, errors.New("connection refused") }
	})
	rec := call(t, a, http.MethodPost, "/screen", token, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestScreenUnconfiguredIsNotImplemented(t *testing.T) {
	a, _ := newAgent(t, nil)
	if rec := call(t, a, http.MethodPost, "/screen", token, nil); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status %d", rec.Code)
	}
}
