# Configuration

No file is required. Missing config means the built-in defaults.

Copy [df-hud.example.toml](../df-hud.example.toml) and keep only the lines you
change. That file comments every key and is checked against the defaults in CI,
so it cannot drift.

| | Linux | Windows |
| --- | --- | --- |
| Config | `~/.config/df-hud/config.toml` | `%APPDATA%\df-hud\config.toml` |
| Data (`credentials.json`, `state.json`, `catalog.json`) | `~/.local/share/df-hud` | `%LOCALAPPDATA%\df-hud` |

Override the config path with `--config`. On Windows, tray **Open config file**
opens or creates the TOML.

Unknown keys are a startup error — a typo would otherwise look like a bug in
df-hud. Intervals below their floor are errors too, not silent clamps.

Saving the file reloads it within a second (`systemctl --user reload df-hud`
does the same via SIGHUP). Appearance, positions, intervals, game detection, and
visibility rules all take effect without a restart. These seven keys need a
restart; an edit is reported and the running values are kept:

- `bridge.enabled`, `bridge.listen`
- `paths.data_dir`
- `presence.enabled`, `presence.socket`
- `tray.enabled`
- `hud.layer`

An edit that fails validation keeps the running config rather than taking the
HUD down mid-game.

## Placement

Each group has its own `x` / `y` under `[widget.*]`, in pixels from the
top-left, in the design space `hud.reference_width` × `hud.reference_height`
(defaults 2560×1440). Those numbers are then scaled to your monitor. The
defaults were measured at 2560×1440 against the game's own UI.

A group near the right edge is clipped, not wrapped — a line that reflows would
make everything below it jump. Leave room for the longest string that group can
produce.

`hud.margin_*` insets the whole surface and moves that origin (useful for a bar
at the top). `font_size` and `color` are optional per group; absent means use
`[hud]`. A group's `color` is its normal colour. Colours that carry meaning
still win: the status banner, an outpost attack, bandits on your block, a shaky
XP rate.

`hud.layer` must stay `"overlay"`. The `top` layer sits below fullscreen
windows, so a `top` HUD vanishes when you play.

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

`enabled = false` is the lasting way to drop a group. A key or tray hide is
session-only. The status banner has no `enabled` key.

Hotkeys, tray, polling intervals, and the rest of the file are documented in the
example TOML. See [How to use](usage.md) for the default keys.
