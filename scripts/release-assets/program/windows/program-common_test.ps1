#Requires -Version 5.1
$ErrorActionPreference = 'Stop'

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $ScriptDir 'program-common.ps1')

function Assert-ProgramAcl([string]$Path, [string[]]$RequiredSids, [bool]$RequireProtected = $true) {
  $acl = Get-Acl -LiteralPath $Path
  if ($RequireProtected -and -not $acl.AreAccessRulesProtected) { throw "expected protected ACL: $Path" }
  foreach ($requiredSid in $RequiredSids) {
    $rule = $acl.Access | Where-Object {
      $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value -eq $requiredSid -and
        $_.AccessControlType -eq [System.Security.AccessControl.AccessControlType]::Allow -and
        ($_.FileSystemRights -band [System.Security.AccessControl.FileSystemRights]::FullControl) -eq
          [System.Security.AccessControl.FileSystemRights]::FullControl
    } | Select-Object -First 1
    if ($null -eq $rule) { throw "expected FullControl for $requiredSid on $Path" }
  }
}

$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("agent-platform acl test {0}" -f [Guid]::NewGuid().ToString('N'))
$nestedDir = Join-Path $testRoot 'configs\nested'
$envFile = Join-Path $testRoot '.env'
$nestedFile = Join-Path $nestedDir 'config.yml'
$futureFile = Join-Path $nestedDir 'runtime-created.yml'
$currentSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$requiredSids = @($currentSid, 'S-1-5-18')

try {
  New-Item -ItemType Directory -Force -Path $nestedDir | Out-Null
  [System.IO.File]::WriteAllText($envFile, "AP_RUNTIME_DIR=C:\runtime`n")
  [System.IO.File]::WriteAllText($nestedFile, "enabled: true`n")
  Protect-ProgramConfigTree $testRoot
  [System.IO.File]::ReadAllText($envFile) | Out-Null
  [System.IO.File]::ReadAllText($nestedFile) | Out-Null
  foreach ($item in @((Get-Item -LiteralPath $testRoot)) + @(Get-ChildItem -LiteralPath $testRoot -Recurse -Force)) {
    Assert-ProgramAcl $item.FullName $requiredSids
  }
  [System.IO.File]::WriteAllText($futureFile, "created: true`n")
  [System.IO.File]::ReadAllText($futureFile) | Out-Null
  Assert-ProgramAcl $futureFile $requiredSids $false
  Write-Host '[test] agent-platform config ACLs remain readable'
} finally {
  if (Test-Path -LiteralPath $testRoot) {
    & icacls.exe $testRoot '/grant' ("*{0}:F" -f $currentSid) '/T' '/C' | Out-Null
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
  }
}

$processTestRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("agent-platform process arg test {0}" -f [Guid]::NewGuid().ToString('N'))
$fakeBackend = Join-Path $processTestRoot 'fake-agent-platform.exe'
$capturedArgsFile = Join-Path $processTestRoot 'captured-args.txt'
$configRoot = Join-Path $processTestRoot 'CuteJ Data\.cutej\.desktop\config\services\agent-platform'
$runDir = Join-Path $processTestRoot 'run'
$logDir = Join-Path $processTestRoot 'logs'
$previousCapturePath = $env:AGENT_PLATFORM_TEST_CAPTURE_ARGS

try {
  New-Item -ItemType Directory -Force -Path $configRoot, $runDir, $logDir | Out-Null
  $fakeBackendSource = @'
using System;
using System.IO;
using System.Threading;

public static class FakeAgentPlatformBackend
{
    public static int Main(string[] args)
    {
        File.WriteAllLines(Environment.GetEnvironmentVariable("AGENT_PLATFORM_TEST_CAPTURE_ARGS"), args);
        Thread.Sleep(5000);
        return 0;
    }
}
'@
  Add-Type -TypeDefinition $fakeBackendSource -Language CSharp -OutputAssembly $fakeBackend -OutputType ConsoleApplication

  $Script:BackendBin = $fakeBackend
  $Script:BundleRoot = $processTestRoot
  $Script:ConfigRoot = $configRoot
  $Script:RunDir = $runDir
  $Script:LogDir = $logDir
  $Script:ProgramPort = '17078'
  Update-ProgramPaths
  $env:AGENT_PLATFORM_TEST_CAPTURE_ARGS = $capturedArgsFile

  Start-ProgramBackend -Daemon

  $deadline = [DateTime]::UtcNow.AddSeconds(3)
  while (-not (Test-Path -LiteralPath $capturedArgsFile -PathType Leaf) -and [DateTime]::UtcNow -lt $deadline) {
    Start-Sleep -Milliseconds 100
  }
  if (-not (Test-Path -LiteralPath $capturedArgsFile -PathType Leaf)) {
    throw 'fake backend did not capture daemon arguments'
  }

  $capturedArgs = @(Get-Content -LiteralPath $capturedArgsFile)
  $expectedArgs = @('--config-dir', $configRoot, '--port', '17078')
  if ($capturedArgs.Count -ne $expectedArgs.Count) {
    throw "expected $($expectedArgs.Count) daemon arguments, got $($capturedArgs.Count): $($capturedArgs -join ' | ')"
  }
  for ($i = 0; $i -lt $expectedArgs.Count; $i++) {
    if ($capturedArgs[$i] -cne $expectedArgs[$i]) {
      throw "daemon argument $i mismatch: expected '$($expectedArgs[$i])', got '$($capturedArgs[$i])'"
    }
  }
  Write-Host '[test] agent-platform daemon preserves spaced config paths'
} finally {
  if (Test-Path -LiteralPath $Script:PidFile -PathType Leaf) {
    $testPid = (Get-Content -LiteralPath $Script:PidFile -Raw -ErrorAction SilentlyContinue).Trim()
    if ($testPid -match '^\d+$') {
      Stop-Process -Id ([int]$testPid) -Force -ErrorAction SilentlyContinue
    }
  }
  $env:AGENT_PLATFORM_TEST_CAPTURE_ARGS = $previousCapturePath
  if (Test-Path -LiteralPath $processTestRoot) {
    Remove-Item -LiteralPath $processTestRoot -Recurse -Force -ErrorAction SilentlyContinue
  }
}
