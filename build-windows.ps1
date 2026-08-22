[CmdletBinding()]
param(
    [string]$OutputDirectory = "dist",
    [string]$Version = "0.1.0-windows"
)

$ErrorActionPreference = "Stop"
$RepoRoot = $PSScriptRoot

$GoCommand = Get-Command "go.exe" -ErrorAction SilentlyContinue
$Go = if ($GoCommand) {
    $GoCommand.Source
} else {
    Join-Path $env:ProgramFiles "Go\bin\go.exe"
}
if (-not (Test-Path $Go)) {
    throw "Go was not found."
}

$WindresCommand = Get-Command "x86_64-w64-mingw32-windres.exe" -ErrorAction SilentlyContinue
if (-not $WindresCommand) {
    $WindresCommand = Get-Command "windres.exe" -ErrorAction SilentlyContinue
}
$Windres = if ($WindresCommand) {
    $WindresCommand.Source
} else {
    $MsysRoot = if ($env:MSYS2_ROOT) { $env:MSYS2_ROOT } else { "C:\msys64" }
    Join-Path $MsysRoot "ucrt64\bin\windres.exe"
}
if (-not (Test-Path $Windres)) {
    throw "windres was not found. Install MinGW binutils or set MSYS2_ROOT."
}

if (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $RepoRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$BaseName = "df-hud-$Version-windows-amd64"
$Stage = Join-Path $OutputDirectory $BaseName
$Archive = Join-Path $OutputDirectory "$BaseName.zip"
$Executable = Join-Path $Stage "df-hud.exe"
$ResourceObject = Join-Path $RepoRoot "cmd\df-hud\df-hud_windows_amd64.syso"

if (Test-Path $ResourceObject) {
    throw "Refusing to overwrite existing generated resource: $ResourceObject"
}
if (Test-Path $Stage) {
    Remove-Item $Stage -Recurse -Force
}
New-Item $Stage -ItemType Directory -Force | Out-Null

try {
    $PreviousPath = $env:PATH
    try {
        $env:PATH = "$(Split-Path $Windres);$env:PATH"
        & $Windres (Join-Path $RepoRoot "df-hud.rc") -O coff -o $ResourceObject
        if ($LASTEXITCODE -ne 0) {
            throw "windres failed to compile the Windows manifest."
        }
    }
    finally {
        $env:PATH = $PreviousPath
    }

    $PreviousCGO = $env:CGO_ENABLED
    $PreviousGOOS = $env:GOOS
    $PreviousGOARCH = $env:GOARCH
    try {
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        Push-Location $RepoRoot
        try {
            & $Go build -trimpath -buildvcs=false `
                -ldflags "-H=windowsgui -X main.version=$Version" `
                -o $Executable ./cmd/df-hud
            if ($LASTEXITCODE -ne 0) {
                throw "go build failed."
            }
        }
        finally {
            Pop-Location
        }
    }
    finally {
        $env:CGO_ENABLED = $PreviousCGO
        $env:GOOS = $PreviousGOOS
        $env:GOARCH = $PreviousGOARCH
    }
}
finally {
    Remove-Item $ResourceObject -Force -ErrorAction SilentlyContinue
}

Copy-Item (Join-Path $RepoRoot "df-hud.example.toml") $Stage
Copy-Item (Join-Path $RepoRoot "LICENSE") $Stage

if (Test-Path $Archive) {
    Remove-Item $Archive -Force
}
Compress-Archive -Path (Join-Path $Stage "*") -DestinationPath $Archive

Write-Host "Built $Executable"
Write-Host "Packaged $Archive"
