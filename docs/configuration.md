# Configuration

The first overlay start writes [df-hud.example.toml](../df-hud.example.toml) to
the path below if that file is missing, and fills `hud.reference_width` /
`hud.reference_height` from the current panel so left-side groups sit in
native pixels. The rest are the built-in defaults (a test checks the values).
After that, keys you leave in the file stay pinned; delete the file to get a
fresh copy.

| | Linux | Windows |
| --- | --- | --- |
| Config | `~/.config/df-hud/config.toml` | `%APPDATA%\df-hud\config.toml` |
| Data (`credentials.json`, `state.json`, `catalog.json`) | `~/.local/share/df-hud` | `%LOCALAPPDATA%\df-hud` |
| Log (Explorer launch) | journalctl / stderr | `%LOCALAPPDATA%\df-hud\df-hud.log` |

`--config` picks a different file. On Windows, tray **Open config file** opens
the TOML, and creates it if it is missing. **Open log file** opens the Explorer
stderr log.

A typo in a key name is a startup error, not a silent ignore. An interval below
its floor is an error too, not a quiet bump.

Saving the file reloads it within a second (`systemctl --user reload df-hud`
does the same). Positions, fonts, colours, intervals, and visibility all change
without a restart. These seven keys need a restart. df-hud logs that and keeps
the old values:

- `bridge.enabled`, `bridge.listen`
- `paths.data_dir`
- `presence.enabled`, `presence.socket`
- `tray.enabled`
- `hud.layer`

A bad edit keeps the running config. The HUD does not go down mid-game for that.

## Placement

Each group has its own `x` and `y` under `[widget.*]`, written for
`hud.reference_width` x `hud.reference_height` (defaults 2560x1440), then
scaled to your monitor. `y` is from the top. `x` is from the left unless
`anchor = "right"`, then `x` is an inset from `reference_width`. Block info
and the boss list use that so they stay on the right if you set the reference
to your panel (1920x1200 and so on).

A group near the right edge is clipped, not wrapped. A wrapping line would make
everything below it jump. Leave room for the longest string that group can show.

`hud.margin_*` shrinks the surface from each edge and moves that origin, useful
if you have a bar at the top. `font_size` and `color` are optional per group.
Leave them off to use `[hud]`. `color` is the normal colour. Status red/amber,
outpost attack, bandits, and a shaky XP rate still override it.

`hud.layer` must stay `"overlay"`. `"top"` sits under fullscreen windows, so the
HUD vanishes when you play.

## Groups

| Table | Page |
| --- | --- |
| `[widget.block]` | [Block info](widgets/block.md) |
| `[widget.bosses]` | [Bosses](widgets/bosses.md) |
| `[widget.session]` | [Run clock](widgets/session.md) |
| `[widget.xp]` | [XP/hr](widgets/xp.md) |
| `[widget.map]` | [City map](widgets/map.md) |
| `[widget.challenges]` | [Challenge board](widgets/challenges.md) |
| `[widget.status]` | [How to use](usage.md#status-banner) |

`enabled = false` turns a group off for good. Hiding it with a key or the tray
lasts until restart. The status banner has no `enabled` key.

## Switches

These sit next to the groups and are easy to miss if you only read the widget
pages. The example TOML comments them in place.

| Key | Default | |
| --- | --- | --- |
| `hud.enabled` | `true` | Master overlay switch. Tray, `J`, and the HTTP toggle cannot turn it back on. |
| `hotkeys.enabled` | `true` | All grabbed keys. Empty strings still turn individual bindings off. |
| `hud.only_when_game_running` | `true` | Hide the pixels when the game is not running. |
| `poll.only_when_game_running` | `true` | Stop asking the game server when the game is not running. Separate from the HUD hide. |

Hotkeys, tray, poll intervals, and the rest of the file are in the example TOML.
See [How to use](usage.md) for the default keys.
