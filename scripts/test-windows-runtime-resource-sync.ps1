#Requires -Version 5.1
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'release-assets/program/windows/program-common.ps1')

$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("agent-platform-resource-sync-test-" + [guid]::NewGuid().ToString('N'))
try {
  New-Item -ItemType Directory -Force -Path $testRoot | Out-Null
  $backend = Join-Path $testRoot 'resource-sync.cmd'
  [System.IO.File]::WriteAllText($backend, "@echo off`r`necho first warning 1>&2`r`necho second warning 1>&2`r`necho sync complete`r`nexit /b 0`r`n")
  $Script:BackendBin = $backend
  $Script:DeployRuntimeResourceSource = $backend

  $output = @(Invoke-ProgramRuntimeResourceSync)
  if (@($output | Where-Object { $_ -is [System.Management.Automation.ErrorRecord] }).Count -ne 0) {
    throw 'successful resource sync forwarded native stderr as an error'
  }
  foreach ($expected in @('first warning', 'second warning', 'sync complete')) {
    if (-not (($output -join "`n").Contains($expected))) {
      throw "resource sync output missing: $expected"
    }
  }
  [System.IO.File]::WriteAllText($backend, "@echo off`r`necho sync failed 1>&2`r`nexit /b 7`r`n")
  $failed = $false
  try {
    Invoke-ProgramRuntimeResourceSync | Out-Null
  } catch {
    $failed = $_.Exception.Message.Contains('exit code 7')
  }
  if (-not $failed) { throw 'failed resource sync did not preserve its native exit code' }
  Write-Host '[test] successful resource sync preserves multi-line native stderr as diagnostics'
} finally {
  Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}
