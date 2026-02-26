Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$rootDir = (Resolve-Path (Join-Path $scriptDir "../..")).Path
$releaseDir = Join-Path $rootDir "release-local"

$startLocal = Join-Path $releaseDir "start-local.ps1"
if (Test-Path $startLocal) {
    & $startLocal @args
    exit $LASTEXITCODE
}

$startScript = Join-Path $releaseDir "start.ps1"
if (Test-Path $startScript) {
    & $startScript @args
    exit $LASTEXITCODE
}

Write-Error "[start-local] release-local start script not found."
Write-Error "[start-local] run .\release-scripts\windows\package-local.ps1 first to generate release-local artifacts."
exit 1
