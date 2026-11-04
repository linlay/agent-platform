$ErrorActionPreference = 'Stop'
$DictationRoot = Split-Path -Parent $PSScriptRoot
$ConfigDir = $DictationRoot
$StateDir = Join-Path $DictationRoot 'run'
$LogDir = $StateDir
$DictationPort = ''
$TokenFile = ''
$RegistryDir = ''
$Daemon = $false
function Read-DictationArgs([string[]]$Arguments) {
    for ($i = 0; $i -lt $Arguments.Length; $i++) {
        $name = $Arguments[$i]
        if ($name -eq '--daemon') { $script:Daemon = $true; continue }
        if ($i + 1 -ge $Arguments.Length) { throw "Missing value for $name" }
        $value = $Arguments[++$i]
        switch ($name) {
            '--config-dir' { $script:ConfigDir = $value }
            '--output-dir' { $script:ConfigDir = $value }
            '--state-dir' { $script:StateDir = $value }
            '--log-dir' { $script:LogDir = $value }
            '--port' { $script:DictationPort = $value }
            '--sso-id-token-file' { $script:TokenFile = $value }
            '--registry-dir' { $script:RegistryDir = $value }
            default { throw "Unsupported dictation argument: $name" }
        }
    }
    $script:Binary = Join-Path $DictationRoot 'backend/agent-dictation.exe'
    $script:PidFile = Join-Path $StateDir 'agent-dictation.pid'
}
function Get-DictationProcess {
    if (!(Test-Path -LiteralPath $PidFile)) { return $null }
    $processId = 0
    if (![int]::TryParse((Get-Content -LiteralPath $PidFile -Raw).Trim(), [ref]$processId)) { throw 'Invalid dictation PID file' }
    $process = Get-CimInstance Win32_Process -Filter "ProcessId=$processId"
    if (!$process) { return $null }
    if ($process.ExecutablePath -ne $Binary) { throw 'PID belongs to another executable; refusing control' }
    return $process
}
function Write-DictationFile([string]$Path, [string]$Text) {
    [IO.File]::WriteAllText($Path, $Text, (New-Object Text.UTF8Encoding($false)))
}
