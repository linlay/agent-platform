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
$identityFile = Join-Path $processTestRoot 'CuteJ Data\.cutej\.desktop\state\desktop\sso-access-token.txt'
$runDir = Join-Path $processTestRoot 'run'
$logDir = Join-Path $processTestRoot 'logs'
$previousCapturePath = $env:AGENT_PLATFORM_TEST_CAPTURE_ARGS
$previousDelayMs = $env:AGENT_PLATFORM_TEST_BACKEND_DELAY_MS
$previousExitCode = $env:AGENT_PLATFORM_TEST_BACKEND_EXIT_CODE

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
        int delayMs;
        if (!int.TryParse(Environment.GetEnvironmentVariable("AGENT_PLATFORM_TEST_BACKEND_DELAY_MS"), out delayMs)) delayMs = 0;
        Thread.Sleep(delayMs);
        int exitCode;
        if (!int.TryParse(Environment.GetEnvironmentVariable("AGENT_PLATFORM_TEST_BACKEND_EXIT_CODE"), out exitCode)) exitCode = 0;
        return exitCode;
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
  $Script:IdentityFile = $identityFile
  $Script:RuntimeMode = 'desktop'
  Update-ProgramPaths
  $env:AGENT_PLATFORM_TEST_CAPTURE_ARGS = $capturedArgsFile
  $env:AGENT_PLATFORM_TEST_BACKEND_DELAY_MS = '5000'

  Start-ProgramBackend -Daemon

  $deadline = [DateTime]::UtcNow.AddSeconds(3)
  while (-not (Test-Path -LiteralPath $capturedArgsFile -PathType Leaf) -and [DateTime]::UtcNow -lt $deadline) {
    Start-Sleep -Milliseconds 100
  }
  if (-not (Test-Path -LiteralPath $capturedArgsFile -PathType Leaf)) {
    throw 'fake backend did not capture daemon arguments'
  }

  $capturedArgs = @(Get-Content -LiteralPath $capturedArgsFile)
  $expectedArgs = @('--config-dir', $configRoot, '--runtime-mode', 'desktop', '--port', '17078', '--identity-file', $identityFile)
  if ($capturedArgs.Count -ne $expectedArgs.Count) {
    throw "expected $($expectedArgs.Count) daemon arguments, got $($capturedArgs.Count): $($capturedArgs -join ' | ')"
  }
  for ($i = 0; $i -lt $expectedArgs.Count; $i++) {
    if ($capturedArgs[$i] -cne $expectedArgs[$i]) {
      throw "daemon argument $i mismatch: expected '$($expectedArgs[$i])', got '$($capturedArgs[$i])'"
    }
  }
  Write-Host '[test] agent-platform daemon preserves spaced identity paths'

  $daemonPid = (Get-Content -LiteralPath $Script:PidFile -Raw).Trim()
  Stop-Process -Id ([int]$daemonPid) -Force
  Remove-Item -LiteralPath $Script:PidFile, $capturedArgsFile -Force
  $env:AGENT_PLATFORM_TEST_BACKEND_DELAY_MS = '0'
  Start-ProgramBackend
  $capturedForegroundArgs = @(Get-Content -LiteralPath $capturedArgsFile)
  if ($capturedForegroundArgs.Count -ne $expectedArgs.Count) {
    throw "expected $($expectedArgs.Count) foreground arguments, got $($capturedForegroundArgs.Count): $($capturedForegroundArgs -join ' | ')"
  }
  for ($i = 0; $i -lt $expectedArgs.Count; $i++) {
    if ($capturedForegroundArgs[$i] -cne $expectedArgs[$i]) {
      throw "foreground argument $i mismatch: expected '$($expectedArgs[$i])', got '$($capturedForegroundArgs[$i])'"
    }
  }
  Write-Host '[test] agent-platform foreground preserves spaced identity paths'

  $resourceSource = Join-Path $processTestRoot 'current env.zip'
  $resourcePreviousSource = Join-Path $processTestRoot 'previous env.zip'
  [System.IO.File]::WriteAllText($resourceSource, '')
  [System.IO.File]::WriteAllText($resourcePreviousSource, '')
  $Script:DeployAPRuntimeDir = Join-Path $processTestRoot 'runtime root'
  $Script:DeployRuntimeResourceSource = $resourceSource
  $Script:DeployRuntimeResourcePreviousSource = $resourcePreviousSource
  $Script:DeployRuntimeResourceMode = 'version-change'
  $Script:DeployDesktopVersionFrom = 'v0.3.26'
  $Script:DeployDesktopVersionTo = 'v0.3.27'
  $Script:DeployDesktopDeviceId = 'desktop-device-123'
  $env:AGENT_PLATFORM_TEST_CAPTURE_ARGS = $capturedArgsFile
  $env:AGENT_PLATFORM_TEST_BACKEND_EXIT_CODE = '0'
  Invoke-ProgramRuntimeResourceSync
  $capturedResourceArgs = @(Get-Content -LiteralPath $capturedArgsFile)
  $expectedResourceArgs = @(
    'runtime-resource-sync',
    '--ap-runtime-dir', $Script:DeployAPRuntimeDir,
    '--runtime-resource-source', $resourceSource,
    '--desktop-version-from', 'v0.3.26',
    '--desktop-version-to', 'v0.3.27',
    '--desktop-device-id', 'desktop-device-123',
    '--mode', 'version-change',
    '--runtime-resource-previous-source', $resourcePreviousSource
  )
  if (($capturedResourceArgs -join "`n") -cne ($expectedResourceArgs -join "`n")) {
    throw "runtime resource arguments were not forwarded exactly: $($capturedResourceArgs -join ' | ')"
  }
  $env:AGENT_PLATFORM_TEST_BACKEND_EXIT_CODE = '23'
  $syncFailed = $false
  try {
    Invoke-ProgramRuntimeResourceSync
  } catch {
    $syncFailed = $_.Exception.Message -like '*exit code 23*'
  }
  if (-not $syncFailed) { throw 'runtime resource subcommand failure did not fail deploy' }
  Write-Host '[test] runtime resource sync arguments and failures are forwarded'
} finally {
  if (Test-Path -LiteralPath $Script:PidFile -PathType Leaf) {
    $testPid = (Get-Content -LiteralPath $Script:PidFile -Raw -ErrorAction SilentlyContinue).Trim()
    if ($testPid -match '^\d+$') {
      Stop-Process -Id ([int]$testPid) -Force -ErrorAction SilentlyContinue
    }
  }
  $env:AGENT_PLATFORM_TEST_CAPTURE_ARGS = $previousCapturePath
  $env:AGENT_PLATFORM_TEST_BACKEND_DELAY_MS = $previousDelayMs
  $env:AGENT_PLATFORM_TEST_BACKEND_EXIT_CODE = $previousExitCode
  if (Test-Path -LiteralPath $processTestRoot) {
    Remove-Item -LiteralPath $processTestRoot -Recurse -Force -ErrorAction SilentlyContinue
  }
}
