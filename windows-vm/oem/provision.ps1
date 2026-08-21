$ErrorActionPreference = "Stop"
Start-Transcript -Path "C:\OEM\provision.log" -Append

try {
    $temp = "C:\OEM\downloads"
    New-Item $temp -ItemType Directory -Force | Out-Null

    $go = "${env:ProgramFiles}\Go\bin\go.exe"
    if (-not (Test-Path $go)) {
        $goMsi = Join-Path $temp "go1.26.5.windows-amd64.msi"
        Invoke-WebRequest `
            -Uri "https://go.dev/dl/go1.26.5.windows-amd64.msi" `
            -OutFile $goMsi
        $process = Start-Process msiexec.exe -Wait -PassThru -ArgumentList @(
            "/i", "`"$goMsi`"", "/qn", "/norestart"
        )
        if ($process.ExitCode -ne 0) {
            throw "Go installer exited with $($process.ExitCode)"
        }
    }

    $msysRoot = "C:\msys64"
    if (-not (Test-Path "$msysRoot\usr\bin\pacman.exe")) {
        $headers = @{
            "Accept" = "application/vnd.github+json"
            "User-Agent" = "df-hud-windows-builder"
        }
        $release = Invoke-RestMethod `
            -Headers $headers `
            -Uri "https://api.github.com/repos/msys2/msys2-installer/releases/latest"
        $asset = $release.assets |
            Where-Object { $_.name -match "^msys2-x86_64-.*\.sfx\.exe$" } |
            Select-Object -First 1
        if (-not $asset) {
            throw "The latest MSYS2 release has no x86_64 SFX installer"
        }
        $installer = Join-Path $temp $asset.name
        Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $installer
        $process = Start-Process $installer -Wait -PassThru -ArgumentList @(
            "-y", "-oC:\"
        )
        if ($process.ExitCode -ne 0) {
            throw "MSYS2 installer exited with $($process.ExitCode)"
        }
    }

    $bash = "$msysRoot\usr\bin\bash.exe"
    & $bash -lc "pacman -Syu --noconfirm"
    if ($LASTEXITCODE -ne 0) {
        throw "MSYS2 update failed with exit code $LASTEXITCODE"
    }
    $packages = @(
        "mingw-w64-ucrt-x86_64-adwaita-icon-theme",
        "mingw-w64-ucrt-x86_64-gcc",
        "mingw-w64-ucrt-x86_64-gtk4",
        "mingw-w64-ucrt-x86_64-ntldd",
        "mingw-w64-ucrt-x86_64-pkgconf"
    ) -join " "
    & $bash -lc "pacman -S --needed --noconfirm $packages"
    if ($LASTEXITCODE -ne 0) {
        throw "MSYS2 package installation failed with exit code $LASTEXITCODE"
    }

    $desktop = [Environment]::GetFolderPath("CommonDesktopDirectory")
    $shortcutPath = Join-Path $desktop "Build df-hud.lnk"
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = "powershell.exe"
    $shortcut.Arguments = '-NoProfile -ExecutionPolicy Bypass -File "Z:\windows-vm\oem\build-df-hud.ps1"'
    $shortcut.WorkingDirectory = "Z:\"
    $shortcut.Description = "Build the native Windows df-hud release archive"
    $shortcut.Save()

    "Provisioning completed at $(Get-Date -Format o)" |
        Set-Content "C:\OEM\provisioned.txt"
}
finally {
    Stop-Transcript
}
