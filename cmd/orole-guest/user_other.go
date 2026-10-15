//go:build !linux

package main

import "github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"

func guestUser(*guestagent.Config) error { return errNotGuest }
