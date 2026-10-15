<#
.SYNOPSIS
  Builds build\orole-sandbox-setup.exe and then build\orole-daemon.exe with the helper's SHA-256 embedded, for
  `go tool mygo dev` (mygo.json devCommand). scripts/build.ps1 does the same for release builds.
.DESCRIPTION
  The daemon starts the setup helper elevated (one UAC prompt) and refuses any file whose SHA-256 differs from
  the one embedded here (sandbox.SetupHelperSHA256), because the folder it sits in is writable by the user.
#>
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)
$null = New-Item -ItemType Directory -Force build
go build -ldflags=-H=windowsgui -o build\orole-sandbox-setup.exe ./cmd/orole-sandbox-setup
if ($LASTEXITCODE) { throw "sandbox setup helper build failed" }
$helperSHA = (Get-FileHash build\orole-sandbox-setup.exe -Algorithm SHA256).Hash.ToLowerInvariant()
go build -ldflags "-X github.com/rayorole/orole-os-desktop/internal/sandbox.SetupHelperSHA256=$helperSHA" -o build\orole-daemon.exe ./cmd/daemon
if ($LASTEXITCODE) { throw "daemon build failed" }
