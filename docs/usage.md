# How to use

Clicks go through the overlay to the game. The HUD hides while Dead Frontier is
not running (`hud.only_when_game_running`). That is pixels only.
`poll.only_when_game_running` is the matching network switch: with it on,
closing the game means zero requests. On Linux the overlay also follows the
game's workspace (`hud.follow_game_workspace`), so it does not sit on every
other desktop on that monitor.

`hud.enabled = false` turns the overlay off for good. Tray, `J`, and the HTTP
toggle cannot override that.

If Hyprland or the game window cannot be found, the HUD stays up rather than
disappearing.

## Widgets

Each group sits where you put it. Placement, colours, and `enabled` are on
[Configuration](configuration.md). What each line means is on the page in the
table.

| Group | Starts | Key | Page |
| --- | --- | --- | --- |
| Block info | shown | — | [block](widgets/block.md) |
| Bosses | shown | — | [bosses](widgets/bosses.md) |
| Keybinds | shown | tray only | [keybinds](widgets/keybinds.md) |
| Run clock | shown | `K` restarts | [session](widgets/session.md) |
| XP/hr | shown | `U` resets | [xp](widgets/xp.md) |
| City map | hidden | `G` | [map](widgets/map.md) |
| Challenge board | shown | `Z` | [challenges](widgets/challenges.md) |
| Status banner | always | cannot hide | [Status banner](#status-banner) |

A key or the tray toggles a group. That is not saved to the file. Restart puts
groups back to how they start (map hidden, the rest shown). `enabled` in the
config is what stays. The status banner cannot be hidden. That is how df-hud
tells you it cannot do its job.

## Keys

df-hud grabs the keys in `[hotkeys]` only while the game window is focused, and
lets go when you alt-tab. Defaults:

| Config key | Default | Action |
| --- | --- | --- |
| `hotkeys.map` | `G` | Show or hide the [city map](widgets/map.md) |
| `hotkeys.challenges` | `Z` | Show or hide the [challenge board](widgets/challenges.md) |
| `hotkeys.run_start` | `K` | Restart the [run clock](widgets/session.md) from now |
| `hotkeys.xp_reset` | `U` | Start the [XP/hr](widgets/xp.md) average again |
| `hotkeys.overlay` | `J` | Show or hide the whole overlay |

Set a value to `""` to leave that one unbound. Chords work (`Ctrl+Shift+M`,
`Win+K`) but a modifier does not hide the letter from Dead Frontier, so the
defaults are unmodified keys the game does not use. `hotkeys.enabled = false`
turns the whole grab off.

The [keybinds](widgets/keybinds.md) group prints these bindings on the overlay.

The keys only work while Dead Frontier is focused. So `J` does nothing if you
have alt-tabbed away. The HUD itself hides by workspace, not by focus. A window
in front of the game on the same workspace still has the HUD over it.

A bare key is eaten before the game sees it. Do not bind WASD, Tab, Shift, grave,
the number row, function keys, or a letter the game already uses (`e` `f` `q`
`c` `b` `h` `m` `n` `o` `t` `v` `y` `x` `r` `i` `l` `p`). See
[knowledge/game-keybinds.md](../knowledge/game-keybinds.md).

Linux installs the same keys on Hyprland. Windows uses `RegisterHotKey`. Do not
also bind them in the compositor or AutoHotkey. On any other compositor, bind
them yourself: [Manual wiring](manual-wiring.md).

## Tray

The tray is how you find df-hud when the overlay is off.

- **Show overlay** / **Show challenges**: same as `J` and `Z`
- **Show keybinds**: the overlay hotkey cheat sheet. Starts shown. No key for this group.
- **FPS display on launch**: press the game's FPS key after the window appears
- **Skip the launcher**: press Play on the configuration dialog. Turn this off
  when you need the Input tab.
- **Reset xp/hr** / **Restart run clock**: same as `U` and `K`
- **Reload config**: read the TOML again without restarting
- **Check for updates**: one probe of the GitHub release page, only when
  clicked; a newer version opens that page in the browser. df-hud never
  checks on its own.
- **Quit df-hud**

On Windows the menu also has **Open config file**, **Open log file**, and
**Start df-hud with Windows**. If Discord IPC is on but not connected, **Retry
Discord IPC bind** shows up.

The ticks follow the keys. Hide the board with `Z` and the menu updates.

## Status banner

Top left by default (`[widget.status]`). Amber when you can fix it (no session
yet, or it expired: load a Dead Frontier page with the bridge script). Red when
you cannot (the game server is not answering). No `enabled` or `color` key.

Group-specific trouble stays with its group rather than coming up here. An
empty challenge board explains itself where the board would be, so turning
challenges off with `Z` or `enabled = false` takes that message with it. The
banner is for what stops df-hud as a whole.

## Position lag under Proton

The map ring and block info prefer the game client's own position (Discord rich
presence) over the slower server poll. Under Proton that pipe has to be bridged
inside the game's wineserver. If the ring lags behind you by tens of seconds,
see [knowledge/presence.md](../knowledge/presence.md).
