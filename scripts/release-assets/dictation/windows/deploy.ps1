. (Join-Path $PSScriptRoot 'scripts/program-common.ps1')
Read-DictationArgs $args
New-Item -ItemType Directory -Path $ConfigDir -Force | Out-Null
$config = Join-Path $ConfigDir 'dictation.yml'
if (!(Test-Path -LiteralPath $config)) { Copy-Item -LiteralPath (Join-Path $DictationRoot 'dictation.example.yml') -Destination $config }
$envFile = Join-Path $ConfigDir '.env'
if (!(Test-Path -LiteralPath $envFile)) {
    $text = [IO.File]::ReadAllText((Join-Path $DictationRoot '.env.example'))
    if ($RegistryDir) {
        $model = Join-Path $RegistryDir 'models/dashscope-paraformer-realtime-v2.yml'
        $provider = Join-Path $RegistryDir 'providers/dashscope.yml'
        $text += "`nDICTATION_MODEL_FILE=$model`nDICTATION_PROVIDER_FILE=$provider`n"
    }
    Write-DictationFile $envFile $text
}
