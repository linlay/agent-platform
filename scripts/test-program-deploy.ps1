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
  Invoke-TestDeploy $ConfiguredOutput @('--ai-image-generate-model-key', 'th-gpt-image-2_5-sunburst')
  $ConfiguredFile = Join-Path (Join-Path $ConfiguredOutput 'configs') 'ai-tools.yml'
  $ConfiguredContent = [System.IO.File]::ReadAllText($ConfiguredFile).Replace("`r`n", "`n")
  $ImageStart = $ConfiguredContent.IndexOf("image-generate:`n")
  $ImageEnd = $ConfiguredContent.IndexOf("speech:`n", $ImageStart)
  Assert-Test ($ImageStart -ge 0 -and $ImageEnd -gt $ImageStart) 'image-generate section was not rendered'
  $ImageBlock = $ConfiguredContent.Substring($ImageStart, $ImageEnd - $ImageStart)
  Assert-Test ($ImageBlock.Contains("  enabled: true`n")) 'image-generate was not enabled'
  Assert-Test ($ImageBlock.Contains("      model-key: th-gpt-image-2_5-sunburst`n")) 'image-generate model key was not rendered'
  Assert-Test ($ImageBlock.Contains("  default-profile: th-gpt-image-2_5-sunburst`n")) 'image default profile was not selected'
  Assert-Test (-not $ImageBlock.Contains("    general:")) 'image general profile must not exist'
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

  $ExpectedRuntimeRoot = 'D:\测试数据目录\.agent-platform'
  $Utf8NoBom = [System.Text.UTF8Encoding]::new($false)
  [System.IO.File]::WriteAllText(
    (Join-Path $BundleRoot '.env'),
    "AP_RUNTIME_DIR=$ExpectedRuntimeRoot`r`n",
    $Utf8NoBom
  )
  . (Join-Path $BundleScripts 'program-common.ps1')
  Import-ProgramEnv
  Assert-Test ($env:AP_RUNTIME_DIR -ceq $ExpectedRuntimeRoot) 'UTF-8 .env path was not preserved'

  Copy-Item (Join-Path $RepoRoot 'configs/runtime.example.yml') (Join-Path $BundleConfigs 'runtime.example.yml')
  $PreviewOutput = Join-Path $TempRoot 'preview'
  $Rejected = $false
  try { Invoke-TestDeploy $PreviewOutput @() } catch { $Rejected = $true }
  Assert-Test $Rejected 'missing preview URLs accepted'
  Invoke-TestDeploy $PreviewOutput @('--document-preview-api-base-url', 'http://hub:8090', '--document-preview-public-base-url', 'https://docs.test')
  $PreviewFile = Join-Path $PreviewOutput 'configs/runtime.yml'
  $PreviewBefore = [IO.File]::ReadAllText($PreviewFile)
  Assert-Test ($PreviewBefore.Contains('api-base-url: "http://hub:8090"')) 'API origin not rendered'
  Assert-Test ($PreviewBefore.Contains('public-base-url: "https://docs.test"')) 'public origin not rendered'
  Invoke-TestDeploy $PreviewOutput @('--document-preview-api-base-url', 'https://ignored.test', '--document-preview-public-base-url', 'https://ignored.test')
  Assert-Test ([IO.File]::ReadAllText($PreviewFile) -ceq $PreviewBefore) 'existing runtime config overwritten'
  foreach ($Bad in @('https://user:secret@docs.test', 'https://docs.test/path', 'https://docs.test?x=1', 'https://docs.test"')) {
    $Rejected = $false
    try { Invoke-TestDeploy $PreviewOutput @('--document-preview-api-base-url', $Bad) } catch { $Rejected = $true }
    Assert-Test $Rejected 'invalid preview origin accepted'
  }
  $ResetArgs = @('--desktop-config-reset', '--desktop-config-backup-dir', (Join-Path $TempRoot 'preview-backup'), '--desktop-version-from', '1', '--desktop-version-to', '2')
  $Rejected = $false
  try { Invoke-TestDeploy $PreviewOutput $ResetArgs } catch { $Rejected = $true }
  Assert-Test $Rejected 'reset without preview URLs accepted'
  Assert-Test ([IO.File]::ReadAllText($PreviewFile) -ceq $PreviewBefore) 'failed reset changed config'
  Invoke-TestDeploy $PreviewOutput ($ResetArgs + @('--document-preview-api-base-url', 'https://new-api.test', '--document-preview-public-base-url', 'https://new-public.test'))
  Assert-Test ([IO.File]::ReadAllText($PreviewFile).Contains('public-base-url: "https://new-public.test"')) 'reset did not render preview origin'
  Write-Host '[program-deploy-test] passed'
} finally {
  Remove-Item Env:AP_RUNTIME_DIR -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $TempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
