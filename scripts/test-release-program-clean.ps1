#Requires -Version 5.1
$ErrorActionPreference = "Stop"

$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$REPO_ROOT = Split-Path -Parent $SCRIPT_DIR
if ($env:OS -ne "Windows_NT" -or $env:PROCESSOR_ARCHITECTURE -ne "AMD64") {
    throw "This regression runs on a Windows AMD64 release runner"
}

$version = (Get-Content (Join-Path $REPO_ROOT "VERSION") -Raw).Trim()
$archive = Join-Path $REPO_ROOT "dist/release/agent-platform-$version-windows-amd64.zip"
$artifactDir = Join-Path $REPO_ROOT "dist/kbase-lance-engine/windows-amd64"

Remove-Item (Join-Path $REPO_ROOT "dist") -Recurse -Force -ErrorAction SilentlyContinue
$env:REQUIRE_KBASE_RELEASE_METADATA = "1"
$env:REQUIRE_RELEASE_SBOM = "1"
Remove-Item Env:PROGRAM_TARGET_MATRIX -ErrorAction SilentlyContinue
Remove-Item Env:PROGRAM_TARGETS -ErrorAction SilentlyContinue
Remove-Item Env:ARCH -ErrorAction SilentlyContinue

Push-Location $REPO_ROOT
try {
    & make release-program
    if ($LASTEXITCODE -ne 0) { throw "make release-program failed" }

    foreach ($path in @(
        (Join-Path $artifactDir "kbase-lance-engine.exe"),
        (Join-Path $artifactDir "kbase-lance-engine.exe.sha256"),
        (Join-Path $artifactDir "cargo-metadata.json"),
        (Join-Path $artifactDir "sbom.cdx.json"),
        $archive,
        "$archive.sha256",
        "$archive.sizes.json",
        "$archive.sbom.cdx.json"
    )) {
        if (-not (Test-Path $path -PathType Leaf)) { throw "Missing release output: $path" }
    }

    & go run ./cmd/verify-program-bundle --archive $archive --os windows --arch amd64
    if ($LASTEXITCODE -ne 0) { throw "verify-program-bundle failed" }
} finally {
    Pop-Location
}

Write-Host "[release-clean-test] passed: $archive"
