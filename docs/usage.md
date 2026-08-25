# How to use

Clicks go through the overlay to the game. The HUD hides while Dead Frontier is
not running (`hud.only_when_game_running`). On Linux it also follows the game's
workspace (`hud.follow_game_workspace`), so it does not sit on every other
desktop on that monitor.

If Hyprland or the game window cannot be found, the HUD stays up rather than
disappearing.

## Keys

df-hud grabs the keys in `[hotkeys]` only while the game window is focused, and
lets go when you alt-tab. Defaults:

| Config key | Default | Action |
| --- | --- | --- |
| `hotkeys.map` | `V` | Show or hide the [city map](widgets/map.md) |
| `hotkeys.challenges` | `T` | Show or hide the [challenge board](widgets/challenges.md) |
| `hotkeys.run_start` | `Grave` (backtick) | Restart the [run clock](widgets/session.md) from now |
| `hotkeys.xp_reset` | `X` | Start the [XP/hr](widgets/xp.md) average again |
| `hotkeys.overlay` | `K` | Show or hide the whole overlay |

Set a value to `""` to leave that one unbound. Chords work (`Ctrl+Shift+M`,
`Alt+F8`, `Win+K`).

The keys only work while Dead Frontier is focused. So `K` does nothing if you
have alt-tabbed away. The HUD itself hides by workspace, not by focus. A window
in front of the game on the same workspace still has the HUD over it.

A bare key is eaten before the game sees it. Do not bind something Dead Frontier
uses (function keys, the number row, chat). On a default install chat is
backtick, and that is also the default run-clock key. Rebind one of them. See
[knowledge/game-keybinds.md](../knowledge/game-keybinds.md) for where the game
stores its own bindings.

Linux installs the same keys on Hyprland. Windows uses `RegisterHotKey`. Do not
also bind them in the compositor or AutoHotkey.

The [city map](widgets/map.md) starts hidden. Press `V` to bring it up.

Hiding a group with a key or the tray lasts until you restart. It is not saved
to the file. `enabled` in the config is what stays. The status banner cannot be
hidden. That is how df-hud tells you it cannot do its job.

## Tray

The tray is how you find df-hud when the overlay is off.

- **Show overlay** / **Show challenges**: same as `K` and `T`
- **FPS display on launch**: press the game's FPS key after the window appears
- **Skip the launcher**: press Play on the configuration dialog. Turn this off
  when you need the Input tab.
- **Reset xp/hr** / **Restart run clock**: same as `X` and Grave
- **Reload config**: read the TOML again without restarting
- **Quit df-hud**

On Windows the menu also has **Open config file** and **Start df-hud with
Windows**. If Discord IPC is on but not connected, **Retry Discord IPC bind**
shows up.

The ticks follow the keys. Hide the board with `T` and the menu updates.

## Status banner

Top left by default (`[widget.status]`). Amber when you can fix it (no session
yet, or it expired: load a Dead Frontier page with the bridge script). Red when
you cannot (the game server is not answering). No `enabled` or `color` key.

## Position lag under Proton

The map ring and block info prefer the game client's own position (Discord rich
presence) over the slower server poll. Under Proton that pipe has to be bridged
inside the game's wineserver. If the ring lags behind you by tens of seconds,
see [knowledge/presence.md](../knowledge/presence.md).
