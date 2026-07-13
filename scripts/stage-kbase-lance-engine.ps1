#Requires -Version 5.1
param(
    [Parameter(Mandatory = $true)]
    [string]$OutputDir,
    [Parameter(Mandatory = $true)]
    [ValidateSet("darwin", "linux", "windows")]
    [string]$TargetOS,
    [Parameter(Mandatory = $true)]
    [ValidateSet("amd64", "arm64")]
    [string]$TargetArch,
    [string]$BuiltinsRoot
)

$ErrorActionPreference = "Stop"
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$REPO_ROOT = Split-Path -Parent $SCRIPT_DIR
if ($BuiltinsRoot -and -not [IO.Path]::IsPathRooted($BuiltinsRoot)) {
    throw "BuiltinsRoot must be an absolute path"
}

$goArgs = @(
    "run", "./cmd/stage-kbase-lance-engine",
    "--repo-root", $REPO_ROOT,
    "--output", $OutputDir,
    "--os", $TargetOS,
    "--arch", $TargetArch
)
if ($BuiltinsRoot) {
    $goArgs += @("--builtins-root", $BuiltinsRoot)
}

Push-Location $REPO_ROOT
try {
    & go @goArgs
    if ($LASTEXITCODE -ne 0) { throw "stage kbase-lance-engine failed for $TargetOS/$TargetArch" }
} finally {
    Pop-Location
}
