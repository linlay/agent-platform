Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$rootDir = (Resolve-Path (Join-Path $scriptDir "../..")).Path
$releaseDir = Join-Path $rootDir "release-local"

$stopLocal = Join-Path $releaseDir "stop-local.ps1"
if (Test-Path $stopLocal) {
    & $stopLocal @args
    exit $LASTEXITCODE
}

$stopScript = Join-Path $releaseDir "stop.ps1"
if (Test-Path $stopScript) {
    & $stopScript @args
    exit $LASTEXITCODE
}

Write-Error "[stop-local] release-local stop script not found."
Write-Error "[stop-local] run .\release-scripts\windows\package-local.ps1 first to generate release-local artifacts."
exit 1
