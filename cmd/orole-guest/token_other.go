//go:build !linux

package main

import "io/fs"

// Off Linux there is no QEMU fw_cfg; readToken only looks at the kernel command line (and fails).
const fwCfgTokenPath = ""

func fwCfgToken(string) (string, error) { return "", fs.ErrNotExist }
