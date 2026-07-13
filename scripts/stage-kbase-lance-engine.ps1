#Requires -Version 5.1
param(
    [Parameter(Mandatory = $true)]
    [string]$OutputDir,
    [Parameter(Mandatory = $true)]
    [ValidateSet("darwin", "linux", "windows")]
    [string]$TargetOS,
    [Parameter(Mandatory = $true)]
    [ValidateSet("amd64", "arm64")]
    [string]$TargetArch,
    [string]$ArtifactRoot,
    [string]$Url = $env:KBASE_LANCE_ENGINE_URL,
    [string]$ExpectedSHA256 = $env:KBASE_LANCE_ENGINE_SHA256,
    [switch]$LocalBuild,
    [switch]$Optional,
    [switch]$RefreshDownload
)

$ErrorActionPreference = "Stop"
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$REPO_ROOT = Split-Path -Parent $SCRIPT_DIR
if (-not $ArtifactRoot) {
    $ArtifactRoot = if ($env:KBASE_LANCE_ENGINE_ARTIFACT_ROOT) { $env:KBASE_LANCE_ENGINE_ARTIFACT_ROOT } else { Join-Path $REPO_ROOT "dist/kbase-lance-engine" }
}
$binaryName = if ($TargetOS -eq "windows") { "kbase-lance-engine.exe" } else { "kbase-lance-engine" }
$artifactDir = Join-Path $ArtifactRoot "$TargetOS-$TargetArch"
$artifactPath = Join-Path $artifactDir $binaryName

function Test-RequireReleaseMetadata {
    return $env:REQUIRE_KBASE_RELEASE_METADATA -eq "1"
}

function Write-CargoMetadata {
    param([string]$ArtifactDir)
    if (-not (Get-Command cargo -ErrorAction SilentlyContinue)) {
        throw "cargo is required to generate release dependency metadata"
    }
    $engineManifest = Join-Path $REPO_ROOT "native/kbase-lance-engine/Cargo.toml"
    $metadata = & cargo metadata --manifest-path $engineManifest --locked --format-version 1
    if ($LASTEXITCODE -ne 0) { throw "cargo metadata failed" }
    [IO.File]::WriteAllText((Join-Path $ArtifactDir "cargo-metadata.json"), ($metadata -join "`n"), [Text.UTF8Encoding]::new($false))
}

function Write-SidecarSBOM {
    param([string]$ArtifactPath, [string]$ArtifactDir)
    if (-not (Get-Command syft -ErrorAction SilentlyContinue)) {
        throw "Syft is required because REQUIRE_KBASE_RELEASE_METADATA=1"
    }
    $sbomPath = Join-Path $ArtifactDir "sbom.cdx.json"
    $tempPath = "$sbomPath.tmp"
    Remove-Item $tempPath -Force -ErrorAction SilentlyContinue
    & syft $ArtifactPath -o "cyclonedx-json=$tempPath"
    if ($LASTEXITCODE -ne 0) { throw "Syft failed" }
    Move-Item $tempPath $sbomPath -Force
}

function Get-VerifiedArtifactSHA256 {
    param([string]$ArtifactPath, [string]$BinaryName, [string]$ExpectedSHA256)
    $checksumPath = "$ArtifactPath.sha256"
    if (-not $ExpectedSHA256) {
        if (-not (Test-Path $checksumPath -PathType Leaf)) { throw "Missing checksum: $checksumPath" }
        $ExpectedSHA256 = ((Get-Content $checksumPath -Raw).Trim() -split '\s+')[0]
    }
    if ($ExpectedSHA256 -notmatch '^[0-9a-fA-F]{64}$') { throw "Invalid SHA-256 for $TargetOS/$TargetArch" }
    $actualSHA = (Get-FileHash -Algorithm SHA256 $ArtifactPath).Hash.ToLowerInvariant()
    $expectedNormalized = $ExpectedSHA256.ToLowerInvariant()
    if ($actualSHA -ne $expectedNormalized) {
        throw "Artifact SHA-256 mismatch: expected $ExpectedSHA256, got $actualSHA"
    }
    [IO.File]::WriteAllText($checksumPath, "$expectedNormalized  $BinaryName`n", [Text.UTF8Encoding]::new($false))
    return $expectedNormalized
}

if ($LocalBuild) {
    $Url = ""
    $ExpectedSHA256 = ""
    $buildArgs = @{
        TargetOS = $TargetOS
        TargetArch = $TargetArch
        OutputDir = $ArtifactRoot
    }
    if (Test-RequireReleaseMetadata) { $buildArgs.RequireSBOM = $true }
    & (Join-Path $SCRIPT_DIR "build-kbase-lance-engine.ps1") @buildArgs
    if ($LASTEXITCODE -ne 0) { throw "build kbase-lance-engine failed for $TargetOS/$TargetArch" }
}
if ($Url -and ($RefreshDownload -or -not (Test-Path $artifactPath -PathType Leaf))) {
    if (-not $ExpectedSHA256) { throw "ExpectedSHA256/KBASE_LANCE_ENGINE_SHA256 is required for direct downloads" }
    New-Item -ItemType Directory -Path $artifactDir -Force | Out-Null
    $tempPath = "$artifactPath.download"
    Remove-Item $tempPath -Force -ErrorAction SilentlyContinue
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $tempPath
    $actualSHA = (Get-FileHash -Algorithm SHA256 $tempPath).Hash.ToLowerInvariant()
    if ($actualSHA -ne $ExpectedSHA256.ToLowerInvariant()) {
        Remove-Item $tempPath -Force -ErrorAction SilentlyContinue
        throw "Download SHA-256 mismatch: expected $ExpectedSHA256, got $actualSHA"
    }
    Remove-Item $artifactPath -Force -ErrorAction SilentlyContinue
    Move-Item $tempPath $artifactPath -Force
    [IO.File]::WriteAllText("$artifactPath.sha256", "$ExpectedSHA256  $binaryName`n", [Text.UTF8Encoding]::new($false))
}
if (-not (Test-Path $artifactPath -PathType Leaf)) {
    if ($Optional -and -not $Url) {
        Remove-Item (Join-Path (Join-Path $OutputDir "bin") $binaryName) -Force -ErrorAction SilentlyContinue
        Write-Warning "Optional sidecar artifact is absent for $TargetOS/$TargetArch; local non-KBASE/SQLite development can continue. KBASE auto mode stays on SQLite and explicit Lance mode reports engine_unavailable."
        exit 0
    }
    throw "Missing sidecar artifact for $TargetOS/$TargetArch: $artifactPath. Build it with scripts/build-kbase-lance-engine.ps1, or provide KBASE_LANCE_ENGINE_URL and KBASE_LANCE_ENGINE_SHA256."
}

$ExpectedSHA256 = Get-VerifiedArtifactSHA256 -ArtifactPath $artifactPath -BinaryName $binaryName -ExpectedSHA256 $ExpectedSHA256
if (-not $LocalBuild -and (Test-RequireReleaseMetadata)) {
    Write-CargoMetadata -ArtifactDir $artifactDir
    Write-SidecarSBOM -ArtifactPath $artifactPath -ArtifactDir $artifactDir
}

$goArgs = @(
    "run", "./cmd/stage-kbase-lance-engine",
    "--repo-root", $REPO_ROOT,
    "--output", $OutputDir,
    "--os", $TargetOS,
    "--arch", $TargetArch,
    "--binary", $artifactPath
)
if ($LocalBuild) {
    $goArgs += "--local-build"
}
$goArgs += @("--expected-sha256", $ExpectedSHA256)
if ($Url) {
    $goArgs += @("--artifact-source", $Url)
}
$cargoMetadata = Join-Path $artifactDir "cargo-metadata.json"
if ((Test-Path $cargoMetadata -PathType Leaf) -and (Get-Item $cargoMetadata).Length -gt 0) {
    $goArgs += @("--cargo-metadata", $cargoMetadata)
} elseif (Test-RequireReleaseMetadata) {
    throw "cargo-metadata.json is required because REQUIRE_KBASE_RELEASE_METADATA=1"
}

Push-Location $REPO_ROOT
try {
    & go @goArgs
    if ($LASTEXITCODE -ne 0) { throw "stage kbase-lance-engine failed for $TargetOS/$TargetArch" }
} finally {
    Pop-Location
}

$sidecarSBOM = Join-Path $artifactDir "sbom.cdx.json"
if ((Test-Path $sidecarSBOM -PathType Leaf) -and (Get-Item $sidecarSBOM).Length -gt 0) {
    $sbomDir = Join-Path $OutputDir "sbom"
    New-Item -ItemType Directory -Path $sbomDir -Force | Out-Null
    Copy-Item $sidecarSBOM (Join-Path $sbomDir "kbase-lance-engine.cdx.json") -Force
} elseif (Test-RequireReleaseMetadata) {
    throw "sidecar SBOM is required because REQUIRE_KBASE_RELEASE_METADATA=1"
}
