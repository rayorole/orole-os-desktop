package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeQMP answers like QEMU's monitor and records the commands it receives.
func fakeQMP(conn net.Conn, reply func(cmd string) string) <-chan []string {
	done := make(chan []string, 1)
	go func() {
		defer conn.Close()
		fmt.Fprintln(conn, `{"QMP": {"version": {"qemu": {"major": 11}}, "capabilities": []}}`)
		var got []string
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			line := sc.Text()
			got = append(got, line)
			fmt.Fprintln(conn, reply(line))
			if strings.Contains(line, "system_powerdown") {
				break
			}
		}
		done <- got
	}()
	return done
}

func TestQMPPowerdownDoesTheHandshakeFirst(t *testing.T) {
	host, qemu := net.Pipe()
	got := fakeQMP(qemu, func(cmd string) string {
		if strings.Contains(cmd, "system_powerdown") {
			return `{"event": "POWERDOWN", "timestamp": {"seconds": 1, "microseconds": 0}}` + "\n" + `{"return": {}}`
		}
		return `{"return": {}}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := qmpPowerdown(ctx, host); err != nil {
		t.Fatal(err)
	}
	cmds := <-got
	if len(cmds) != 2 || !strings.Contains(cmds[0], `"qmp_capabilities"`) || !strings.Contains(cmds[1], `"system_powerdown"`) {
		t.Fatalf("commands %q", cmds)
	}
}

func TestQMPPowerdownReportsAnError(t *testing.T) {
	host, qemu := net.Pipe()
	fakeQMP(qemu, func(cmd string) string {
		if strings.Contains(cmd, "system_powerdown") {
			return `{"error": {"class": "GenericError", "desc": "no ACPI"}}`
		}
		return `{"return": {}}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := qmpPowerdown(ctx, host); err == nil || !strings.Contains(err.Error(), "no ACPI") {
		t.Fatalf("err = %v", err)
	}
}

func TestQMPPowerdownGivesUpWhenQEMUIsSilent(t *testing.T) {
	host, qemu := net.Pipe()
	defer qemu.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := qmpPowerdown(ctx, host); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

// The backend negotiates capabilities at boot (the reply proves QEMU finished initialising, so the token
// file can go) and sends system_powerdown later on the same connection, skipping events in between.
func TestQMPClientHandshakeThenCommandLater(t *testing.T) {
	host, qemu := net.Pipe()
	got := fakeQMP(qemu, func(cmd string) string {
		if strings.Contains(cmd, "qmp_capabilities") {
			return `{"return": {}}` + "\n" + `{"event": "RTC_CHANGE", "data": {"offset": 0}, "timestamp": {"seconds": 1, "microseconds": 0}}`
		}
		return `{"return": {}}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := qmpConnect(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.execute(ctx, "system_powerdown"); err != nil {
		t.Fatal(err)
	}
	if cmds := <-got; len(cmds) != 2 {
		t.Fatalf("commands %q", cmds)
	}
}

func TestQMPConnectRejectsANonQMPGreeting(t *testing.T) {
	host, qemu := net.Pipe()
	go func() { fmt.Fprintln(qemu, `{"hello": 1}`); qemu.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := qmpConnect(ctx, host); err == nil {
		t.Fatal("accepted a greeting without QMP")
	}
}

// qmp_capabilities is allowed before the machine exists (QEMU can dispatch QMP from aio_poll during init), so
// readiness is a command QEMU refuses until machine initialization is done.
func TestQMPWaitMachineReadyRetriesUntilTheMachineExists(t *testing.T) {
	host, qemu := net.Pipe()
	queries := 0
	got := fakeQMP(qemu, func(cmd string) string {
		if strings.Contains(cmd, "query-cpus-fast") {
			queries++
			if queries < 3 {
				return `{"error": {"class": "GenericError", "desc": "The command query-cpus-fast is permitted only after machine initialization has completed"}}`
			}
			return `{"return": [{"cpu-index": 0}]}`
		}
		if strings.Contains(cmd, "system_powerdown") {
			return `{"return": {}}`
		}
		return `{"return": {}}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := qmpConnect(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.waitMachineReady(ctx); err != nil {
		t.Fatal(err)
	}
	if queries != 3 {
		t.Fatalf("queries = %d", queries)
	}
	c.execute(ctx, "system_powerdown")
	<-got
}

func TestQMPWaitMachineReadyGivesUpAtTheDeadline(t *testing.T) {
	host, qemu := net.Pipe()
	fakeQMP(qemu, func(cmd string) string {
		if strings.Contains(cmd, "query-cpus-fast") {
			return `{"error": {"class": "GenericError", "desc": "The command query-cpus-fast is permitted only after machine initialization has completed"}}`
		}
		return `{"return": {}}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c, err := qmpConnect(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.waitMachineReady(ctx); err == nil {
		t.Fatal("ready although QEMU never finished initialising")
	}
}

func TestQMPWaitMachineReadyReportsOtherErrors(t *testing.T) {
	host, qemu := net.Pipe()
	fakeQMP(qemu, func(cmd string) string {
		if strings.Contains(cmd, "query-cpus-fast") {
			return `{"error": {"class": "CommandNotFound", "desc": "nope"}}`
		}
		return `{"return": {}}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := qmpConnect(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.waitMachineReady(ctx); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}
