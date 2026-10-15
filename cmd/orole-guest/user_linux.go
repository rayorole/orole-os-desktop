//go:build linux

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
)

// botUser is the guest image's unprivileged user (guest/configuration.nix: uid 1000, home /home/bot).
const botUser = "bot"

// guestUser makes commands and terminals run as the bot user, through login shells so they get the system
// environment (PATH, nix-ld) from /etc/profile rather than the service's minimal one.
func guestUser(cfg *guestagent.Config) error {
	id, home, err := lookupIdentity(botUser)
	if err != nil {
		return err
	}
	cfg.RunAs, cfg.Home = id, home
	cfg.Shell = []string{"/bin/sh", "-lc"}
	cfg.LoginShell = []string{"/bin/sh", "-l"}
	if _, err := os.Stat(nixosBash); err == nil {
		cfg.LoginShell = []string{nixosBash, "-l"}
	}
	return nil
}

// nixosBash is bash on the NixOS guest image; other guests (the Alpine real-boot test) get /bin/sh.
const nixosBash = "/run/current-system/sw/bin/bash"

func lookupIdentity(name string) (*guestagent.Identity, string, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return nil, "", fmt.Errorf("guest user %s: %w", name, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, "", fmt.Errorf("guest user %s: uid %q: %w", name, u.Uid, err)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, "", fmt.Errorf("guest user %s: gid %q: %w", name, u.Gid, err)
	}
	id := &guestagent.Identity{Name: u.Username, UID: uint32(uid), GID: uint32(gid)}
	gids, err := u.GroupIds()
	if err != nil {
		return nil, "", fmt.Errorf("guest user %s: groups: %w", name, err)
	}
	for _, g := range gids {
		n, err := strconv.ParseUint(g, 10, 32)
		if err != nil {
			return nil, "", fmt.Errorf("guest user %s: group %q: %w", name, g, err)
		}
		id.Groups = append(id.Groups, uint32(n))
	}
	return id, u.HomeDir, nil
}
