// orole-sandbox-setup turns on the Windows Hypervisor Platform feature for Orole's bot computer. The
// daemon starts it once through a UAC prompt; it does nothing else and accepts no other input.
package main

import "github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"

func dismArgs() []string {
	return []string{"/Online", "/Enable-Feature", "/FeatureName:HypervisorPlatform", "/All", "/NoRestart", "/Quiet"}
}

// run is the whole program: exactly one argument, elevation required, DISM's exit code mapped.
func run(args []string, elevated bool, dism func(args []string) (int, error)) int {
	if len(args) != 1 || args[0] != setupcode.EnableWHPArg {
		return setupcode.Usage
	}
	if !elevated {
		return setupcode.NotElevated
	}
	code, err := dism(dismArgs())
	switch {
	case err != nil:
		return setupcode.Failed
	case code == 0:
		return setupcode.OK
	case code == 3010:
		return setupcode.RebootRequired
	default:
		return setupcode.Failed
	}
}
