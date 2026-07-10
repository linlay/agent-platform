#Requires -Version 5.1
param(
    [Parameter(Mandatory = $true)]
    [string]$OutputDir,
    [Parameter(Mandatory = $true)]
    [string]$TargetOS,
    [Parameter(Mandatory = $true)]
    [string]$TargetArch
)

$ErrorActionPreference = "Stop"
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$REPO_ROOT = Split-Path -Parent $SCRIPT_DIR

Push-Location $REPO_ROOT
try {
    & go run ./cmd/stage-builtins --repo-root $REPO_ROOT --output $OutputDir --os $TargetOS --arch $TargetArch
    if ($LASTEXITCODE -ne 0) {
        Write-Error "stage builtins failed for $TargetOS/$TargetArch"
    }
} finally {
    Pop-Location
}
