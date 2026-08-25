# How to use

The overlay is click-through: every pointer event reaches the game. It hides
while Dead Frontier is not running (`hud.only_when_game_running`). On Linux it
also follows the game's workspace (`hud.follow_game_workspace`), so it is not
drawn over every other desktop on that monitor.

If the compositor or the game window cannot be identified, the HUD stays
visible rather than vanishing.

## Keys

df-hud registers the chords in `[hotkeys]` **only while the game window is
focused**, and releases them when you alt-tab. Defaults:

| Config key | Default | Action |
| --- | --- | --- |
| `hotkeys.map` | `V` | Show or hide the [city map](widgets/map.md) |
| `hotkeys.challenges` | `T` | Show or hide the [challenge board](widgets/challenges.md) |
| `hotkeys.run_start` | `Grave` (backtick) | Restart the [run clock](widgets/session.md) from now |
| `hotkeys.xp_reset` | `X` | Start the [XP/hr](widgets/xp.md) average again |
| `hotkeys.overlay` | `K` | Show or hide the whole overlay |

Set a value to `""` to leave that action unbound. Chords work (`Ctrl+Shift+M`,
`Alt+F8`, `Win+K`).

The keys exist only while Dead Frontier is focused. The overlay toggle therefore
stops working when you alt-tab off the game — which is sometimes when you want
it, because the HUD is hidden by workspace, not by focus. A window in front of
the game on the same workspace still has the HUD over it.

A bare key is eaten before the game sees it. Do not bind something Dead Frontier
uses (function keys, the number row, chat). On a default install chat is
backtick, which is also the default run-clock key — rebind one of them. See
[knowledge/game-keybinds.md](../knowledge/game-keybinds.md) for where the game
stores its own bindings.

Linux installs the same chords on Hyprland. Windows uses `RegisterHotKey`. Do
not duplicate them with compositor or AutoHotkey binds. The overlay never takes
a keyboard seat.

The [city map](widgets/map.md) starts hidden; `V` summons it.

Hiding a group with a key or the tray lasts until restart. It is not written to
the config. `enabled` in the file is the lasting switch. The status banner
cannot be hidden: it is how df-hud says it cannot do its job.

## Tray

The tray is the process you can see when the overlay is off-screen.

- **Show overlay** / **Show challenges** — same as `K` and `T`
- **FPS display on launch** — press the game's FPS key after the window appears
- **Skip the launcher** — press Play on the configuration dialog (turn this off
  when you need the Input tab)
- **Reset xp/hr** / **Restart run clock** — same as `X` and Grave
- **Reload config** — re-read the TOML without restarting
- **Quit df-hud**

On Windows the menu also has **Open config file** and **Start df-hud with
Windows**. If Discord IPC is enabled but not connected, **Retry Discord IPC
bind** appears.

The tray tick follows the keys: hide the board with `T` and the menu updates.

## Status banner

Top-left by default (`[widget.status]`). Amber when you can fix it (no session
yet, expired credentials — load a Dead Frontier page with the bridge script).
Red when you cannot (the game server is not answering). It has no `enabled` or
`color` key.

## Position lag under Proton

The map ring and block info prefer the game client's own position (Discord rich
presence) over the slower server poll. Under Proton that pipe has to be bridged
inside the game's wineserver. If the ring trails you by tens of seconds, see
[knowledge/presence.md](../knowledge/presence.md).
