package main

import (
	"errors"
	"slices"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

func TestRunAcceptsOnlyTheOneCommand(t *testing.T) {
	never := func([]string) (int, error) { t.Fatal("DISM ran"); return 0, nil }
	for _, args := range [][]string{nil, {"enable-whp", "extra"}, {"/Online"}, {"ENABLE-WHP"}} {
		if got := run(args, true, never); got != setupcode.Usage {
			t.Errorf("%q: got %d", args, got)
		}
	}
}

func TestRunNeedsElevationBeforeDISM(t *testing.T) {
	never := func([]string) (int, error) { t.Fatal("DISM ran unelevated"); return 0, nil }
	if got := run([]string{setupcode.EnableWHPArg}, false, never); got != setupcode.NotElevated {
		t.Fatalf("got %d", got)
	}
}

func TestRunMapsDISMExitCodes(t *testing.T) {
	cases := []struct {
		code int
		err  error
		want int
	}{
		{0, nil, setupcode.OK}, {3010, nil, setupcode.RebootRequired}, {87, nil, setupcode.Failed}, {0, errors.New("no dism"), setupcode.Failed},
	}
	for _, c := range cases {
		var gotArgs []string
		got := run([]string{setupcode.EnableWHPArg}, true, func(a []string) (int, error) { gotArgs = a; return c.code, c.err })
		if got != c.want {
			t.Errorf("dism %d/%v: got %d want %d", c.code, c.err, got, c.want)
		}
		if !slices.Equal(gotArgs, []string{"/Online", "/Enable-Feature", "/FeatureName:HypervisorPlatform", "/All", "/NoRestart", "/Quiet"}) {
			t.Errorf("dism args %q", gotArgs)
		}
	}
}
