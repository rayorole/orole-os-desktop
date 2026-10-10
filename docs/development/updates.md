# Signed desktop updates

MyGo apps update themselves from **signed** releases: `mygo build` signs the build with a private key, the app
embeds the public key, `mygo.Updater` downloads a newer version, verifies the signature and replaces the app.
Windows per-user installs (`%LOCALAPPDATA%\Programs`) can self-update. Source: MyGo `docs/updates.md`.

**Current state:** `mygo.json` has an `updates` block (public key, `github: rayorole/orole-os-desktop`,
`tagPrefix: desktop-v`). The bound `app.Updates` service calls MyGo's `Enabled`, `Check`, and `Install` APIs. The
footer shows Update only after MyGo confirms a newer published release. Releases are cut with the tag-triggered
`release.yml`; see [releasing.md](releasing.md). Never generate or commit keys as part of ordinary work. The
private key lives in the `release` GitHub Environment secret `MYGO_UPDATER_PRIVATE_KEY` and the maintainer's backup.

## How to turn it on (maintainer, once)

1. Create the key pair on a trusted machine: `go tool mygo keygen`. It writes `mygo-update.key` (secret) and
   `mygo-update.pub` to `mygo/update-keys` in the user config dir (`-o` picks another). Keep the secret in a
   password manager and in CI secrets, never in the repository: losing it strands installed apps, leaking it lets
   anyone ship code to users.
2. Add to `mygo.json`:

   ```json
   "updates": {
     "publicKey": "<contents of mygo-update.pub>",
     "github": "rayorole/orole-os-desktop",
     "tagPrefix": "desktop-v"
   }
   ```

   (`url` + optional `s3` instead of `github` for your own host.) Releases tagged `desktop-v0.2.0` hold the
   manifests; apps read the newest non-draft, non-prerelease release with that prefix.
3. Build signed output with the key in the environment (never on a pull-request workflow):
   `$env:MYGO_UPDATER_PRIVATE_KEY = Get-Content mygo-update.key -Raw ; go tool mygo build`, or
   `./scripts/build.ps1 -Upload` (also needs `GH_TOKEN`) to upload to a draft release; CI does the latter. This
   writes per-platform `update-windows-amd64.json`, the archive and binary deltas next to the installer.
4. Release notes are the `## 0.2.0` section of `CHANGELOG.md`; `scripts/check-release.ps1` enforces that it exists.
5. Install the signed build. The shared update store checks on startup and at a
   bounded 30-minute cadence while visible; Settings also offers a manual check.
   Installation requires explicit frontend intent plus a Go-owned native confirmation
   naming the release and warning that daemon-owned tasks will end.

## Installation boundary and verification

`internal/app/updates.go` keeps only transient desktop update state. The feed,
version comparison, signature verification, download, replacement and relaunch
belong to MyGo. No caller-supplied URL or version selects executable content.
The desktop inhibits its normal daemon reconnect/start path during installation,
stops the authenticated daemon, and waits for process exit before replacing its
bundled executable. Windows tracks the original process handle, so pipe closure
alone cannot release the replacement barrier. A failed install resumes normal
daemon reconnection and surfaces an error; it never claims a restart succeeded.

The installed WebView2 smoke (before the `updates` block existed) showed version 0.1.0, the disabled configuration
message, and no footer Update button. A Windows subprocess regression exercised the live-process barrier and release
on actual process exit. **End-to-end signed download/replacement remains unverified** until a signed release is
published and an older install upgrades from it (issue #111). The installer and executables are not Authenticode
signed (issue #110).

## CI

`ci.yml` runs on pull requests only, with a read-only token and no secrets, and builds unsigned artefacts.
`release.yml` runs on `desktop-v*` tags only: it verifies the tag against `mygo.json` and `CHANGELOG.md`, repeats the CI
gates, then in the `release` environment (tag-restricted, holds the signing key) builds, signs and uploads to a
**draft** release. Publishing is a manual step (`gh release edit <tag> --draft=false --latest=false`).
