# Configuration

You do not need a config file. With none, you get the built-in defaults.

Copy [df-hud.example.toml](../df-hud.example.toml) and keep only the lines you
change. That file comments every key and a test checks it against the defaults,
so it cannot drift.

| | Linux | Windows |
| --- | --- | --- |
| Config | `~/.config/df-hud/config.toml` | `%APPDATA%\df-hud\config.toml` |
| Data (`credentials.json`, `state.json`, `catalog.json`) | `~/.local/share/df-hud` | `%LOCALAPPDATA%\df-hud` |

`--config` picks a different file. On Windows, tray **Open config file** opens
the TOML, and creates it if it is missing.

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

Each group has its own `x` and `y` under `[widget.*]`. Those are pixels from the
top-left of the screen, written for `hud.reference_width` x
`hud.reference_height` (defaults 2560x1440), then scaled to your monitor.
Defaults were set at 2560x1440 against the game's own UI. Block info starts in
the top right.

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

Hotkeys, tray, poll intervals, and the rest of the file are in the example TOML.
See [How to use](usage.md) for the default keys.
