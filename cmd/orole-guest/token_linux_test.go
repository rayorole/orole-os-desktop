//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTokenPrefersFwCfg(t *testing.T) {
	dir := t.TempDir()
	fw, cmdline := filepath.Join(dir, "raw"), filepath.Join(dir, "cmdline")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(fw, "fwtoken\n")
	write(cmdline, "init=/x orole.token=cmdtoken quiet\n")
	if tok, err := readTokenFrom(fw, cmdline); err != nil || tok != "fwtoken" {
		t.Fatalf("fw_cfg: got %q %v", tok, err)
	}
	write(fw, " \n") // present but empty: fall back
	if tok, err := readTokenFrom(fw, cmdline); err != nil || tok != "cmdtoken" {
		t.Fatalf("empty fw_cfg: got %q %v", tok, err)
	}
	if err := os.Remove(fw); err != nil {
		t.Fatal(err)
	}
	if tok, err := readTokenFrom(fw, cmdline); err != nil || tok != "cmdtoken" {
		t.Fatalf("fallback: got %q %v", tok, err)
	}
	// A fw_cfg token that exists but cannot be read must not silently fall back to the command line.
	if err := os.Mkdir(fw, 0o700); err != nil {
		t.Fatal(err)
	}
	if tok, err := readTokenFrom(fw, cmdline); err == nil {
		t.Fatalf("unreadable fw_cfg fell back to %q", tok)
	}
	if err := os.Remove(fw); err != nil {
		t.Fatal(err)
	}
	write(cmdline, "init=/x quiet\n")
	if _, err := readTokenFrom(fw, cmdline); err == nil {
		t.Fatal("no token anywhere must be an error")
	}
}
