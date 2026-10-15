# Packaging

```powershell
./scripts/build.ps1            # or: make build
```

Steps (`scripts/build.ps1`):

1. `go build -trimpath -ldflags "-s -w -X github.com/rayorole/orole-os-desktop.Version=<version>" -o build\orole-daemon.exe ./cmd/daemon`
   (all three binaries read their version from `mygo.json`, embedded by the root `version.go`; the
   `-X` override only matters when `build.ps1 -Version` differs from it)
2. `go build ... -o build\orole.exe ./cmd/orole-cli` (the CLI; not part of the installer today)
3. `go tool mygo build`: runs `buildCommand` (`bun run --cwd frontend build`), compiles `cmd/desktop` for
   windows/amd64 with the icon from `resources/icon.png`, copies `resources` from `mygo.json`
   (`build/orole-daemon.exe`, `build/orole-sandbox-setup.exe`), and builds the NSIS installer (NSIS 3.13
   downloaded by mygo on first use).
   - `build/orole-sandbox-setup.exe` (from `cmd/orole-sandbox-setup`, built `-H windowsgui`): the only binary
     Orole ever runs elevated. The daemon starts it with a UAC prompt to enable Windows Hypervisor Platform for
     the bot computer. It sits next to `orole-daemon.exe` in the installed resources folder.

`-DaemonOnly` stops after step 2 (which also builds the setup helper) (needed by `scripts/measure-daemon.ps1`).

The daemon must exist before step 3 or the resource copy fails; that ordering is why `go tool mygo build` alone is
not enough on a clean checkout.

## Output (`build/windows-amd64/`)

| File | Measured size (v0.1.0 integrated build, 2026-10-08) |
|---|---|
| `Orole OS.exe` | 21,684,736 bytes (~20.7 MiB), no console window |
| `orole-daemon.exe` | 18,246,656 bytes (~17.4 MiB), copied next to the app |
| `Orole OS Setup 0.1.0.exe` | 12,420,523 bytes (~11.8 MiB), unsigned NSIS installer |

## Installer behaviour (verified: `/S`, `/D=`, uninstaller, and cold/warm OS URL association)

Per-user install to `%LOCALAPPDATA%\Programs\Orole OS`, no admin; Start menu shortcut; uninstaller listed in
Settings; registers the `orole://` URL scheme. `/S` installs silently (no shortcuts); `/D=<dir>` (last argument,
unquoted) chooses the directory.

`scripts/smoke-installer.ps1` installs silently to a temp dir with an isolated `OROLE_HOME`, launches the app,
waits for the daemon, checks its path and answers over the pipe via `build\orole.exe`, prints diagnostics,
closes the app, stops the daemon and uninstalls (`Uninstall.exe /S _?=<dir>`).

## Icon

`go run ./scripts/genicon` redraws `resources/icon.png` (1024x1024, the packaging icon MyGo converts into the
exe resource) and `cmd/desktop/window-icon.png` (256x256, embedded and applied with `Window.SetIcon`). Original
artwork: a lighthouse beam over a horizon line on dark navy.

## Release build

`./scripts/build.ps1 -Upload` (CI only; needs `GH_TOKEN` and `MYGO_UPDATER_PRIVATE_KEY`) runs the same steps but ends
with `go tool mygo build -upload`, which signs the update manifest and uploads to a draft release. See
[releasing.md](releasing.md).

## Not configured

Authenticode code signing (`windows.certificate` / `signCommand`) is not set up: installers are unsigned (SmartScreen
will warn). Update-channel signing is configured; see `updates.md`.
