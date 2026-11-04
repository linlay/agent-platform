# Shared archive and target handling; service bundle assembly stays in its entry script.
function Compress-Directory {
    param(
        [string]$StageRoot,
        [string]$BundleDirName,
        [string]$OutputPath,
        [string]$Format
    )

    $parentDir = Split-Path -Parent $OutputPath
    if (-not (Test-Path $parentDir)) {
        New-Item -ItemType Directory -Path $parentDir -Force | Out-Null
    }

    if ($Format -eq "zip") {
        # Windows PowerShell 5.1 requires both assemblies before resolving ZipArchiveMode.
        Add-Type -AssemblyName System.IO.Compression
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        if (Test-Path -LiteralPath $OutputPath) {
            Remove-Item -LiteralPath $OutputPath -Force
        }
        # Archive the stage root directly. Moving the bundle into another
        # temporary directory deleted the staging tree that size and SBOM
        # generation still consume after compression.
        # .NET Framework on Windows emits backslash entry names. ZIP requires
        # forward slashes so Go and other readers preserve empty directories.
        $stagePrefix = [IO.Path]::GetFullPath($StageRoot).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
        $archive = [IO.Compression.ZipFile]::Open($OutputPath, [IO.Compression.ZipArchiveMode]::Create)
        try {
            foreach ($item in Get-ChildItem -LiteralPath $StageRoot -Recurse -Force) {
                $entryName = $item.FullName.Substring($stagePrefix.Length).Replace('\', '/')
                if ($item.PSIsContainer) {
                    $entry = $archive.CreateEntry($entryName + '/')
                    $entry.ExternalAttributes = 16 # DOS directory attribute.
                } else {
                    [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, $item.FullName, $entryName, [IO.Compression.CompressionLevel]::Optimal) | Out-Null
                }
            }
        } finally {
            $archive.Dispose()
        }
    } else {
        Push-Location $StageRoot
        try {
            tar -czf $OutputPath $BundleDirName
        } finally {
            Pop-Location
        }
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

function Write-ReleaseChecksum([string]$ArchivePath) {
    $digest = (Get-FileHash -Algorithm SHA256 $ArchivePath).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$ArchivePath.sha256", "$digest  $(Split-Path $ArchivePath -Leaf)`n", [Text.UTF8Encoding]::new($false))
}
