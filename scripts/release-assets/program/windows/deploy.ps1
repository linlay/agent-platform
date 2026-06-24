$ErrorActionPreference = 'Stop'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $ScriptDir 'scripts/program-common.ps1')

Set-Location $ScriptDir
Set-ProgramDeployArgs $args
Write-Host '[program-deploy] validating bundle'
Test-ProgramBundle
Write-Host '[program-deploy] bundle validated'
Write-Host ("[program-deploy] backend binary: {0}" -f $Script:BackendBin)
Write-Host ("[program-deploy] initializing config under {0}" -f $Script:ConfigDir)
Initialize-ProgramDeployConfig
Write-Host ("[program-deploy] config initialized: {0}" -f $Script:ConfigDir)
Write-Host '[program-deploy] deploy complete'
