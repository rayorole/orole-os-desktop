// Package agenttest lets a test binary act as the shell that guestagent runs for /exec, so exec tests behave the
// same on Windows and Linux without /bin/sh. Call Main from TestMain and put Shell() in guestagent.Config.Shell.
package agenttest

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperArg = "orole-guest-helper"

// Main runs one helper command when the binary was started as the helper, otherwise the tests.
func Main(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == helperArg {
		os.Exit(run(os.Args[2]))
	}
	os.Exit(m.Run())
}

// Shell is the guestagent.Config.Shell value that runs commands through this binary; the agent appends the command.
func Shell() []string {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return []string{exe, helperArg}
}

// run understands: echo TEXT, err TEXT, exit N, flood N (N bytes of 'y'), forever (writes until killed),
// sleep DURATION, pwd, env NAME.
func run(command string) int {
	verb, arg, _ := strings.Cut(command, " ")
	switch verb {
	case "echo":
		fmt.Println(arg)
	case "err":
		fmt.Fprintln(os.Stderr, arg)
	case "exit":
		n, _ := strconv.Atoi(arg)
		return n
	case "flood":
		n, _ := strconv.Atoi(arg)
		os.Stdout.Write(bytes.Repeat([]byte("y"), n))
	case "forever":
		chunk := bytes.Repeat([]byte("y\n"), 4096)
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				return 1
			}
		}
	case "sleep":
		d, _ := time.ParseDuration(arg)
		time.Sleep(d)
	case "pwd":
		wd, _ := os.Getwd()
		fmt.Println(wd)
	case "env":
		fmt.Println(os.Getenv(arg))
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown command %q\n", command)
		return 127
	}
	return 0
}
