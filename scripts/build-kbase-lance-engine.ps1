#Requires -Version 5.1
param(
    [ValidateSet("darwin", "linux", "windows")]
    [string]$TargetOS = "windows",
    [ValidateSet("amd64", "arm64")]
    [string]$TargetArch = "amd64",
    [string]$OutputDir,
    [switch]$RequireSBOM
)

$ErrorActionPreference = "Stop"
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$REPO_ROOT = Split-Path -Parent $SCRIPT_DIR
$ENGINE_DIR = Join-Path $REPO_ROOT "native/kbase-lance-engine"
if (-not $OutputDir) { $OutputDir = Join-Path $REPO_ROOT "dist/kbase-lance-engine" }

$triples = @{
    "darwin/amd64" = "x86_64-apple-darwin"
    "darwin/arm64" = "aarch64-apple-darwin"
    "linux/amd64" = "x86_64-unknown-linux-gnu"
    "linux/arm64" = "aarch64-unknown-linux-gnu"
    "windows/amd64" = "x86_64-pc-windows-msvc"
    "windows/arm64" = "aarch64-pc-windows-msvc"
}
$triple = $triples["$TargetOS/$TargetArch"]
if (-not $triple) { throw "Unsupported target $TargetOS/$TargetArch" }
if (-not (Test-Path (Join-Path $ENGINE_DIR "Cargo.lock") -PathType Leaf)) {
    throw "Cargo.lock is required: $ENGINE_DIR/Cargo.lock"
}
if (-not (Get-Command cargo -ErrorAction SilentlyContinue)) { throw "cargo is required" }
if (-not (Get-Command rustup -ErrorAction SilentlyContinue)) { throw "rustup is required" }
if (-not (Get-Command protoc -ErrorAction SilentlyContinue)) { throw "protoc is required by LanceDB's locked build dependencies; provision it on the build runner or use a verified prebuilt sidecar artifact" }
$installed = & rustup target list --installed
if ($LASTEXITCODE -ne 0 -or $installed -notcontains $triple) {
    throw "Rust target $triple is not installed. Provision it on the build runner before release; this script will not install toolchains."
}

Write-Host "[kbase-lance-build] building $TargetOS/$TargetArch ($triple)"
& cargo build --manifest-path (Join-Path $ENGINE_DIR "Cargo.toml") --release --locked --target $triple
if ($LASTEXITCODE -ne 0) { throw "cargo build failed for $TargetOS/$TargetArch" }

$binaryName = if ($TargetOS -eq "windows") { "kbase-lance-engine.exe" } else { "kbase-lance-engine" }
$builtPath = Join-Path $ENGINE_DIR "target/$triple/release/$binaryName"
if (-not (Test-Path $builtPath -PathType Leaf)) { throw "Cargo completed but binary is missing: $builtPath" }
$artifactDir = Join-Path $OutputDir "$TargetOS-$TargetArch"
New-Item -ItemType Directory -Path $artifactDir -Force | Out-Null
$artifactPath = Join-Path $artifactDir $binaryName
Copy-Item $builtPath $artifactPath -Force
$digest = (Get-FileHash -Algorithm SHA256 $artifactPath).Hash.ToLowerInvariant()
[IO.File]::WriteAllText("$artifactPath.sha256", "$digest  $binaryName`n", [Text.UTF8Encoding]::new($false))

$metadata = & cargo metadata --manifest-path (Join-Path $ENGINE_DIR "Cargo.toml") --locked --format-version 1
if ($LASTEXITCODE -ne 0) { throw "cargo metadata failed" }
[IO.File]::WriteAllText((Join-Path $artifactDir "cargo-metadata.json"), ($metadata -join "`n"), [Text.UTF8Encoding]::new($false))

$syft = Get-Command syft -ErrorAction SilentlyContinue
if ($syft) {
    & syft $artifactPath -o "cyclonedx-json=$(Join-Path $artifactDir 'sbom.cdx.json')"
    if ($LASTEXITCODE -ne 0) { throw "Syft failed" }
} elseif ($RequireSBOM) {
    throw "Syft is required by -RequireSBOM but was not found"
} else {
    Write-Warning "Syft not found; SBOM hook skipped (use -RequireSBOM in release CI)"
}
Write-Host "[kbase-lance-build] artifact: $artifactPath ($digest)"
