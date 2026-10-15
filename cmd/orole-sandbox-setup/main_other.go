//go:build !windows

package main

import (
	"fmt"
	"os"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

func main() {
	fmt.Fprintln(os.Stderr, "orole-sandbox-setup only runs on Windows")
	os.Exit(setupcode.Failed)
}
