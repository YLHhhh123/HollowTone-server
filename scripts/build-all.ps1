# Cross-platform release builder for HollowTone Server (Windows / PowerShell).
#
# Produces static binaries in ./dist with a SHA256SUMS.txt, embedding the
# version, commit and build time so a running deployment can be identified via
# GET /version. This is what the "build the server version" step in the release
# flow runs; it is safe to run repeatedly.
#
#   pwsh -File scripts/build-all.ps1                 # version from git describe
#   pwsh -File scripts/build-all.ps1 -Version v0.2.0 # explicit version
#   pwsh -File scripts/build-all.ps1 -Targets linux/amd64,darwin/arm64
[CmdletBinding()]
param(
    [string]$Version = "",
    [string]$Targets = "linux/amd64,linux/arm64,windows/amd64",
    [string]$OutDir = "dist",
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$module = "github.com/YLHhhh123/HollowTone-server"

# --- resolve build identity -------------------------------------------------
if (-not $Version) {
    try { $Version = (git describe --tags --always --dirty 2>$null) } catch { $Version = "" }
    if (-not $Version) { $Version = "dev" }
}
$commit = ""
try { $commit = (git rev-parse HEAD 2>$null) } catch { $commit = "none" }
if (-not $commit) { $commit = "none" }
$shortCommit = if ($commit.Length -ge 12) { $commit.Substring(0, 12) } else { $commit }
$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

Write-Host "== HollowTone Server build =="
Write-Host "  version : $Version"
Write-Host "  commit  : $shortCommit"
Write-Host "  built   : $buildTime"

# --- quality gates ----------------------------------------------------------
if (-not $SkipTests) {
    Write-Host "`n[1/4] go test ./..."
    go test ./... | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "go test failed" }
    Write-Host "      ok"

    Write-Host "[2/4] go vet ./..."
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet failed" }
    Write-Host "      ok"
}
else {
    Write-Host "`n[1/4] tests skipped (-SkipTests)"
    Write-Host "[2/4] tests skipped (-SkipTests)"
}

# --- build ------------------------------------------------------------------
$ldflags = @(
    "-s", "-w",
    "-X", "$module/internal/version.Version=$Version",
    "-X", "$module/internal/version.Commit=$shortCommit",
    "-X", "$module/internal/version.BuildTime=$buildTime"
) -join " "

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
Get-ChildItem $OutDir -File -Filter "hollowtone-server*" -ErrorAction SilentlyContinue | Remove-Item -Force

$env:CGO_ENABLED = "0"
$built = @()
Write-Host "`n[3/4] building targets"
foreach ($t in $Targets.Split(",")) {
    $t = $t.Trim()
    if (-not $t) { continue }
    $parts = $t.Split("/")
    $goos = $parts[0]; $goarch = $parts[1]
    $name = "hollowtone-server-$goos-$goarch"
    if ($goos -eq "windows") { $name += ".exe" }

    $env:GOOS = $goos
    $env:GOARCH = $goarch
    Write-Host "      $goos/$goarch -> $OutDir/$name"
    go build -trimpath -ldflags $ldflags -o (Join-Path $OutDir $name) ./cmd/hollowtone-server
    if ($LASTEXITCODE -ne 0) { throw "build failed for $goos/$goarch" }
    $built += $name
}

# --- checksums --------------------------------------------------------------
Write-Host "`n[4/4] SHA256SUMS.txt"
$lines = foreach ($n in $built) {
    $h = (Get-FileHash -Algorithm SHA256 (Join-Path $OutDir $n)).Hash.ToLower()
    "$h  $n"
}
$lines | Set-Content -Encoding ascii (Join-Path $OutDir "SHA256SUMS.txt")

# --- dist/README ------------------------------------------------------------
$rows = foreach ($n in $built) {
    $size = [math]::Round((Get-Item (Join-Path $OutDir $n)).Length / 1MB, 1)
    "| ``$n`` | $size MB |"
}
@"
# HollowTone Server $Version

Built $buildTime from commit $shortCommit.

| Binary | Size |
|---|---|
$($rows -join "`n")

Verify before running:

``````bash
sha256sum -c SHA256SUMS.txt
``````

Run it:

``````bash
chmod +x hollowtone-server-linux-amd64
HT_DATA_DIR=./data HT_ADDR=:8080 ./hollowtone-server-linux-amd64
``````

Confirm which build is live:

``````bash
curl -s http://127.0.0.1:8080/version
``````
"@ | Set-Content -Encoding utf8 (Join-Path $OutDir "README.md")

Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
Write-Host "`nDone. Artifacts in $OutDir"
Get-ChildItem $OutDir -File | Select-Object Name, Length | Format-Table -AutoSize
