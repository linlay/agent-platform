#Requires -Version 5.1
param(
    [switch]$All,
    [string[]]$Target,
    [string]$BuiltinsRoot
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Split-Path -Parent $ScriptDir
$BuildRoot = Join-Path $RepoRoot "build/builtins"
$CanonicalLock = Join-Path $ScriptDir "release-assets/builtins.lock.json"
function Assert-Command {
    param([string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "$Name is required"
    }
}

function Assert-Target {
    param([string]$Value)
    if ($Value -ne "windows/amd64") {
        throw "Unsupported native PowerShell builtin target: $Value (allowed: windows/amd64)"
    }
}

function Invoke-Native {
    param(
        [string]$Command,
        [string[]]$Arguments,
        [string]$WorkingDirectory
    )
    Push-Location $WorkingDirectory
    try {
        & $Command @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "$Command failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

function Copy-IsolatedProject {
    param([string]$Name, [string]$CollectionRoot)
    $source = Join-Path $BuiltinsRoot $Name
    $destination = Join-Path $CollectionRoot $Name
    New-Item -ItemType Directory -Path $destination -Force | Out-Null
    & robocopy $source $destination /E /XD dist target /XF .DS_Store /NFL /NDL /NJH /NJS /NP
    if ($LASTEXITCODE -ge 8) {
        throw "robocopy failed for $Name with exit code $LASTEXITCODE"
    }
}

function Copy-ExistingKbaseRelease {
    param([string]$CollectionRoot, [string]$TargetOS, [string]$TargetArch)
    $sourceRoot = Join-Path $BuiltinsRoot "kbase-lance-engine"
    $version = (Get-Content -LiteralPath (Join-Path $sourceRoot "VERSION") -Raw).Trim()
    $archiveName = "kbase-lance-engine`_$version`_$TargetOS`_$TargetArch.zip"
    $sourceArchive = Join-Path $sourceRoot "dist/$version/$archiveName"
    if (-not (Test-Path -LiteralPath $sourceArchive -PathType Leaf)) {
        return $false
    }
    $destinationDir = Join-Path $CollectionRoot "kbase-lance-engine/dist/$version"
    New-Item -ItemType Directory -Path $destinationDir -Force | Out-Null
    Copy-Item -LiteralPath $sourceArchive -Destination (Join-Path $destinationDir $archiveName) -Force
    $sourceHash = "$sourceArchive.sha256"
    if (Test-Path -LiteralPath $sourceHash -PathType Leaf) {
        Copy-Item -LiteralPath $sourceHash -Destination "$($destinationDir)/$archiveName.sha256" -Force
    }
    Write-Host "[builtins-sync] reuse kbase-lance-engine release: $sourceArchive"
    return $true
}

if ($All -and $Target.Count -gt 0) {
    throw "Use either -All or -Target, not both"
}

$Targets = @()
if ($All) {
    $Targets = @("windows/amd64")
} elseif ($Target.Count -gt 0) {
    $Targets = @($Target)
} else {
    $Targets = @("windows/amd64")
}
$Targets = @($Targets | ForEach-Object { $_.Trim().ToLowerInvariant() } | Select-Object -Unique)
foreach ($item in $Targets) { Assert-Target $item }

foreach ($command in @("go", "git", "powershell", "robocopy")) { Assert-Command $command }
if (-not (Test-Path -LiteralPath $CanonicalLock -PathType Leaf)) {
    throw "Canonical builtin lock not found: $CanonicalLock"
}

if (-not $BuiltinsRoot) {
    $BuiltinsRoot = Join-Path (Split-Path -Parent $RepoRoot) "agent-platform-builtins"
}
if (-not [IO.Path]::IsPathRooted($BuiltinsRoot)) {
    throw "-BuiltinsRoot must be an absolute path"
}
$BuiltinsRoot = (Resolve-Path -LiteralPath $BuiltinsRoot).Path
foreach ($component in @("ripgrep", "dbx", "httpx", "kbase-lance-engine", "poppler-pdftotext")) {
    $componentRoot = Join-Path $BuiltinsRoot $component
    if (-not (Test-Path -LiteralPath $componentRoot -PathType Container)) {
        throw "Missing sibling builtin project: $componentRoot"
    }
}

New-Item -ItemType Directory -Path $BuildRoot -Force | Out-Null
$WorkDir = Join-Path $BuildRoot ".sync.$([Guid]::NewGuid().ToString('N'))"
$CollectionRoot = Join-Path $WorkDir "collection"
$canonicalHashBefore = (Get-FileHash -LiteralPath $CanonicalLock -Algorithm SHA256).Hash
$oldGoCache = $env:GOCACHE
$oldGoModCache = $env:GOMODCACHE

try {
    New-Item -ItemType Directory -Path $CollectionRoot -Force | Out-Null
    if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $BuildRoot ".gocache" }
    if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $BuildRoot ".gomodcache" }
    New-Item -ItemType Directory -Path $env:GOCACHE -Force | Out-Null
    New-Item -ItemType Directory -Path $env:GOMODCACHE -Force | Out-Null

    foreach ($component in @("ripgrep", "dbx", "httpx", "kbase-lance-engine", "poppler-pdftotext")) {
        Copy-IsolatedProject -Name $component -CollectionRoot $CollectionRoot
    }

    foreach ($item in $Targets) {
        $parts = $item.Split('/')
        $targetOS = $parts[0]
        $targetArch = $parts[1]
        Invoke-Native -Command "powershell" -WorkingDirectory (Join-Path $CollectionRoot "dbx") -Arguments @(
            "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "scripts/release/build.ps1", "-TargetOS", $targetOS, "-TargetArch", $targetArch
        )
        Invoke-Native -Command "powershell" -WorkingDirectory (Join-Path $CollectionRoot "httpx") -Arguments @(
            "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "scripts/release/build.ps1", "-TargetOS", $targetOS, "-TargetArch", $targetArch
        )
    }

    $printArgs = @("run", "./cmd/prepare-local-builtins-lock", "--input", $CanonicalLock, "--print-component-targets", "poppler-pdftotext")
    foreach ($item in $Targets) { $printArgs += @("--target", $item) }
    Push-Location $RepoRoot
    try {
        $popplerTargets = @(& go @printArgs)
        if ($LASTEXITCODE -ne 0) { throw "Could not resolve locked Poppler targets" }
    } finally {
        Pop-Location
    }
    foreach ($item in $popplerTargets) {
        if (-not $item) { continue }
        $parts = $item.Trim().Split('/')
        Invoke-Native -Command "powershell" -WorkingDirectory (Join-Path $CollectionRoot "poppler-pdftotext") -Arguments @(
            "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "scripts/release/build.ps1", "-TargetOS", $parts[0], "-TargetArch", $parts[1]
        )
    }

    foreach ($item in $Targets) {
        $parts = $item.Split('/')
        if (-not (Copy-ExistingKbaseRelease -CollectionRoot $CollectionRoot -TargetOS $parts[0] -TargetArch $parts[1])) {
            $cargoTargetDir = Join-Path $BuildRoot ".cargo-target/$($parts[0])-$($parts[1])"
            Invoke-Native -Command "powershell" -WorkingDirectory (Join-Path $CollectionRoot "kbase-lance-engine") -Arguments @(
                "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "scripts/build-release.ps1",
                "-TargetOS", $parts[0], "-TargetArch", $parts[1], "-CargoTargetDir", $cargoTargetDir
            )
        }
    }

    $LocalLock = Join-Path $WorkDir "builtins.local.lock.json"
    $lockArgs = @(
        "run", "./cmd/prepare-local-builtins-lock", "--input", $CanonicalLock,
        "--output", $LocalLock, "--builtins-root", $CollectionRoot
    )
    foreach ($item in $Targets) { $lockArgs += @("--target", $item) }
    Invoke-Native -Command "go" -WorkingDirectory $RepoRoot -Arguments $lockArgs

    foreach ($item in $Targets) {
        $parts = $item.Split('/')
        $stageDir = Join-Path $WorkDir "$($parts[0])-$($parts[1])"
        New-Item -ItemType Directory -Path $stageDir -Force | Out-Null
        Invoke-Native -Command "go" -WorkingDirectory $RepoRoot -Arguments @(
            "run", "./cmd/stage-builtins", "--repo-root", $RepoRoot, "--lock", $LocalLock,
            "--output", $stageDir, "--os", $parts[0], "--arch", $parts[1], "--builtins-root", $CollectionRoot
        )
        Invoke-Native -Command "go" -WorkingDirectory $RepoRoot -Arguments @(
            "run", "./cmd/stage-kbase-lance-engine", "--repo-root", $RepoRoot, "--lock", $LocalLock,
            "--output", $stageDir, "--os", $parts[0], "--arch", $parts[1], "--builtins-root", $CollectionRoot
        )
    }

    # Activation starts only after every target has built and staged. A failed
    # move restores the previous cache for both the current and earlier targets.
    $activated = New-Object System.Collections.ArrayList
    try {
        foreach ($item in $Targets) {
            $parts = $item.Split('/')
            $name = "$($parts[0])-$($parts[1])"
            $destination = Join-Path $BuildRoot $name
            $staged = Join-Path $WorkDir $name
            $backup = Join-Path $BuildRoot ".$name.previous"
            Remove-Item -LiteralPath $backup -Recurse -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $destination) {
                Move-Item -LiteralPath $destination -Destination $backup
            }
            try {
                Move-Item -LiteralPath $staged -Destination $destination
            } catch {
                Remove-Item -LiteralPath $destination -Recurse -Force -ErrorAction SilentlyContinue
                if (Test-Path -LiteralPath $backup) { Move-Item -LiteralPath $backup -Destination $destination }
                throw
            }
            [void]$activated.Add([PSCustomObject]@{ Destination = $destination; Backup = $backup })
        }
    } catch {
        for ($index = $activated.Count - 1; $index -ge 0; $index--) {
            $record = $activated[$index]
            Remove-Item -LiteralPath $record.Destination -Recurse -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $record.Backup) {
                Move-Item -LiteralPath $record.Backup -Destination $record.Destination
            }
        }
        throw
    }
    foreach ($record in $activated) {
        Remove-Item -LiteralPath $record.Backup -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host "[builtins-sync] updated $($Targets.Count) target cache(s) under $BuildRoot"
} finally {
    if ($null -eq $oldGoCache) { Remove-Item Env:GOCACHE -ErrorAction SilentlyContinue } else { $env:GOCACHE = $oldGoCache }
    if ($null -eq $oldGoModCache) { Remove-Item Env:GOMODCACHE -ErrorAction SilentlyContinue } else { $env:GOMODCACHE = $oldGoModCache }
    Remove-Item -LiteralPath $WorkDir -Recurse -Force -ErrorAction SilentlyContinue
    $canonicalHashAfter = (Get-FileHash -LiteralPath $CanonicalLock -Algorithm SHA256).Hash
    if ($canonicalHashAfter -ne $canonicalHashBefore) {
        throw "Canonical builtins.lock.json changed during local builtin sync"
    }
}
