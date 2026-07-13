#Requires -Version 5.1
param(
    [switch]$All,
    [string[]]$Target,
    [string]$BuiltinsRoot
)

$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$bash = Get-Command bash -ErrorAction SilentlyContinue
if (-not $bash) {
    throw "bash is required to run scripts/sync-local-builtins.sh"
}
$bashPath = $bash.Source

$invokeArgs = @()
if ($All -or -not $Target) {
    $invokeArgs += "--all"
} else {
    foreach ($item in $Target) {
        $invokeArgs += @("--target", $item)
    }
}
if ($BuiltinsRoot) {
    if (-not [IO.Path]::IsPathRooted($BuiltinsRoot)) {
        throw "BuiltinsRoot must be an absolute path"
    }
    $invokeArgs += @("--builtins-root", $BuiltinsRoot)
}

& $bashPath (Join-Path $scriptDir "sync-local-builtins.sh") @invokeArgs
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
