[CmdletBinding()]
param(
    [string]$OutputDirectory = "dist",
    [string]$Version = ""
)

$ErrorActionPreference = "Stop"
$RepoRoot = $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($Version)) {
    $describe = git describe --tags --always --dirty 2>$null
    if ($LASTEXITCODE -eq 0 -and $describe) {
        $Version = $describe -replace '^v', ''
    } else {
        $Version = "unknown"
    }
}

$Cargo = Get-Command "cargo.exe" -ErrorAction SilentlyContinue
if (-not $Cargo) {
    throw "cargo was not found."
}

if (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $RepoRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$BaseName = "df-hud-$Version-windows-amd64"
$Stage = Join-Path $OutputDirectory $BaseName
$Archive = Join-Path $OutputDirectory "$BaseName.zip"
$Executable = Join-Path $Stage "df-hud.exe"

if (Test-Path $Stage) {
    Remove-Item $Stage -Recurse -Force
}
New-Item $Stage -ItemType Directory -Force | Out-Null

Push-Location $RepoRoot
try {
    & $Cargo.Source build --locked --release
    if ($LASTEXITCODE -ne 0) {
        throw "cargo build failed."
    }
}
finally {
    Pop-Location
}

Copy-Item (Join-Path $RepoRoot "target\release\df-hud.exe") $Executable
Copy-Item (Join-Path $RepoRoot "df-hud.example.toml") $Stage
Copy-Item (Join-Path $RepoRoot "LICENSE") $Stage

if (Test-Path $Archive) {
    Remove-Item $Archive -Force
}
Compress-Archive -Path (Join-Path $Stage "*") -DestinationPath $Archive

Write-Host "Built $Executable"
Write-Host "Packaged $Archive"
