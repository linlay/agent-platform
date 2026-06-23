$ErrorActionPreference = 'Stop'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $ScriptDir 'scripts/program-common.ps1')

Set-Location $ScriptDir
Set-ProgramLayoutArgs $args
Write-Host '[program-deploy] validating bundle'
Test-ProgramBundle
Write-Host '[program-deploy] bundle validated'
Write-Host ("[program-deploy] backend binary: {0}" -f $Script:BackendBin)
Write-Host ("[program-deploy] initializing config under {0}" -f $Script:ConfigDir)
Initialize-ProgramConfig
Write-Host ("[program-deploy] config initialized: {0}" -f $Script:ConfigDir)
Write-Host ("[program-deploy] loading env: {0}" -f $Script:EnvFile)
Import-ProgramEnv
Write-Host '[program-deploy] env loaded'
Write-Host ("[program-deploy] preparing runtime dirs under {0} and {1}" -f $Script:RuntimeRoot, $Script:RunDir)
Initialize-ProgramRuntime
Write-Host ("[program-deploy] runtime directories prepared under {0} and {1}" -f $Script:RuntimeRoot, $Script:RunDir)
Write-Host '[program-deploy] deploy complete'
