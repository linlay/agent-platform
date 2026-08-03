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
$ChatResourceTicketSecret = $null
if ($Script:DeployDesktopConfigReset) {
  Reset-DesktopProgramConfig $Script:DeployDesktopConfigBackupDir
  $ChatResourceTicketSecret = Get-ProgramEnvLiteralValue (Join-Path $Script:DeployDesktopConfigBackupDir '.env') 'AP_CHAT_RESOURCE_TICKET_SECRET'
}
Initialize-ProgramDeployConfig
if ($Script:DeployDesktopConfigReset -and -not [string]::IsNullOrWhiteSpace($ChatResourceTicketSecret)) {
  Set-ProgramEnvValue $Script:EnvFile 'AP_CHAT_RESOURCE_TICKET_SECRET' $ChatResourceTicketSecret
}
if ($Script:DeployDesktopConfigReset) {
  Protect-ProgramConfigTree $Script:ConfigRoot
}
Write-Host ("[program-deploy] config initialized: {0}" -f $Script:ConfigDir)
if ($Script:DeployDesktopConfigReset) {
  Write-Host ("[program-deploy] Desktop config rebuilt: {0} -> {1}" -f $Script:DeployDesktopVersionFrom, $Script:DeployDesktopVersionTo)
}
Write-Host '[program-deploy] deploy complete'
