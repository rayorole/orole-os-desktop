package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultLimits(t *testing.T) {
	if got := DefaultLimits(); got != (Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 10}) {
		t.Fatalf("DefaultLimits() = %+v", got)
	}
	if got := (Limits{CPUs: 4}).withDefaults(); got != (Limits{MemoryMB: 2048, CPUs: 4, DiskGB: 10}) {
		t.Fatalf("withDefaults kept the wrong fields: %+v", got)
	}
	if got := (Limits{MemoryMB: -1, CPUs: 0, DiskGB: 20}).withDefaults(); got != (Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 20}) {
		t.Fatalf("withDefaults with bad values: %+v", got)
	}
}

func TestCheckBotID(t *testing.T) {
	for _, id := range []string{"b1", "0b6a2c1e-6f7d-4d0a-9a39-3f1c2b8f9e10", "bot_2", strings.Repeat("a", 64), "com0", "console", "lpt10"} {
		if err := checkBotID(id); err != nil {
			t.Errorf("checkBotID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"", "../x", `a\b`, "a/b", "-lead", "a b", strings.Repeat("a", 65), "con", "CON", "Nul", "aux", "prn", "com1", "COM9", "lpt1", "Lpt9"} {
		if err := checkBotID(id); err == nil {
			t.Errorf("checkBotID(%q) = nil, want an error", id)
		}
	}
}

func TestPaths(t *testing.T) {
	root := filepath.Join("data", "sandbox")
	p := Paths{Root: root}
	for _, tc := range []struct{ got, want string }{
		{p.Runtime("q1"), filepath.Join(root, "runtime", "q1")},
		{p.Image("i1"), filepath.Join(root, "images", "i1")},
		{p.Overlay("b1"), filepath.Join(root, "bots", "b1.qcow2")},
		{p.Log("data", "b1"), filepath.Join("data", "logs", "sandbox-b1.log")},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}

func TestSocketIsShortAndStablePerBot(t *testing.T) {
	p := Paths{Root: `C:\Users\averagename\AppData\Local\Orole\sandbox`}
	a := p.Socket("0f8fad5b-d9cb-469f-a165-70867728950e")
	if a != p.Socket("0f8fad5b-d9cb-469f-a165-70867728950e") {
		t.Fatal("the socket path changes between calls")
	}
	if a == p.Socket("7c9e6679-7425-40de-944b-e07fc1f90ae7") {
		t.Fatal("two bots share a socket")
	}
	if filepath.Dir(a) != filepath.Join(p.Root, "run") || filepath.Ext(a) != ".sock" || len(filepath.Base(a)) != 16+len(".sock") {
		t.Fatalf("socket %q", a)
	}
	if err := CheckSocketPath(a); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSocketPathRejectsLongDataDirs(t *testing.T) {
	long := Paths{Root: `C:\` + strings.Repeat("d", 100) + `\sandbox`}
	err := CheckSocketPath(long.Socket("bot"))
	if err == nil || !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), long.Root) {
		t.Fatalf("err = %v", err)
	}
}
