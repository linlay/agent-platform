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
$Script:DeployAPRuntimeDir = ''
$Script:DeployContainerHubBaseUrl = ''
$Script:DeployAIVisionGeneralModelKey = ''
$Script:DeployAIVisionOCRModelKey = ''
$Script:DeployAIWebFetchModelKey = ''
$Script:DeployCoderModelKey = ''
$Script:DeployCoderReasoningEffort = ''
$Script:DeployLocalPublicKeyFile = ''
$Script:Utf8NoBom = [System.Text.UTF8Encoding]::new($false)

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

function Assert-ProgramArgValue([string]$Name, [string]$Value) {
  if ([string]::IsNullOrWhiteSpace($Value)) {
    Fail-Program "missing required deploy argument: $Name"
  }
}

function Reject-ProgramDeployStartArg([string]$Name) {
  Fail-Program "$Name is a start/runtime argument; pass it to start.ps1 instead of deploy.ps1"
}

function Set-ProgramDeployOption([string]$Name, [string]$Value) {
  switch ($Name) {
    '--output-dir' {
      Assert-ProgramArgValue '--output-dir' $Value
      $Script:ConfigRoot = $Value
    }
    '--ap-runtime-dir' { $Script:DeployAPRuntimeDir = $Value }
    '--container-hub-base-url' { $Script:DeployContainerHubBaseUrl = $Value }
    '--ai-vision-general-model-key' { $Script:DeployAIVisionGeneralModelKey = $Value }
    '--ai-vision-ocr-model-key' { $Script:DeployAIVisionOCRModelKey = $Value }
    '--ai-web-fetch-model-key' { $Script:DeployAIWebFetchModelKey = $Value }
    '--coder-model-key' { $Script:DeployCoderModelKey = $Value }
    '--coder-reasoning-effort' {
      if (@('NONE', 'LOW', 'MEDIUM', 'HIGH') -notcontains $Value) {
        Fail-Program '--coder-reasoning-effort must be one of NONE, LOW, MEDIUM, HIGH'
      }
      $Script:DeployCoderReasoningEffort = $Value
    }
    '--local-public-key-file' { $Script:DeployLocalPublicKeyFile = $Value }
    default { Fail-Program "unsupported deploy argument: $Name" }
  }
  Update-ProgramPaths
}

function Set-ProgramDeployArgs([string[]]$Arguments) {
  for ($i = 0; $i -lt $Arguments.Length; $i++) {
    $name = $Arguments[$i]
    if (@('--config-dir', '--runtime-dir', '--state-dir', '--log-dir', '--port', '--daemon') -contains $name) {
      Reject-ProgramDeployStartArg $name
    }
    if ($name -eq '--force') {
      Fail-Program 'unsupported deploy argument: --force'
    }
    if (@(
      '--output-dir',
      '--ap-runtime-dir',
      '--container-hub-base-url',
      '--ai-vision-general-model-key',
      '--ai-vision-ocr-model-key',
      '--ai-web-fetch-model-key',
      '--coder-model-key',
      '--coder-reasoning-effort',
      '--local-public-key-file'
    ) -notcontains $name) {
      Fail-Program "unsupported deploy argument: $name"
    }
    if ($i + 1 -ge $Arguments.Length) {
      Fail-Program "missing value for $name"
    }
    $i += 1
    Set-ProgramDeployOption $name $Arguments[$i]
  }

  Assert-ProgramArgValue '--ap-runtime-dir' $Script:DeployAPRuntimeDir
  Assert-ProgramArgValue '--container-hub-base-url' $Script:DeployContainerHubBaseUrl
  Assert-ProgramArgValue '--ai-vision-general-model-key' $Script:DeployAIVisionGeneralModelKey
  Assert-ProgramArgValue '--ai-vision-ocr-model-key' $Script:DeployAIVisionOCRModelKey
  Assert-ProgramArgValue '--ai-web-fetch-model-key' $Script:DeployAIWebFetchModelKey
  Assert-ProgramArgValue '--coder-model-key' $Script:DeployCoderModelKey
  Assert-ProgramArgValue '--coder-reasoning-effort' $Script:DeployCoderReasoningEffort
  Assert-ProgramArgValue '--local-public-key-file' $Script:DeployLocalPublicKeyFile
  if (-not (Test-Path -LiteralPath $Script:DeployLocalPublicKeyFile -PathType Leaf)) {
    Fail-Program "required file not found: $Script:DeployLocalPublicKeyFile"
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

function Write-ProgramTextFile([string]$Path, [string[]]$Lines) {
  [System.IO.File]::WriteAllText($Path, (($Lines -join [Environment]::NewLine) + [Environment]::NewLine), $Script:Utf8NoBom)
}

function Set-ProgramEnvValue([string]$Path, [string]$Name, [string]$Value) {
  $lines = [System.Collections.Generic.List[string]]::new()
  $found = $false
  foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
    if ($line -match ("^\s*#?\s*{0}=" -f [regex]::Escape($Name))) {
      $lines.Add(("{0}={1}" -f $Name, $Value))
      $found = $true
      continue
    }
    $lines.Add($line)
  }
  if (-not $found) {
    $lines.Add(("{0}={1}" -f $Name, $Value))
  }
  Write-ProgramTextFile $Path $lines.ToArray()
}

function New-ProgramDeployEnvFile([string]$Target) {
  Copy-Item -LiteralPath $Script:EnvExampleFile -Destination $Target
  Set-ProgramEnvValue $Target 'AP_RUNTIME_DIR' $Script:DeployAPRuntimeDir
  Set-ProgramEnvValue $Target 'AP_CONTAINER_HUB_BASE_URL' $Script:DeployContainerHubBaseUrl
}

function Set-ProgramAIToolsModelKey([string]$Path, [string]$Section, [string]$Profile, [string]$Value) {
  $lines = [System.Collections.Generic.List[string]]::new()
  $currentSection = ''
  $inProfiles = $false
  $currentProfile = ''
  $replaced = $false
  foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
    if ($line -match '^[^\s#][^:]*:') {
      $currentSection = $matches[0].TrimEnd(':')
      $inProfiles = $false
      $currentProfile = ''
    }
    if ($currentSection -eq $Section -and $line -match '^  profiles:') {
      $inProfiles = $true
      $lines.Add($line)
      continue
    }
    if ($currentSection -eq $Section -and $inProfiles -and $line -match '^    ([A-Za-z0-9_-]+):') {
      $currentProfile = $matches[1]
      $lines.Add($line)
      continue
    }
    if ($currentSection -eq $Section -and $inProfiles -and $currentProfile -eq $Profile -and $line -match '^      model-key:') {
      $lines.Add(("      model-key: {0}" -f $Value))
      $replaced = $true
      continue
    }
    $lines.Add($line)
  }
  if (-not $replaced) {
    Fail-Program "failed to update $Section.profiles.$Profile.model-key in $Path"
  }
  Write-ProgramTextFile $Path $lines.ToArray()
}

function New-ProgramDeployAIToolsFile([string]$Source, [string]$Target) {
  Copy-Item -LiteralPath $Source -Destination $Target
  Set-ProgramAIToolsModelKey $Target 'vision-recognize' 'general' $Script:DeployAIVisionGeneralModelKey
  Set-ProgramAIToolsModelKey $Target 'vision-recognize' 'ocr' $Script:DeployAIVisionOCRModelKey
  Set-ProgramAIToolsModelKey $Target 'web-fetch' 'general' $Script:DeployAIWebFetchModelKey
}

function Set-ProgramCoderDefaultValue([string]$Path, [string]$Name, [string]$Value) {
  $lines = [System.Collections.Generic.List[string]]::new()
  $inDefaultAgent = $false
  $replaced = $false
  foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
    if ($line -match '^[^\s#][^:]*:') {
      $inDefaultAgent = $matches[0] -eq 'default-agent:'
    }
    if ($inDefaultAgent -and $line -match ("^  {0}:" -f [regex]::Escape($Name))) {
      $lines.Add(("  {0}: {1}" -f $Name, $Value))
      $replaced = $true
      continue
    }
    $lines.Add($line)
  }
  if (-not $replaced) {
    Fail-Program "failed to update default-agent.$Name in $Path"
  }
  Write-ProgramTextFile $Path $lines.ToArray()
}

function New-ProgramDeployCoderSettingsFile([string]$Source, [string]$Target) {
  Copy-Item -LiteralPath $Source -Destination $Target
  Set-ProgramCoderDefaultValue $Target 'modelKey' $Script:DeployCoderModelKey
  Set-ProgramCoderDefaultValue $Target 'reasoningEffort' $Script:DeployCoderReasoningEffort
}

function Install-ProgramDeployLocalPublicKey {
  $target = Join-Path $Script:ConfigDir 'local-public-key.pem'
  if (-not (Test-Path -LiteralPath $target -PathType Leaf)) {
    Copy-Item -LiteralPath $Script:DeployLocalPublicKeyFile -Destination $target
  }
}

function Initialize-ProgramDeployConfig {
  New-Item -ItemType Directory -Force -Path $Script:ConfigDir | Out-Null
  if (-not (Test-Path -LiteralPath $Script:EnvFile -PathType Leaf)) {
    New-ProgramDeployEnvFile $Script:EnvFile
  }
  $bundleConfigDir = Join-Path $Script:BundleRoot 'configs'
  if (Test-Path -LiteralPath $bundleConfigDir -PathType Container) {
    foreach ($example in Get-ChildItem -LiteralPath $bundleConfigDir -Filter '*.example.yml' -File) {
      $name = $example.Name.Substring(0, $example.Name.Length - '.example.yml'.Length)
      $target = Join-Path $Script:ConfigDir ($name + '.yml')
      if ($name -eq 'channels') {
        if (-not (Test-Path -LiteralPath $target -PathType Leaf)) {
          New-Item -ItemType File -Path $target -Force | Out-Null
        }
        continue
      }
      if (Test-Path -LiteralPath $target -PathType Leaf) {
        continue
      }
      switch ($name) {
        'ai-tools' { New-ProgramDeployAIToolsFile $example.FullName $target }
        'coder-settings' { New-ProgramDeployCoderSettingsFile $example.FullName $target }
        default { Copy-Item -LiteralPath $example.FullName -Destination $target }
      }
    }
    foreach ($example in Get-ChildItem -LiteralPath $bundleConfigDir -Filter '*.example.pem' -File) {
      $name = $example.Name.Substring(0, $example.Name.Length - '.example.pem'.Length)
      if ($name -eq 'local-public-key') {
        continue
      }
      $target = Join-Path $Script:ConfigDir ($name + '.pem')
      if (-not (Test-Path -LiteralPath $target -PathType Leaf)) {
        Copy-Item -LiteralPath $example.FullName -Destination $target
      }
    }
  }
  Install-ProgramDeployLocalPublicKey
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
