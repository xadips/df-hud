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

The keys only work while Dead Frontier is focused. So `J` does nothing if you
have alt-tabbed away. The HUD itself hides by workspace, not by focus. A window
in front of the game on the same workspace still has the HUD over it.

A bare key is eaten before the game sees it. Do not bind WASD, Tab, Shift, grave,
the number row, function keys, or a letter the game already uses (`e` `f` `q`
`c` `b` `h` `m` `n` `o` `t` `v` `y` `x` `r` `i` `l` `p`). See
[knowledge/game-keybinds.md](../knowledge/game-keybinds.md).

Linux installs the same keys on Hyprland. Windows uses `RegisterHotKey`. Do not
also bind them in the compositor or AutoHotkey.

### Without Hyprland

On any other layer-shell compositor df-hud grabs nothing, so bind keys yourself
and POST to the loopback listener. Every action above is reachable, and so are
four groups the keys do not cover:

| Action | Request to `http://127.0.0.1:9310` |
| --- | --- |
| City map | `POST /api/widget/map/toggle` |
| Challenge board | `POST /api/widget/challenges/toggle` |
| Restart the run clock | `POST /api/run/start` |
| Reset XP/hr | `POST /api/xp/reset` |
| Show or hide the overlay | `POST /api/overlay/toggle` |
| Other groups | `POST /api/widget/<name>/toggle`, where name is `block`, `bosses`, `session`, or `xp` |

```sh
bind = SUPER, G, exec, curl -fsS -X POST http://127.0.0.1:9310/api/widget/map/toggle
```

One difference matters. The keys df-hud grabs only fire while Dead Frontier is
focused, and it lets go when you alt-tab. A compositor binding fires wherever
you are, so pick keys you will not want elsewhere.

The [city map](widgets/map.md) starts hidden. Press `G` to bring it up.

Hiding a group with a key or the tray lasts until you restart. It is not saved
to the file. `enabled` in the config is what stays. The status banner cannot be
hidden. That is how df-hud tells you it cannot do its job.

## Tray

The tray is how you find df-hud when the overlay is off.

- **Show overlay** / **Show challenges**: same as `J` and `Z`
- **FPS display on launch**: press the game's FPS key after the window appears
- **Skip the launcher**: press Play on the configuration dialog. Turn this off
  when you need the Input tab.
- **Reset xp/hr** / **Restart run clock**: same as `U` and `K`
- **Reload config**: read the TOML again without restarting
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
