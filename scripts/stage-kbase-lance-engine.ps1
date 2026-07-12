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
    [switch]$Optional
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

if ($LocalBuild) {
    & (Join-Path $SCRIPT_DIR "build-kbase-lance-engine.ps1") -TargetOS $TargetOS -TargetArch $TargetArch -OutputDir $ArtifactRoot
}
if (-not (Test-Path $artifactPath -PathType Leaf) -and $Url) {
    if (-not $ExpectedSHA256) { throw "ExpectedSHA256/KBASE_LANCE_ENGINE_SHA256 is required for direct downloads" }
    New-Item -ItemType Directory -Path $artifactDir -Force | Out-Null
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $artifactPath
    $actualSHA = (Get-FileHash -Algorithm SHA256 $artifactPath).Hash.ToLowerInvariant()
    if ($actualSHA -ne $ExpectedSHA256.ToLowerInvariant()) {
        Remove-Item $artifactPath -Force -ErrorAction SilentlyContinue
        throw "Download SHA-256 mismatch: expected $ExpectedSHA256, got $actualSHA"
    }
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
} else {
    if (-not $ExpectedSHA256) {
        $checksumPath = "$artifactPath.sha256"
        if (-not (Test-Path $checksumPath -PathType Leaf)) { throw "Missing checksum: $checksumPath" }
        $ExpectedSHA256 = ((Get-Content $checksumPath -Raw).Trim() -split '\s+')[0]
    }
    $goArgs += @("--expected-sha256", $ExpectedSHA256)
}
if ($Url) {
    $goArgs += @("--artifact-source", $Url)
}
$cargoMetadata = Join-Path $artifactDir "cargo-metadata.json"
if (Test-Path $cargoMetadata -PathType Leaf) {
    $goArgs += @("--cargo-metadata", $cargoMetadata)
} elseif ($env:REQUIRE_KBASE_RELEASE_METADATA -eq "1") {
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
if (Test-Path $sidecarSBOM -PathType Leaf) {
    $sbomDir = Join-Path $OutputDir "sbom"
    New-Item -ItemType Directory -Path $sbomDir -Force | Out-Null
    Copy-Item $sidecarSBOM (Join-Path $sbomDir "kbase-lance-engine.cdx.json") -Force
} elseif ($env:REQUIRE_KBASE_RELEASE_METADATA -eq "1") {
    throw "sidecar SBOM is required because REQUIRE_KBASE_RELEASE_METADATA=1"
}
