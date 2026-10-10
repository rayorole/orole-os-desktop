<#
.SYNOPSIS
  Builds orole-daemon.exe into build\ (a mygo resource) and then the packaged app + NSIS installer.
.PARAMETER DaemonOnly
  Build only build\orole-daemon.exe and build\orole.exe (the CLI), skipping the frontend and installer.
.PARAMETER Version
  Version stamped into the binaries (default: version in mygo.json).
.PARAMETER Upload
  Run `mygo build -upload`: signs the update files and uploads them, with the installer, to a draft GitHub
  release of the mygo.json version. Needs GH_TOKEN and MYGO_UPDATER_PRIVATE_KEY in the environment.
#>
param([switch]$DaemonOnly, [string]$Version = "", [switch]$Upload)
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)

# Fail before building anything when the upload could not work.
if ($Upload) {
  if ($DaemonOnly) { throw "-Upload and -DaemonOnly cannot be combined: -DaemonOnly builds no installer to upload" }
  $missing = @("GH_TOKEN", "MYGO_UPDATER_PRIVATE_KEY") | Where-Object { -not [Environment]::GetEnvironmentVariable($_) }
  if ($missing) { throw "-Upload needs these environment variables, which are not set: $($missing -join ', ')" }
}

if (-not $Version) { $Version = (Get-Content mygo.json -Raw | ConvertFrom-Json).version }
$null = New-Item -ItemType Directory -Force build

# -H windowsgui is NOT used for the daemon: it must stay a console-subsystem binary so
# `orole-daemon -stderr` works; it is launched with CREATE_NO_WINDOW by the app and the CLI.
$ld = "-s -w -X github.com/rayorole/orole-os-desktop/internal/daemon.Version=$Version"
go build -trimpath -ldflags $ld -o build\orole-daemon.exe ./cmd/daemon
if ($LASTEXITCODE) { throw "daemon build failed" }
go build -trimpath -ldflags "-s -w -X main.version=$Version" -o build\orole.exe ./cmd/orole-cli
if ($LASTEXITCODE) { throw "cli build failed" }
Write-Host "built build\orole-daemon.exe and build\orole.exe ($Version)"
if ($DaemonOnly) { return }

# mygo runs buildCommand (frontend build), compiles cmd/desktop, bundles the `resources`
# listed in mygo.json (build\orole-daemon.exe) and produces the NSIS installer. With -Upload it also
# signs the updates and uploads everything to the draft release.
if ($Upload) { go tool mygo build -upload } else { go tool mygo build }
if ($LASTEXITCODE) { throw "mygo build failed" }
Get-ChildItem build\windows-amd64 -ErrorAction SilentlyContinue | Format-Table Name, Length -AutoSize
