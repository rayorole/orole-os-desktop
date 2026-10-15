//go:build linux

package main

import "testing"

func TestLookupIdentity(t *testing.T) {
	id, home, err := lookupIdentity("root")
	if err != nil {
		t.Skip("no root entry in the user database:", err)
	}
	if id.Name != "root" || id.UID != 0 || id.GID != 0 || home == "" {
		t.Fatalf("root = %+v home %q", id, home)
	}
	if _, _, err := lookupIdentity("orole-no-such-user"); err == nil {
		t.Fatal("unknown user must be an error")
	}
}
