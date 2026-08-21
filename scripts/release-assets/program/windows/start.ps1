$ErrorActionPreference = 'Stop'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $ScriptDir 'scripts/program-common.ps1')

$Daemon = $false
$layoutArgs = [System.Collections.Generic.List[string]]::new()
for ($i = 0; $i -lt $args.Length; $i++) {
  $arg = $args[$i]
  if ($arg.StartsWith('--runtime-mode=', [System.StringComparison]::OrdinalIgnoreCase)) {
    $layoutArgs.Add('--runtime-mode')
    $layoutArgs.Add($arg.Substring('--runtime-mode='.Length))
    continue
  }
  switch ($arg) {
    '--daemon' { $Daemon = $true }
    '-Daemon' { $Daemon = $true }
    default {
      $layoutArgs.Add($arg)
      if (@('--config-dir', '--state-dir', '--log-dir', '--port', '--identity-file', '--runtime-mode') -contains $arg) {
        if ($i + 1 -ge $args.Length) {
          Fail-Program "missing value for $arg"
        }
        $i += 1
        $layoutArgs.Add($args[$i])
      }
    }
  }
}

Set-Location $ScriptDir
Set-ProgramLayoutArgs $layoutArgs.ToArray()
Test-ProgramBundle
Import-ProgramEnv
Set-ProgramServerPortEnv
Initialize-ProgramRuntime
Start-ProgramBackend -Daemon:$Daemon
