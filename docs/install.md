# Install

You need the overlay binary, and you should install
[DF HUD Bridge](https://greasyfork.org/en/scripts/592954-df-hud-bridge). The
userscript is the recommended way to feed df-hud: it syncs your session on its
own, and the challenge board will not work without it.

[`df.user_id`](#without-the-script) is the fallback if you do not want the
script. It is a real option, documented below; it is not a substitute for the
board.

## Download

[Releases](https://github.com/xadips/df-hud/releases) have two archives:

- `df-hud-<version>-linux-amd64.tar.gz`: Linux binary, example config, systemd user unit
- `df-hud-<version>-windows-amd64.zip`: `df-hud.exe` and the example config

## Linux

You need a Wayland compositor that speaks
[wlr-layer-shell](https://wayland.app/protocols/wlr-layer-shell-unstable-v1),
plus `libEGL`, `libGLESv2`, and `libwayland-client`. No GTK, no X11.

Nearly every compositor has it: Hyprland, KWin (KDE), Sway, niri, COSMIC, river,
Wayfire, labwc, Mir, phoc, Treeland, GameScope. The ones that do not are
**Mutter (GNOME)**, **Muffin (Cinnamon)**, and Weston. GNOME has declined to add
it, so df-hud cannot draw over the game there.

df-hud only grabs keys itself on Hyprland. Everything else on that list runs the
overlay fine, and [How to use](usage.md#without-hyprland) covers binding your own
keys to the loopback API.

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

You can also run `./df-hud` from a terminal if you do not want to install it.

Optional Hyprland layer rules (no animation, no blur, not hotkeys) are in
[contrib/df-hud.hypr.conf](../contrib/df-hud.hypr.conf). Source that file from
`hyprland.conf` if you want them. Do not bind the overlay keys in Hyprland
yourself. The binary already owns them.

Logs: `journalctl --user -u df-hud -f`, or `make logs` from a clone.

## Windows

Unzip the release and run `df-hud.exe`. Nothing else to install.

The exe is a GUI app, so Explorer does not open a console. If you launch it
from `cmd`, `--version` and friends still print. Otherwise logs go to
`%LOCALAPPDATA%\df-hud\df-hud.log`.

Right-click the tray icon and tick **Start df-hud with Windows** if you want it
at login. The first start writes `%APPDATA%\df-hud\config.toml` if it is missing.
**Open config file** opens that TOML (and writes the defaults if you deleted it).
**Open log file** opens `%LOCALAPPDATA%\df-hud\df-hud.log`.

## Session script

df-hud listens on `127.0.0.1:9310`. Install
[DF HUD Bridge](https://greasyfork.org/en/scripts/592954-df-hud-bridge) in
Tampermonkey or Violentmonkey. That is the recommended setup: the script posts
your session on the loopback, keeps it in sync when you log in again, and is
what the challenge board needs.

It is the only script that feeds df-hud. Three values live solely inside a
logged-in page's JavaScript (`userID`, a per-session `password` hash that is
not your account password, and `sc`), and the challenge board additionally
needs the page cookie and `skeygen`, the game's request-signing salt. Reporting
the salt from page context is what lets the board keep working when the game
rotates it, with no config edit.

Load the Outpost home page once after you install it. df-hud will not poll
until that arrives. The script re-posts every five minutes, so logging in
again is picked up on its own.

The destination is hardcoded to `127.0.0.1`. Nothing leaves your machine, and
if df-hud is not running the POST just fails and the script goes quiet.

### Without the script

If you do not want the userscript, the game still answers
`get_values.php?userID=<id>` for anyone, with no session. That is the same
public record DFProfiler reads. Point df-hud at it:

```toml
[df]
user_id = "1234567"
```

Digits only, and a wrong one is a startup error rather than a blank HUD. That
gives you block info, the city map ring, the bosses panel, the run clock and
XP/hr. The challenge board still needs a real session and says so in its own
place; the status banner stays quiet, because nothing else is missing. If you
never wanted the board, `widget.challenges.enabled = false` removes that line
too and the HUD says nothing at all.

This is a fallback, not a substitute. The moment the bridge delivers a session
df-hud uses that instead, and you can leave the key in place.

## First run

1. Start df-hud. You should see a tray icon (waybar's tray module, or the
   Windows notification area).
2. Load the Outpost page with the userscript installed. If you are using
   `df.user_id` instead of the script, skip this and expect an amber banner
   about the challenge board.
3. Launch Dead Frontier. The overlay shows while the game is running.

If nothing appears, [How to use](usage.md) covers visibility and keys.
[Configuration](configuration.md) covers the config file.

## Build from source

Rust 1.91 or later (`Cargo.toml` `rust-version`). `make` is
`cargo build --release`. `make check` is `cargo test --locked`.

Windows from Linux: `make package-windows`. On Windows itself,
`build-windows.ps1 -Version ...`.
