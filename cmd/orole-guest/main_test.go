package main

import "testing"

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
