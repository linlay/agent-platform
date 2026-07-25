#Requires -Version 5.1
$ErrorActionPreference = 'Stop'

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Split-Path -Parent $ScriptDir
$TempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("agent-platform-deploy-test-" + [guid]::NewGuid().ToString('N'))

function Assert-Test([bool]$Condition, [string]$Message) {
  if (-not $Condition) {
    throw "[program-deploy-test] $Message"
  }
}

try {
  $BundleRoot = Join-Path $TempRoot 'agent-platform'
  $BundleBackend = Join-Path $BundleRoot 'backend'
  $BundleConfigs = Join-Path $BundleRoot 'configs'
  $BundleScripts = Join-Path $BundleRoot 'scripts'
  New-Item -ItemType Directory -Force -Path $BundleBackend, $BundleConfigs, $BundleScripts | Out-Null

  Copy-Item (Join-Path $RepoRoot 'scripts/release-assets/program/windows/deploy.ps1') (Join-Path $BundleRoot 'deploy.ps1')
  Copy-Item (Join-Path $RepoRoot 'scripts/release-assets/program/windows/program-common.ps1') (Join-Path $BundleScripts 'program-common.ps1')
  Copy-Item (Join-Path $RepoRoot 'configs/ai-tools.example.yml') (Join-Path $BundleConfigs 'ai-tools.example.yml')
  [System.IO.File]::WriteAllText((Join-Path $BundleRoot 'manifest.json'), "{}`r`n")
  [System.IO.File]::WriteAllText((Join-Path $BundleRoot '.env.example'), "AP_RUNTIME_DIR=`r`nAP_CONTAINER_HUB_BASE_URL=`r`n")
  [System.IO.File]::WriteAllText((Join-Path $BundleBackend 'agent-platform.exe'), '')
  $PublicKey = Join-Path $TempRoot 'local-public-key.pem'
  [System.IO.File]::WriteAllText($PublicKey, "test-public-key`r`n")
  $DeployScript = Join-Path $BundleRoot 'deploy.ps1'

  function Invoke-TestDeploy([string]$OutputDir, [string[]]$AdditionalArgs) {
    $DeployArgs = @(
      '--output-dir', $OutputDir,
      '--ap-runtime-dir', (Join-Path $OutputDir 'runtime'),
      '--container-hub-base-url', 'http://127.0.0.1:19090',
      '--public-key-source-file', $PublicKey
    ) + $AdditionalArgs
    & $DeployScript @DeployArgs
  }

  $ConfiguredOutput = Join-Path $TempRoot 'configured'
  Invoke-TestDeploy $ConfiguredOutput @('--ai-image-generate-model-key', 'image-model-key')
  $ConfiguredFile = Join-Path (Join-Path $ConfiguredOutput 'configs') 'ai-tools.yml'
  $ConfiguredContent = [System.IO.File]::ReadAllText($ConfiguredFile).Replace("`r`n", "`n")
  $ImageStart = $ConfiguredContent.IndexOf("image-generate:`n")
  $ImageEnd = $ConfiguredContent.IndexOf("speech:`n", $ImageStart)
  Assert-Test ($ImageStart -ge 0 -and $ImageEnd -gt $ImageStart) 'image-generate section was not rendered'
  $ImageBlock = $ConfiguredContent.Substring($ImageStart, $ImageEnd - $ImageStart)
  Assert-Test ($ImageBlock.Contains("  enabled: true`n")) 'image-generate was not enabled'
  Assert-Test ($ImageBlock.Contains("      model-key: image-model-key`n")) 'image-generate model key was not rendered'
  $BlankModelKeys = ([regex]::Matches($ConfiguredContent, '(?m)^      model-key:$')).Count
  Assert-Test ($BlankModelKeys -eq 3) 'an unrelated AI tool model key changed'

  $DefaultOutput = Join-Path $TempRoot 'default'
  Invoke-TestDeploy $DefaultOutput @()
  $TemplateContent = [System.IO.File]::ReadAllText((Join-Path $RepoRoot 'configs/ai-tools.example.yml'))
  $DefaultContent = [System.IO.File]::ReadAllText((Join-Path (Join-Path $DefaultOutput 'configs') 'ai-tools.yml'))
  Assert-Test ($DefaultContent -ceq $TemplateContent) 'default image-generate config changed without the deploy argument'

  $ExistingOutput = Join-Path $TempRoot 'existing'
  $ExistingConfigDir = Join-Path $ExistingOutput 'configs'
  New-Item -ItemType Directory -Force -Path $ExistingConfigDir | Out-Null
  $ExistingFile = Join-Path $ExistingConfigDir 'ai-tools.yml'
  [System.IO.File]::WriteAllText($ExistingFile, "custom-ai-tools-config`r`n")
  Invoke-TestDeploy $ExistingOutput @('--ai-image-generate-model-key', 'ignored-model-key')
  Assert-Test ([System.IO.File]::ReadAllText($ExistingFile) -ceq "custom-ai-tools-config`r`n") 'existing ai-tools.yml was overwritten'

  $MissingValueFailed = $false
  try {
    Invoke-TestDeploy (Join-Path $TempRoot 'missing-value') @('--ai-image-generate-model-key')
  } catch {
    $MissingValueFailed = $true
    Assert-Test ($_.Exception.Message.Contains('missing value for --ai-image-generate-model-key')) 'missing value returned an unexpected error'
  }
  Assert-Test $MissingValueFailed 'missing image-generate model key unexpectedly succeeded'

  Write-Host '[program-deploy-test] passed'
} finally {
  Remove-Item -LiteralPath $TempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
