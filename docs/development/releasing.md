# Releasing

A release is a `desktop-vX.Y.Z` tag. `.github/workflows/release.yml` verifies it, creates a **draft** GitHub release
and uploads the signed build. A human reviews and publishes the draft; installed apps then self-update (see
[updates.md](updates.md), [packaging.md](packaging.md)). Nothing is published automatically.

## Cut a release

1. Bump `version` in `mygo.json` (semver; a prerelease suffix such as `0.2.0-rc.1` is allowed). This is the only
   version source; the daemon version is injected from it at build time.
2. Add a `## X.Y.Z - YYYY-MM-DD` section to `CHANGELOG.md`. MyGo uses that section as the release notes, and the
   release check fails if it is missing.
3. Merge the change to `main` through a normal pull request.
4. Tag the merge commit and push the tag:

   ```powershell
   git tag desktop-vX.Y.Z
   git push origin desktop-vX.Y.Z
   ```

   The tag must equal `desktop-v` + the `mygo.json` version exactly.

Check locally before tagging: `./scripts/check-release.ps1 -Tag desktop-vX.Y.Z` validates the tag format, that the
tag version equals `mygo.json`, and that `CHANGELOG.md` has the section. It prints the version on success and exits
non-zero with a message otherwise; `-NotesOut <path>` also writes the section body to a file.

## What `release.yml` does

Runs on `desktop-v*` tags only, never on pull requests.

1. **Verify:** runs `scripts/check-release.ps1` (above) and the same checks as CI.
2. **Draft release:** creates the GitHub release for the tag as a draft, with the changelog section as notes
   (`-rc.N` versions are marked pre-release).
3. **Signed build and upload:** in the `release` environment (which holds `MYGO_UPDATER_PRIVATE_KEY`), runs
   `./scripts/build.ps1 -Upload`, i.e. `go tool mygo build -upload` with the workflow's `GITHUB_TOKEN` as `GH_TOKEN`.
   The script fails early with a message if either variable is missing. Uploaded assets: the NSIS installer, the
   per-platform update manifest (`update-windows-amd64.json`), the archive and binary deltas. The job then fails if
   the installer, archive or manifest is missing from the draft.

If a step fails, fix the cause, delete the draft release and the tag (`gh release delete desktop-vX.Y.Z --cleanup-tag`)
and tag again. A version that was never published can be re-tagged; a published version must not be reused.

## Review and publish

Open the draft (`gh release view desktop-vX.Y.Z --web`). Check the notes, that the installer and update manifest
assets are present, and download and install the installer on a clean machine. Then publish:

```powershell
gh release edit desktop-vX.Y.Z --draft=false --latest=false
```

`--latest=false` is required: `desktop-v*` releases must not become the repository's "latest" release.

## Prereleases

A version with a suffix (`0.2.0-rc.1`) is published as a GitHub pre-release. The updater only follows the newest
non-draft, non-prerelease release with the `desktop-v` prefix, so installed apps ignore release candidates. Install
them by hand to test.

## Key custody

- The update-signing private key is the `MYGO_UPDATER_PRIVATE_KEY` secret of the `release` GitHub Environment,
  restricted to `desktop-v*` tags, plus the maintainer's offline backup (password manager). It is never in the
  repository and never available to pull-request workflows. The public key lives in `mygo.json` `updates.publicKey`.
- **Losing the key strands installed apps:** they only accept updates signed by the key they embed.
- **Leaking the key** lets anyone ship code to every install. Treat it as an incident: rotate.
- **Rotation:** build a release carrying the *new* public key in `mygo.json`, signed with the *old* private key, and
  publish it so installs pick up the new key. Only after that, sign releases with the new key and retire the old one.
  If the old key is lost, rotation is impossible and users must reinstall manually.

## Rollback

Apps read the newest non-draft, non-prerelease release. To stop a bad release being offered, un-publish it
(`gh release edit desktop-vX.Y.Z --draft=true`) or delete it; apps then see the previous release as newest. That does
not downgrade anyone who already updated. The real fix is a new, higher version (`X.Y.Z+1`) through the normal flow.

## Consequences to know

- **The daemon stops on update.** Installing an update asks for native confirmation and ends the daemon, so
  daemon-owned terminals and tasks end; the app relaunches on the new version.
- **Unsigned installer (Authenticode):** the update signature protects the update channel only. The installer and
  executables are not code-signed until issue #110, so Windows SmartScreen warns on first download.
- **Public repository:** the anonymous update feed reads GitHub releases without a token, so
  `rayorole/orole-os-desktop` must be public for installed apps to see updates.
- **Not yet verified:** an end-to-end signed upgrade (install an old release, see it update from a published one)
  has not been exercised; that is issue #111.
