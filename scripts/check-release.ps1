<#
.SYNOPSIS
  Validates a release tag against mygo.json and CHANGELOG.md. Exits non-zero on any mismatch.
.DESCRIPTION
  The tag must be desktop-v<version> where <version> is semver (optional -prerelease suffix) and equals
  `version` in mygo.json, and CHANGELOG.md must have a `## <version>` section (optionally followed by
  ` - date`, or the Keep a Changelog `## [<version>] - date` form). Prints the version on success.
.PARAMETER Tag
  The tag being released, e.g. desktop-v0.1.0.
.PARAMETER NotesOut
  Optional path; the body of the version's changelog section is written there (UTF-8, no BOM).
#>
param(
  [Parameter(Mandatory)][string]$Tag,
  [string]$NotesOut = ""
)
$ErrorActionPreference = "Stop"
if ($NotesOut) { $NotesOut = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($NotesOut) }
Set-Location (Split-Path $PSScriptRoot -Parent)

function Fail([string]$Message) {
  [Console]::Error.WriteLine("check-release: $Message")
  exit 1
}

if ($Tag -notmatch '^desktop-v(\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?)$') {
  Fail "tag '$Tag' is not of the form desktop-vX.Y.Z or desktop-vX.Y.Z-suffix"
}
$tagVersion = $Matches[1]

$mygoVersion = (Get-Content mygo.json -Raw | ConvertFrom-Json).version
if (-not $mygoVersion) { Fail "mygo.json has no version" }
if ($tagVersion -ne $mygoVersion) {
  Fail "tag '$Tag' is version $tagVersion but mygo.json version is $mygoVersion"
}

if (-not (Test-Path CHANGELOG.md)) { Fail "CHANGELOG.md not found" }
$heading = '^##\s+\[?' + [regex]::Escape($tagVersion) + '\]?(\s+-\s+.*)?\s*$'
$notes = [System.Collections.Generic.List[string]]::new()
$found = $false
foreach ($line in (Get-Content CHANGELOG.md -Encoding UTF8)) {
  if ($found) {
    if ($line -match '^##\s') { break }
    $notes.Add($line)
  } elseif ($line -match $heading) {
    $found = $true
  }
}
if (-not $found) { Fail "CHANGELOG.md has no '## $tagVersion' section" }

if ($NotesOut) {
  $body = ($notes -join "`n").Trim() + "`n"
  [System.IO.File]::WriteAllText($NotesOut, $body, [System.Text.UTF8Encoding]::new($false))
}
Write-Output $tagVersion
