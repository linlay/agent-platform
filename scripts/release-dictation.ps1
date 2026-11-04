#Requires -Version 5.1
param([string]$VERSION, [string]$ARCH = 'amd64', [string]$PROGRAM_TARGETS = 'windows', [string]$PROGRAM_TARGET_MATRIX)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'release-common.ps1')
$root = Split-Path -Parent $PSScriptRoot
if (!$VERSION) { $VERSION = (Get-Content -LiteralPath (Join-Path $root 'VERSION') -Raw).Trim() }
$release = Join-Path $root 'dist/release'
$stage = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString())
$previous = @{ GOOS=$env:GOOS; GOARCH=$env:GOARCH; CGO_ENABLED=$env:CGO_ENABLED }
$location = Get-Location
try {
    Set-Location $root
    New-Item -ItemType Directory -Path $release -Force | Out-Null
    foreach ($target in (Get-ProgramTargetMatrix -Targets $PROGRAM_TARGETS -Matrix $PROGRAM_TARGET_MATRIX -Arch $ARCH)) {
        $targetOS, $targetArch = $target.Os, $target.Arch
        if ($targetOS -ne 'windows' -or $targetArch -notin @('amd64', 'arm64')) { throw "Unsupported native release target: $target" }
        $bundle = Join-Path $stage "$targetArch/agent-dictation"
        New-Item -ItemType Directory -Path (Join-Path $bundle 'backend'), (Join-Path $bundle 'scripts') -Force | Out-Null
        $env:GOOS=$targetOS; $env:GOARCH=$targetArch; $env:CGO_ENABLED='0'
        go build -o (Join-Path $bundle 'backend/agent-dictation.exe') ./cmd/agent-dictation
        if ($LASTEXITCODE -ne 0) { throw 'Dictation build failed' }
        foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process') }
        $assets = Join-Path $PSScriptRoot 'release-assets/dictation'
        Copy-Item -LiteralPath (Join-Path $assets '.env.example') -Destination $bundle
        Copy-Item -LiteralPath (Join-Path $root 'configs/dictation.example.yml') -Destination $bundle
        Copy-Item -LiteralPath (Join-Path $assets 'windows/program-common.ps1') -Destination (Join-Path $bundle 'scripts')
        foreach ($action in @('deploy', 'start', 'stop')) { Copy-Item -LiteralPath (Join-Path $assets "windows/$action.ps1") -Destination $bundle }
        $asset = "agent-dictation-$VERSION-$targetOS-$targetArch.zip"
        go run ./cmd/render-program-manifest --template (Join-Path $assets 'manifest.template.json') --output (Join-Path $bundle 'manifest.json') --version $VERSION --os $targetOS --arch $targetArch --backend backend/agent-dictation.exe --asset $asset
        if ($LASTEXITCODE -ne 0) { throw 'Dictation manifest failed' }
        $archive = Join-Path $release $asset
        Compress-Directory -StageRoot (Split-Path $bundle -Parent) -BundleDirName 'agent-dictation' -OutputPath $archive -Format zip
        Write-ReleaseChecksum $archive
    }
} finally {
    foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process') }
    Set-Location $location
    if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
}
