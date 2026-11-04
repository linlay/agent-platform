. (Join-Path $PSScriptRoot 'scripts/program-common.ps1')
Read-DictationArgs $args
if (Get-DictationProcess) { Write-Host 'agent-dictation already running'; exit 0 }
$config = Join-Path $ConfigDir 'dictation.yml'
if (!(Test-Path -LiteralPath $Binary) -or !(Test-Path -LiteralPath $config)) { throw 'Deploy agent-dictation first' }
$envFile = Join-Path $ConfigDir '.env'
if (Test-Path -LiteralPath $envFile) {
    foreach ($line in Get-Content -LiteralPath $envFile) {
        if ($line -match '^([A-Z_]+)=(.*)$') { [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2].Trim().Trim('"').Trim("'"), 'Process') }
    }
}
if (!$DictationPort) { $DictationPort = $env:DICTATION_PORT }
if (!$DictationPort) { $DictationPort = '11964' }
$env:DICTATION_LISTEN = "127.0.0.1:$DictationPort"
if ($TokenFile) { $env:DICTATION_SSO_ID_TOKEN_FILE = $TokenFile }
if (Get-NetTCPConnection -LocalPort $DictationPort -State Listen -ErrorAction SilentlyContinue) { throw 'Dictation port is already occupied' }
if (Get-CimInstance Win32_Process -Filter "Name='agent-dictation.exe'" | Where-Object { $_.ExecutablePath -eq $Binary -or ($_.CommandLine -and $_.CommandLine.Contains($config)) }) { throw 'Existing dictation instance has no matching PID record' }
New-Item -ItemType Directory -Path $StateDir, $LogDir -Force | Out-Null
if (!$Daemon) { & $Binary --config $config; exit $LASTEXITCODE }
$process = Start-Process -FilePath $Binary -ArgumentList @('--config', ('"' + $config + '"')) -WorkingDirectory $ConfigDir -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $LogDir 'agent-dictation.log') -RedirectStandardError (Join-Path $LogDir 'agent-dictation.stderr.log')
Write-DictationFile $PidFile ([string]$process.Id)
Start-Sleep -Milliseconds 200
if ($process.HasExited) { Remove-Item -LiteralPath $PidFile; throw 'agent-dictation failed; see its log' }
