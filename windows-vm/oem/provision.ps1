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
            Where-Object { $_.name -match "^msys2-base-x86_64-.*\.sfx\.exe$" } |
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
        "mingw-w64-ucrt-x86_64-gobject-introspection",
        "mingw-w64-ucrt-x86_64-gobject-introspection-runtime",
        "mingw-w64-ucrt-x86_64-gtk4",
        "mingw-w64-ucrt-x86_64-ntldd",
        "mingw-w64-ucrt-x86_64-pkgconf"
    ) -join " "
    & $bash -lc "pacman -S --needed --noconfirm $packages"
    if ($LASTEXITCODE -ne 0) {
        throw "MSYS2 package installation failed with exit code $LASTEXITCODE"
    }

    $hostKey = "Z:\windows-vm\.ssh\id_ed25519.pub"
    if (-not (Test-Path $hostKey)) {
        throw "Host SSH key not found at $hostKey; run 'make windows-vm-key' on Linux"
    }
    $sshCapability = Get-WindowsCapability -Online |
        Where-Object { $_.Name -like "OpenSSH.Server*" } |
        Select-Object -First 1
    if (-not $sshCapability) {
        throw "Windows did not report the OpenSSH.Server capability"
    }
    if ($sshCapability.State -ne "Installed") {
        Add-WindowsCapability -Online -Name $sshCapability.Name | Out-Null
    }
    Set-Service sshd -StartupType Automatic
    Start-Service sshd
    if (-not (Get-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule `
            -Name "OpenSSH-Server-In-TCP" `
            -DisplayName "OpenSSH Server (sshd)" `
            -Enabled True `
            -Direction Inbound `
            -Protocol TCP `
            -Action Allow `
            -LocalPort 22 | Out-Null
    }
    $sshDirectory = Join-Path $env:ProgramData "ssh"
    $authorizedKeys = Join-Path $sshDirectory "administrators_authorized_keys"
    New-Item $sshDirectory -ItemType Directory -Force | Out-Null
    $keyText = (Get-Content $hostKey -Raw).Trim() + "`n"
    [IO.File]::WriteAllText($authorizedKeys, $keyText, [Text.Encoding]::ASCII)
    & icacls.exe $authorizedKeys /inheritance:r /grant "*S-1-5-32-544:F" /grant "SYSTEM:F" | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Could not secure $authorizedKeys"
    }

    Copy-Item "Z:\windows-vm\oem\build-df-hud.ps1" "C:\OEM\build-df-hud.ps1" -Force
    Copy-Item "Z:\windows-vm\build.cmd" "C:\OEM\build.cmd" -Force
    $desktop = [Environment]::GetFolderPath("CommonDesktopDirectory")
    $shortcutPath = Join-Path $desktop "Build df-hud.lnk"
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = "C:\OEM\build.cmd"
    $shortcut.WorkingDirectory = "C:\OEM"
    $shortcut.Description = "Build the native Windows df-hud release archive"
    $shortcut.Save()

    "Provisioning completed at $(Get-Date -Format o)" |
        Set-Content "C:\OEM\provisioned.txt"
}
finally {
    Stop-Transcript
}
