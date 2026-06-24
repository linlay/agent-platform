$ErrorActionPreference = 'Stop'

$Script:ProgramCommonDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$Script:BundleRoot = Split-Path -Parent $Script:ProgramCommonDir
$Script:AppName = 'agent-platform'
$Script:ManifestFile = Join-Path $Script:BundleRoot 'manifest.json'
$Script:EnvExampleFile = Join-Path $Script:BundleRoot '.env.example'
$Script:ConfigRoot = $Script:BundleRoot
$Script:EnvFile = Join-Path $Script:ConfigRoot '.env'
$Script:BackendBin = Join-Path (Join-Path $Script:BundleRoot 'backend') 'agent-platform.exe'
$Script:ConfigDir = Join-Path $Script:ConfigRoot 'configs'
$Script:RuntimeRoot = Join-Path $Script:BundleRoot 'runtime'
$Script:RuntimeRootExplicit = $false
$Script:RunDir = Join-Path $Script:BundleRoot 'run'
$Script:LogDir = $Script:RunDir
$Script:PidFile = Join-Path $Script:RunDir 'agent-platform.pid'
$Script:LogFile = Join-Path $Script:LogDir 'agent-platform.log'
$Script:ErrorLogFile = Join-Path $Script:LogDir 'agent-platform.stderr.log'
$Script:ProgramPort = ''

function Fail-Program([string]$Message) {
  throw "[program] $Message"
}

function Test-ProgramBundle {
  if (-not (Test-Path -LiteralPath $Script:ManifestFile -PathType Leaf)) {
    Fail-Program "required file not found: $Script:ManifestFile"
  }
  if (-not (Test-Path -LiteralPath $Script:EnvExampleFile -PathType Leaf)) {
    Fail-Program "required file not found: $Script:EnvExampleFile"
  }
  if (-not (Test-Path -LiteralPath $Script:BackendBin -PathType Leaf)) {
    Fail-Program "required file not found: $Script:BackendBin"
  }
}

function Update-ProgramPaths {
  $Script:EnvFile = Join-Path $Script:ConfigRoot '.env'
  $Script:ConfigDir = Join-Path $Script:ConfigRoot 'configs'
  $Script:PidFile = Join-Path $Script:RunDir 'agent-platform.pid'
  $Script:LogFile = Join-Path $Script:LogDir 'agent-platform.log'
  $Script:ErrorLogFile = Join-Path $Script:LogDir 'agent-platform.stderr.log'
}

function Set-ProgramLayoutOption([string]$Name, [string]$Value) {
  switch ($Name) {
    '--config-dir' { $Script:ConfigRoot = $Value }
    '--runtime-dir' {
      $Script:RuntimeRoot = $Value
      $Script:RuntimeRootExplicit = $true
    }
    '--state-dir' { $Script:RunDir = $Value }
    '--log-dir' { $Script:LogDir = $Value }
    '--port' { $Script:ProgramPort = $Value }
    default { Fail-Program "unsupported argument: $Name" }
  }
  Update-ProgramPaths
}

function Set-ProgramLayoutArgs([string[]]$Arguments) {
  for ($i = 0; $i -lt $Arguments.Length; $i++) {
    $name = $Arguments[$i]
    if (@('--config-dir', '--runtime-dir', '--state-dir', '--log-dir', '--port') -notcontains $name) {
      Fail-Program "unsupported argument: $name"
    }
    if ($i + 1 -ge $Arguments.Length) {
      Fail-Program "missing value for $name"
    }
    $i += 1
    Set-ProgramLayoutOption $name $Arguments[$i]
  }
}

function Initialize-ProgramConfig {
  New-Item -ItemType Directory -Force -Path $Script:ConfigDir | Out-Null
  if (-not (Test-Path -LiteralPath $Script:EnvFile -PathType Leaf)) {
    Copy-Item -LiteralPath $Script:EnvExampleFile -Destination $Script:EnvFile
  }
  $bundleConfigDir = Join-Path $Script:BundleRoot 'configs'
  if (-not (Test-Path -LiteralPath $bundleConfigDir -PathType Container)) {
    return
  }
  foreach ($example in Get-ChildItem -LiteralPath $bundleConfigDir -Filter '*.example.yml' -File) {
    $name = $example.Name.Substring(0, $example.Name.Length - '.example.yml'.Length)
    $target = Join-Path $Script:ConfigDir ($name + '.yml')
    if ($name -eq 'channels') {
      if (-not (Test-Path -LiteralPath $target -PathType Leaf)) {
        New-Item -ItemType File -Path $target -Force | Out-Null
      }
      continue
    }
    if (-not (Test-Path -LiteralPath $target -PathType Leaf)) {
      Copy-Item -LiteralPath $example.FullName -Destination $target
    }
  }
  foreach ($example in Get-ChildItem -LiteralPath $bundleConfigDir -Filter '*.example.pem' -File) {
    $name = $example.Name.Substring(0, $example.Name.Length - '.example.pem'.Length)
    $target = Join-Path $Script:ConfigDir ($name + '.pem')
    if (-not (Test-Path -LiteralPath $target -PathType Leaf)) {
      Copy-Item -LiteralPath $example.FullName -Destination $target
    }
  }
}

function Import-ProgramEnv {
  if (-not (Test-Path -LiteralPath $Script:EnvFile -PathType Leaf)) {
    Fail-Program 'missing .env (copy from .env.example first)'
  }
  foreach ($rawLine in Get-Content -LiteralPath $Script:EnvFile) {
    $line = $rawLine.Trim()
    if ([string]::IsNullOrWhiteSpace($line) -or $line.StartsWith('#')) {
      continue
    }
    $index = $line.IndexOf('=')
    if ($index -lt 1) {
      continue
    }
    $name = $line.Substring(0, $index).Trim()
    $value = $line.Substring($index + 1)
    [Environment]::SetEnvironmentVariable($name, $value, 'Process')
  }
}

function Set-ProgramServerPortEnv {
  if ([string]::IsNullOrWhiteSpace($env:SERVER_PORT)) {
    $env:SERVER_PORT = '11949'
  }
}

function Resolve-ProgramRuntimePath {
  param([string]$Value)
  $trimmed = $Value.Trim()
  if ($trimmed -eq '~') { return $HOME }
  if ($trimmed.StartsWith('~/') -or $trimmed.StartsWith('~\')) {
    return (Join-Path $HOME $trimmed.Substring(2))
  }
  if ([System.IO.Path]::IsPathRooted($trimmed)) { return $trimmed }
  return (Join-Path $Script:BundleRoot $trimmed)
}

function Resolve-ProgramRuntimeRoot {
  if (-not $Script:RuntimeRootExplicit -and $env:AP_RUNTIME_DIR) {
    $Script:RuntimeRoot = Resolve-ProgramRuntimePath $env:AP_RUNTIME_DIR
  }
}

function Initialize-ProgramRuntime {
  Resolve-ProgramRuntimeRoot
  New-Item -ItemType Directory -Force -Path `
    $Script:RunDir, `
    $Script:LogDir, `
    (Join-Path $Script:RuntimeRoot 'registries/providers'), `
    (Join-Path $Script:RuntimeRoot 'registries/models'), `
    (Join-Path $Script:RuntimeRoot 'registries/mcp-servers'), `
    (Join-Path $Script:RuntimeRoot 'registries/viewport-servers'), `
    (Join-Path $Script:RuntimeRoot 'tools'), `
    (Join-Path $Script:RuntimeRoot 'viewports'), `
    (Join-Path $Script:RuntimeRoot 'owner'), `
    (Join-Path $Script:RuntimeRoot 'agents'), `
    (Join-Path $Script:RuntimeRoot 'teams'), `
    (Join-Path $Script:RuntimeRoot 'root'), `
    (Join-Path $Script:RuntimeRoot 'automations'), `
    (Join-Path $Script:RuntimeRoot 'chats'), `
    (Join-Path $Script:RuntimeRoot 'memory'), `
    (Join-Path $Script:RuntimeRoot 'pan'), `
    (Join-Path $Script:RuntimeRoot 'skills-market') | Out-Null
}

function Clear-StaleProgramPid {
  if (-not (Test-Path -LiteralPath $Script:PidFile -PathType Leaf)) {
    return
  }

  $pidValue = (Get-Content -LiteralPath $Script:PidFile -Raw).Trim()
  if (-not [string]::IsNullOrWhiteSpace($pidValue)) {
    try {
      $null = Get-Process -Id ([int]$pidValue) -ErrorAction Stop
      Fail-Program "$Script:AppName is already running with pid $pidValue"
    } catch [Microsoft.PowerShell.Commands.ProcessCommandException] {
      Remove-Item -LiteralPath $Script:PidFile -Force -ErrorAction SilentlyContinue
      return
    }
  }

  Remove-Item -LiteralPath $Script:PidFile -Force -ErrorAction SilentlyContinue
}

function Start-ProgramBackend {
  param(
    [switch]$Daemon
  )

  if ($Daemon) {
    Clear-StaleProgramPid
    if (Test-Path -LiteralPath $Script:LogFile) {
      Clear-Content -LiteralPath $Script:LogFile
    } else {
      New-Item -ItemType File -Path $Script:LogFile -Force | Out-Null
    }
    if (Test-Path -LiteralPath $Script:ErrorLogFile) {
      Clear-Content -LiteralPath $Script:ErrorLogFile
    } else {
      New-Item -ItemType File -Path $Script:ErrorLogFile -Force | Out-Null
    }

    $backendArgs = @('--config-dir', $Script:ConfigRoot, '--runtime-dir', $Script:RuntimeRoot)
    if (-not [string]::IsNullOrWhiteSpace($Script:ProgramPort)) {
      $backendArgs += @('--port', $Script:ProgramPort)
    }
    $proc = Start-Process -FilePath $Script:BackendBin -ArgumentList $backendArgs -WorkingDirectory $Script:BundleRoot -WindowStyle Hidden -RedirectStandardOutput $Script:LogFile -RedirectStandardError $Script:ErrorLogFile -PassThru
    $proc.Id | Set-Content -LiteralPath $Script:PidFile
    Start-Sleep -Seconds 1
    if ($proc.HasExited) {
      Remove-Item -LiteralPath $Script:PidFile -Force -ErrorAction SilentlyContinue
      Fail-Program "backend failed to start; see $Script:LogFile and $Script:ErrorLogFile"
    }
    Write-Host "[program-start] started $Script:AppName in daemon mode (pid=$($proc.Id))"
    Write-Host "[program-start] log file: $Script:LogFile"
    Write-Host "[program-start] stderr file: $Script:ErrorLogFile"
    return
  }

  $backendArgs = @('--config-dir', $Script:ConfigRoot, '--runtime-dir', $Script:RuntimeRoot)
  if (-not [string]::IsNullOrWhiteSpace($Script:ProgramPort)) {
    $backendArgs += @('--port', $Script:ProgramPort)
  }
  & $Script:BackendBin @backendArgs
}

function Stop-ProgramBackend {
  if (-not (Test-Path -LiteralPath $Script:PidFile -PathType Leaf)) {
    Write-Host "[program-stop] pid file not found: $Script:PidFile"
    return
  }

  $pidValue = (Get-Content -LiteralPath $Script:PidFile -Raw).Trim()
  if ([string]::IsNullOrWhiteSpace($pidValue)) {
    Fail-Program "pid file is empty: $Script:PidFile"
  }

  try {
    $proc = Get-Process -Id ([int]$pidValue) -ErrorAction Stop
  } catch [Microsoft.PowerShell.Commands.ProcessCommandException] {
    Remove-Item -LiteralPath $Script:PidFile -Force -ErrorAction SilentlyContinue
    Write-Host "[program-stop] process $pidValue is not running; removed stale pid file"
    return
  }

  Stop-Process -Id $proc.Id -ErrorAction Stop
  for ($i = 0; $i -lt 30; $i++) {
    Start-Sleep -Seconds 1
    if ($proc.HasExited) {
      Remove-Item -LiteralPath $Script:PidFile -Force -ErrorAction SilentlyContinue
      Write-Host "[program-stop] stopped $Script:AppName (pid=$($proc.Id))"
      return
    }
    $proc.Refresh()
  }

  Fail-Program "process $($proc.Id) did not stop within 30s"
}
