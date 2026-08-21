[CmdletBinding()]
param(
    [string]$Version = "",
    [switch]$NonInteractive
)

$ErrorActionPreference = "Stop"
$work = "C:\df-hud-build"
$output = "C:\df-hud-output"

$shared = $null
foreach ($candidate in @("Z:\", "\\host.lan\Data")) {
    if (Test-Path "$candidate\go.mod") {
        $shared = $candidate
        break
    }
}
if (-not $shared) {
    throw "The shared df-hud repository is unavailable at Z:\ or \\host.lan\Data"
}
if ([string]::IsNullOrWhiteSpace($Version)) {
    if ($NonInteractive) {
        $Version = "0.1.0-local"
    } else {
        $Version = Read-Host "Version [0.1.0-local]"
        if ([string]::IsNullOrWhiteSpace($Version)) {
            $Version = "0.1.0-local"
        }
    }
}

New-Item $work -ItemType Directory -Force | Out-Null
& robocopy.exe $shared $work /MIR /XD `
    (Join-Path $shared ".git") `
    (Join-Path $shared "dist") `
    (Join-Path $shared "windows-vm") `
    /NFL /NDL /NJH /NJS /NP
$robocopyExit = $LASTEXITCODE
if ($robocopyExit -gt 7) {
    throw "Copying the source failed with robocopy exit code $robocopyExit"
}

Remove-Item $output -Recurse -Force -ErrorAction SilentlyContinue
& (Join-Path $work "build-windows.ps1") `
    -OutputDirectory $output `
    -Version $Version

$archiveName = "df-hud-$Version-windows-amd64.zip"
$sourceArchive = Join-Path $output $archiveName
$sharedDist = Join-Path $shared "dist"
$targetArchive = Join-Path $sharedDist $archiveName
New-Item $sharedDist -ItemType Directory -Force | Out-Null
Copy-Item $sourceArchive $targetArchive -Force

$hash = (Get-FileHash $targetArchive -Algorithm SHA256).Hash.ToLowerInvariant()
Write-Host ""
Write-Host "Built df-hud $Version"
Write-Host "Copied to $targetArchive"
Write-Host "SHA-256: $hash"
if (-not $NonInteractive) {
    Read-Host "Press Enter to close"
}
