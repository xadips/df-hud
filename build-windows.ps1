[CmdletBinding()]
param(
    [string]$OutputDirectory = "dist",
    [string]$Version = "0.1.0-windows"
)

$ErrorActionPreference = "Stop"
$RepoRoot = $PSScriptRoot
$MsysRoot = if ($env:MSYS2_ROOT) { $env:MSYS2_ROOT } else { "C:\msys64" }
$UcrtRoot = Join-Path $MsysRoot "ucrt64"
$UcrtBin = Join-Path $UcrtRoot "bin"
$Pacman = Join-Path $MsysRoot "usr\bin\pacman.exe"

if (-not (Test-Path $Pacman)) {
    throw "MSYS2 was not found at $MsysRoot. Install MSYS2 or set MSYS2_ROOT."
}
if (-not (Test-Path $UcrtBin)) {
    throw "The MSYS2 UCRT64 environment was not found at $UcrtRoot."
}

$RequiredPackages = @(
    "mingw-w64-ucrt-x86_64-gcc",
    "mingw-w64-ucrt-x86_64-pkgconf",
    "mingw-w64-ucrt-x86_64-gtk4",
    "mingw-w64-ucrt-x86_64-adwaita-icon-theme",
    "mingw-w64-ucrt-x86_64-ntldd"
)
$MissingPackages = @()
foreach ($Package in $RequiredPackages) {
    & $Pacman -Q $Package *> $null
    if ($LASTEXITCODE -ne 0) {
        $MissingPackages += $Package
    }
}
if ($MissingPackages.Count -ne 0) {
    $Names = $MissingPackages -join " "
    throw "Missing MSYS2 UCRT64 packages. Install them with: pacman -S --needed $Names"
}

$env:PATH = "$UcrtBin;$env:PATH"
$env:CGO_ENABLED = "1"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CC = Join-Path $UcrtBin "gcc.exe"

$GoCommand = Get-Command "go.exe" -ErrorAction SilentlyContinue
$Go = if ($GoCommand) {
    $GoCommand.Source
} else {
    Join-Path $env:ProgramFiles "Go\bin\go.exe"
}
$PkgConfig = Join-Path $UcrtBin "pkg-config.exe"
$Windres = Join-Path $UcrtBin "windres.exe"
$Ntldd = Join-Path $UcrtBin "ntldd.exe"
foreach ($Tool in @($Go, $PkgConfig, $Windres, $Ntldd, $env:CC)) {
    if (-not (Test-Path $Tool)) {
        throw "Required UCRT64 tool was not found: $Tool"
    }
}

& $PkgConfig --exists gtk4
if ($LASTEXITCODE -ne 0) {
    throw "pkg-config cannot resolve gtk4 in the UCRT64 environment."
}

if (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $RepoRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$Stage = Join-Path $OutputDirectory "df-hud-windows-amd64"
$Archive = Join-Path $OutputDirectory "df-hud-windows-amd64.zip"
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
    Push-Location $RepoRoot
    try {
        & $Windres (Join-Path $RepoRoot "df-hud.rc") -O coff -o $ResourceObject
        if ($LASTEXITCODE -ne 0) {
            throw "windres failed to compile the Windows manifest."
        }

        & $Go build -trimpath `
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
    Remove-Item $ResourceObject -Force -ErrorAction SilentlyContinue
}

Copy-Item (Join-Path $RepoRoot "df-hud.exe.manifest") $Stage
Copy-Item (Join-Path $RepoRoot "df-hud.example.toml") $Stage
Copy-Item (Join-Path $RepoRoot "LICENSE") $Stage

function Copy-RuntimeTree {
    param([string]$RelativePath)

    $Source = Join-Path $UcrtRoot $RelativePath
    if (-not (Test-Path $Source)) {
        return
    }
    $Destination = Join-Path $Stage $RelativePath
    New-Item (Split-Path $Destination) -ItemType Directory -Force | Out-Null
    Copy-Item $Source $Destination -Recurse -Force
}

# GTK loads these at runtime rather than linking every file into the executable,
# so the PE dependency scanner alone cannot discover them.
foreach ($Tree in @(
    "etc\gtk-4.0",
    "lib\gdk-pixbuf-2.0",
    "lib\gtk-4.0",
    "share\glib-2.0\schemas",
    "share\gtk-4.0",
    "share\icons\Adwaita",
    "share\icons\hicolor"
)) {
    Copy-RuntimeTree $Tree
}

function Copy-LinkedDlls {
    param([string[]]$Roots)

    $Queue = [Collections.Generic.Queue[string]]::new()
    $Seen = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::OrdinalIgnoreCase
    )
    foreach ($Root in $Roots) {
        $Queue.Enqueue($Root)
    }

    while ($Queue.Count -gt 0) {
        $Binary = $Queue.Dequeue()
        if (-not $Seen.Add($Binary)) {
            continue
        }
        $Lines = & $Ntldd -R $Binary 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw "ntldd failed while inspecting $Binary"
        }
        foreach ($Line in $Lines) {
            $Match = [regex]::Match(
                [string]$Line,
                "=>\s+([A-Za-z]:[/\\][^ ]+?\.dll)\s+\("
            )
            if (-not $Match.Success) {
                $Match = [regex]::Match(
                    [string]$Line,
                    "^\s*([A-Za-z]:[/\\][^ ]+?\.dll)\s+\("
                )
            }
            if (-not $Match.Success) {
                continue
            }
            $Dependency = [IO.Path]::GetFullPath(
                $Match.Groups[1].Value.Replace("/", "\")
            )
            if (-not $Dependency.StartsWith(
                $UcrtBin,
                [StringComparison]::OrdinalIgnoreCase
            )) {
                continue
            }
            $Target = Join-Path $Stage ([IO.Path]::GetFileName($Dependency))
            if (-not (Test-Path $Target)) {
                Copy-Item $Dependency $Target
            }
            $Queue.Enqueue($Dependency)
        }
    }
}

$RuntimeBinaries = @($Executable)
$RuntimeBinaries += Get-ChildItem $Stage -Recurse -Filter "*.dll" |
    ForEach-Object { $_.FullName }
Copy-LinkedDlls $RuntimeBinaries

if (Test-Path $Archive) {
    Remove-Item $Archive -Force
}
Compress-Archive -Path (Join-Path $Stage "*") -DestinationPath $Archive

Write-Host "Built $Executable"
Write-Host "Packaged $Archive"
