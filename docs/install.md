# Install

You need the overlay binary and a userscript that hands df-hud your session.
Without the script, the tray appears but the HUD has nothing to show.

## Download

[Releases](https://github.com/xadips/df-hud/releases) ship two archives:

- `df-hud-<version>-linux-amd64.tar.gz` — Linux binary, example config, systemd user unit
- `df-hud-<version>-windows-amd64.zip` — `df-hud.exe` and the example config

## Linux

Needs a wlroots compositor that speaks layer-shell (Hyprland), plus `libEGL`,
`libGLESv2`, and `libwayland-client`. No GTK. Hotkeys are Hyprland-only.

From a clone:

```sh
make            # cargo build --release, copy to ./df-hud
make install    # ~/.local/bin/df-hud and ~/.config/systemd/user/df-hud.service
make enable     # start now, and at every login
```

From a release tarball, copy the same two files and enable the unit:

```sh
install -Dm755 df-hud ~/.local/bin/df-hud
install -Dm644 df-hud.service ~/.config/systemd/user/df-hud.service
systemctl --user daemon-reload
systemctl --user enable --now df-hud.service
```

`./df-hud` from a terminal still works if you would rather not install it.

Optional Hyprland layer rules (no animation, no blur — not hotkeys) are in
[contrib/df-hud.hypr.conf](../contrib/df-hud.hypr.conf). Source that file from
`hyprland.conf` if you want them. Do not duplicate the overlay keys as compositor
binds; the binary already owns them.

Logs: `journalctl --user -u df-hud -f`, or `make logs` from a clone.

## Windows

Unzip the release and run `df-hud.exe`. There is no extra runtime.

The exe is a GUI app, so Explorer does not open a console. When you launch from
`cmd`, `--version` and friends still print. Otherwise stderr goes to
`%LOCALAPPDATA%\df-hud\df-hud.log`.

Right-click the tray icon and tick **Start df-hud with Windows** if you want it
at login. **Open config file** creates or opens the TOML next to the other
AppData files.

## Session script

df-hud reads the game through a loopback listener on `127.0.0.1:9275`. A
userscript in the browser posts your session there. Install one of:

- **the bridge userscript** (or the bridge userscript) — already posts to
  that endpoint. Versions before *** do not send the signing salt, so the
  challenge board will not work.
- **the bridge userscript** (`the bridge userscript`) — also reports the salt, and
  re-posts every five minutes so a rotated session recovers by itself.

Load the Outpost home page once after installing the script. The overlay does
not start polling until that payload arrives.

## First run

1. Start df-hud. A tray icon should appear (waybar's tray module, or the
   Windows notification area).
2. Load the Outpost page with the userscript installed.
3. Launch Dead Frontier. The overlay shows while the game is running.

If the HUD is missing, [How to use](usage.md) covers visibility and keys.
[Configuration](configuration.md) covers the config file.

## Build from source

Rust stable. `make` is `cargo build --release`. `make check` is
`cargo test --locked`.

Windows from Linux: `make package-windows`. On Windows itself,
`build-windows.ps1 -Version ...`.
