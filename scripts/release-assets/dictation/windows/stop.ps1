. (Join-Path $PSScriptRoot 'scripts/program-common.ps1')
Read-DictationArgs $args
$process = Get-DictationProcess
if ($process) { Stop-Process -Id $process.ProcessId; Wait-Process -Id $process.ProcessId -Timeout 10 -ErrorAction SilentlyContinue }
Remove-Item -LiteralPath $PidFile -ErrorAction SilentlyContinue
