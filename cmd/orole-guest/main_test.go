package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenFromCmdline(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"BOOT_IMAGE=/vmlinuz console=ttyS0 orole.token=abc123 quiet", "abc123"},
		{"orole.token=first orole.token=second", "first"},
		{"quiet splash", ""},
		{"orole.token=", ""},
		{"orole.tokens=x", ""},
		{"  orole.token=t1\n", "t1"},
	} {
		if got := tokenFromCmdline(tc.in); got != tc.want {
			t.Errorf("tokenFromCmdline(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Off Linux there is no fw_cfg reader, so the command line is the only source; on Linux a missing fw_cfg file falls back to it.
func TestReadTokenFallsBackToCmdline(t *testing.T) {
	dir := t.TempDir()
	cmdline := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdline, []byte("quiet orole.token=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := readTokenFrom(filepath.Join(dir, "missing"), cmdline); err != nil || tok != "abc" {
		t.Fatalf("got %q %v", tok, err)
	}
	if _, err := readTokenFrom(filepath.Join(dir, "missing"), filepath.Join(dir, "nocmdline")); err == nil {
		t.Fatal("no source must be an error")
	}
}
