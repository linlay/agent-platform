#Requires -Version 5.1
param(
    [string]$VERSION,
    [string]$ARCH,
    [string]$PROGRAM_TARGETS,
    [string]$PROGRAM_TARGET_MATRIX
)

$ErrorActionPreference = "Stop"

if (-not $env:REQUIRE_RELEASE_SBOM) { $env:REQUIRE_RELEASE_SBOM = "1" }

$APP_NAME = "agent-platform"
$PROGRAM_NAME = "agent-platform"
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$REPO_ROOT = Split-Path -Parent $SCRIPT_DIR
$PROGRAM_RELEASE_ASSETS_DIR = Join-Path $SCRIPT_DIR "release-assets/program"
$RELEASE_DIR = Join-Path $REPO_ROOT "dist/release"

function Get-DetectedArch {
    $arch = $env:PROCESSOR_ARCHITECTURE
    if ($arch -eq "AMD64") { return "amd64" }
    if ($arch -eq "ARM64") { return "arm64" }
    if ($arch -eq "x86") { return "amd64" }
    Write-Error "Cannot detect ARCH from processor architecture: $arch"
}

function Test-Version {
    param([string]$ver)
    if ($ver -notmatch '^v\d+\.\d+\.\d+$') {
        Write-Error "VERSION must match vX.Y.Z (got: $ver)"
    }
}

function Get-ArchiveFormat {
    param([string]$TargetOs)
    if ($TargetOs -eq "windows") { return "zip" }
    return "tar.gz"
}

function Get-BinaryName {
    param([string]$TargetOs)
    if ($TargetOs -eq "windows") { return "$PROGRAM_NAME.exe" }
    return $PROGRAM_NAME
}

function Get-BundleFilename {
    param([string]$Version, [string]$TargetOs, [string]$TargetArch, [string]$Format)
    return "$APP_NAME-$Version-$TargetOs-$TargetArch.$Format"
}

function Test-RequiredFile {
    param([string]$Path)
    if (-not (Test-Path $Path -PathType Leaf)) {
        Write-Error "Required file not found: $Path"
    }
}

function Test-RequiredDir {
    param([string]$Path)
    if (-not (Test-Path $Path -PathType Container)) {
        Write-Error "Required directory not found: $Path"
    }
}

function Test-ReleaseTools {
    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) {
        Write-Error "go is required"
    }
}

function Test-ArchiveTool {
    param([string]$Format)
    if ($Format -eq "tar.gz") {
        $tar = Get-Command tar -ErrorAction SilentlyContinue
        if (-not $tar) {
            Write-Error "tar is required for $target_os bundles"
        }
    }
}

function Copy-ConfigTemplates {
    param([string]$BundleRoot)
    $templates = @(
        Get-ChildItem "$REPO_ROOT/configs/*.example.yml" -ErrorAction SilentlyContinue
        Get-ChildItem "$REPO_ROOT/configs/*.example.yaml" -ErrorAction SilentlyContinue
        Get-ChildItem "$REPO_ROOT/configs/*.example.pem" -ErrorAction SilentlyContinue
    )
    foreach ($t in $templates) {
        Copy-Item $t.FullName (Join-Path $BundleRoot "configs") -Force
    }
}

function Compress-Directory {
    param(
        [string]$StageRoot,
        [string]$BundleDirName,
        [string]$OutputPath,
        [string]$Format
    )

    $bundlePath = Join-Path $StageRoot $BundleDirName
    $parentDir = Split-Path -Parent $OutputPath
    if (-not (Test-Path $parentDir)) {
        New-Item -ItemType Directory -Path $parentDir -Force | Out-Null
    }

    if ($Format -eq "zip") {
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        if (Test-Path -LiteralPath $OutputPath) {
            Remove-Item -LiteralPath $OutputPath -Force
        }
        # Archive the stage root directly. Moving the bundle into another
        # temporary directory deleted the staging tree that size and SBOM
        # generation still consume after compression.
        [System.IO.Compression.ZipFile]::CreateFromDirectory($StageRoot, $OutputPath, [System.IO.Compression.CompressionLevel]::Optimal, $false)
    } else {
        $oldPwd = $PWD
        Push-Location $StageRoot
        try {
            tar -czf $OutputPath $BundleDirName
        } finally {
            Pop-Location
        }
    }
}

function Write-ProgramManifest {
    param(
        [string]$Dest,
        [string]$TargetOs,
        [string]$TargetArch,
        [string]$BackendEntry,
        [string]$AssetFileName
    )

    $template = Join-Path $PSScriptRoot "release-assets\manifest.template.json"
    & go run ./cmd/render-program-manifest --template $template --output $Dest --version $VERSION --os $TargetOs --arch $TargetArch --backend $BackendEntry --asset $AssetFileName
    if ($LASTEXITCODE -ne 0) { throw "Manifest renderer failed for $TargetOs/$TargetArch" }
}
function Build-ProgramBundle {
    param(
        [string]$TargetOs,
        [string]$TargetArch
    )

    $binaryName = Get-BinaryName -TargetOs $TargetOs
    $archiveFormat = Get-ArchiveFormat -TargetOs $TargetOs
    $bundleArchive = Join-Path $RELEASE_DIR (Get-BundleFilename -Version $VERSION -TargetOs $TargetOs -TargetArch $TargetArch -Format $archiveFormat)

    Write-Host "[release] program VERSION=$VERSION TARGET_OS=$TargetOs ARCH=$TargetArch"

    $tmpDir = Join-Path $env:TEMP "agent-platform-program-release.$([System.Guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

    try {
        $stageRoot = Join-Path $tmpDir "stage"
        $bundleRoot = Join-Path $stageRoot $APP_NAME
        $backendDir = Join-Path $bundleRoot "backend"
        $scriptsDir = Join-Path $bundleRoot "scripts"
        $backendPath = Join-Path $backendDir $binaryName
        $backendEntry = "backend/$binaryName"

        New-Item -ItemType Directory -Path $backendDir -Force | Out-Null
        New-Item -ItemType Directory -Path $scriptsDir -Force | Out-Null
        New-Item -ItemType Directory -Path (Join-Path $bundleRoot "configs") -Force | Out-Null

        Write-Host "[release] building program binary for $TargetOs..."
        $oldCGOEnabled = $env:CGO_ENABLED
        $oldGOOS = $env:GOOS
        $oldGOARCH = $env:GOARCH
        try {
            $env:CGO_ENABLED = "0"
            $env:GOOS = $TargetOs
            $env:GOARCH = $TargetArch
            & go build -o $backendPath ./cmd/agent-platform
            if ($LASTEXITCODE -ne 0) {
                Write-Error "go build failed for $TargetOs/$TargetArch"
            }
        } finally {
            if ($null -eq $oldCGOEnabled) { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $oldCGOEnabled }
            if ($null -eq $oldGOOS) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $oldGOOS }
            if ($null -eq $oldGOARCH) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $oldGOARCH }
        }

        Write-Host "[release] assembling program bundle for $TargetOs..."

        Copy-Item "$REPO_ROOT/.env.example" $bundleRoot

        $manifestPath = Join-Path $bundleRoot "manifest.json"
        Write-ProgramManifest -Dest $manifestPath -TargetOs $TargetOs -TargetArch $TargetArch -BackendEntry $backendEntry -AssetFileName (Split-Path $bundleArchive -Leaf)

        Copy-ConfigTemplates -BundleRoot $bundleRoot
        if ($TargetOs -eq "windows") {
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/windows/tools.example.yml" (Join-Path (Join-Path $bundleRoot "configs") "tools.example.yml") -Force
        }
        $builtinsCache = Join-Path (Join-Path (Join-Path $REPO_ROOT "build") "builtins") "$TargetOs-$TargetArch"
        & "$SCRIPT_DIR/stage-builtins.ps1" -OutputDir $bundleRoot -TargetOS $TargetOs -TargetArch $TargetArch -CacheDir $builtinsCache
        if ($LASTEXITCODE -ne 0) {
            Write-Error "stage builtins failed for $TargetOs/$TargetArch"
        }

        if ($TargetOs -eq "windows") {
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/windows/deploy.ps1" $bundleRoot
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/windows/start.ps1" $bundleRoot
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/windows/stop.ps1" $bundleRoot
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/windows/program-common.ps1" $scriptsDir
        } else {
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/unix/deploy.sh" $bundleRoot
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/unix/start.sh" $bundleRoot
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/unix/stop.sh" $bundleRoot
            Copy-Item "$PROGRAM_RELEASE_ASSETS_DIR/unix/program-common.sh" $scriptsDir
        }

        if (-not (Test-Path $RELEASE_DIR)) {
            New-Item -ItemType Directory -Path $RELEASE_DIR -Force | Out-Null
        }

        Compress-Directory -StageRoot $stageRoot -BundleDirName $APP_NAME -OutputPath $bundleArchive -Format $archiveFormat

        & go run ./cmd/verify-program-bundle --archive $bundleArchive --os $TargetOs --arch $TargetArch
        if ($LASTEXITCODE -ne 0) {
            throw "verify program bundle failed for $TargetOs/$TargetArch"
        }

        $archiveDigest = (Get-FileHash -Algorithm SHA256 $bundleArchive).Hash.ToLowerInvariant()
        [IO.File]::WriteAllText("$bundleArchive.sha256", "$archiveDigest  $(Split-Path $bundleArchive -Leaf)`n", [Text.UTF8Encoding]::new($false))
        $sidecarName = if ($TargetOs -eq "windows") { "kbase-lance-engine.exe" } else { "kbase-lance-engine" }
        $sidecarPath = Join-Path (Join-Path $bundleRoot "bin") $sidecarName
        $sizes = [ordered]@{
            backendBytes = (Get-Item $backendPath).Length
            sidecarBytes = (Get-Item $sidecarPath).Length
            archiveBytes = (Get-Item $bundleArchive).Length
        } | ConvertTo-Json
        [IO.File]::WriteAllText("$bundleArchive.sizes.json", "$sizes`n", [Text.UTF8Encoding]::new($false))

        $syft = Get-Command syft -ErrorAction SilentlyContinue
        Remove-Item "$bundleArchive.sbom.cdx.json" -Force -ErrorAction SilentlyContinue
        if ($syft) {
            & syft "dir:$bundleRoot" -o "cyclonedx-json=$bundleArchive.sbom.cdx.json"
            if ($LASTEXITCODE -ne 0) { throw "Syft failed" }
        } elseif ($env:REQUIRE_RELEASE_SBOM -eq "1") {
            throw "Syft is required because REQUIRE_RELEASE_SBOM=1"
        } else {
            Write-Warning "Syft not found; bundle SBOM hook skipped (set REQUIRE_RELEASE_SBOM=1 in release CI)"
        }

        Write-Host "[release] done: $bundleArchive"
    } finally {
        Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Get-ProgramTargetMatrix {
    param(
        [string]$Targets,
        [string]$Matrix,
        [string]$Arch
    )

    if ($Matrix) {
        $entries = $Matrix -split ','
        foreach ($entry in $entries) {
            $parts = $entry -split '/'
            if ($parts.Count -ne 2) {
                Write-Error "PROGRAM_TARGET_MATRIX entries must look like <os>/<arch> (got: $entry)"
            }
            Write-Output @{ Os = $parts[0]; Arch = $parts[1] }
        }
    } elseif ($Targets) {
        $osList = $Targets -split ','
        foreach ($os in $osList) {
            Write-Output @{ Os = $os.Trim(); Arch = $Arch }
        }
    } else {
        # This is the native Windows release entry. With no explicit matrix it
        # must never depend on Bash host detection or an undefined host arch.
        Write-Output @{ Os = "windows"; Arch = $Arch }
    }
}

# Main
Push-Location $REPO_ROOT
try {
    Test-ReleaseTools
    Test-RequiredFile (Join-Path $SCRIPT_DIR "stage-builtins.ps1")

    # Resolve version: read from file if not provided
    $VERSION_FILE = Join-Path $REPO_ROOT "VERSION"
    if ($VERSION) {
        $VERSION = $VERSION.Trim()
    } elseif (Test-Path $VERSION_FILE) {
        $VERSION = (Get-Content $VERSION_FILE -Raw).Trim()
        if (-not $VERSION) { $VERSION = "dev" }
    } else {
        $VERSION = "dev"
    }
    Test-Version $VERSION

    # Resolve arch
    if (-not $ARCH) {
        $ARCH = Get-DetectedArch
    }
    if ($ARCH -notin @("amd64", "arm64")) {
        Write-Error "ARCH must be amd64 or arm64 (got: $ARCH)"
    }

    $targets = @(Get-ProgramTargetMatrix -Targets $PROGRAM_TARGETS -Matrix $PROGRAM_TARGET_MATRIX -Arch $ARCH)

    foreach ($target in $targets) {
        $os = $target.Os
        $targetArch = $target.Arch

        if ($os -notin @("linux", "darwin", "windows")) {
            Write-Error "TARGET_OS must be linux, darwin, or windows (got: $os)"
        }
        if ($targetArch -notin @("amd64", "arm64")) {
            Write-Error "ARCH must be amd64 or arm64 (got: $targetArch)"
        }

        Test-ArchiveTool (Get-ArchiveFormat -TargetOs $os)
        Build-ProgramBundle -TargetOs $os -TargetArch $targetArch
    }
} finally {
    Pop-Location
}
