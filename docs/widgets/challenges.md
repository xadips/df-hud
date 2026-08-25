# Challenge board

`[widget.challenges]` — the whole board, in the game's own order.

Each category is a switch, not a cap on rows:

| Key | Default | What it covers |
| --- | --- | --- |
| `show_repeatable` | `false` | Limited-time event challenges (they pay event currency, not XP) |
| `show_clan` | `true` | Your clan's challenges — the clan's progress, not yours |
| `show_personal` | `true` | Ordinary dailies and weeklies |
| `show_completed` | `true` | Cuts across all three: rows that will not change again this cycle |
| `show_sections` | `true` | A divider naming each category |

`T` (`hotkeys.challenges`) and the tray item **Show challenges** hide the board
until restart. That is not written to the file; `enabled` is the lasting switch.

A finished challenge or objective is **green and struck through**. An unfinished
one with less than `urgent_within` left (default two hours) is **red**.

Each challenge is a group: the name on one row, its objectives indented
underneath. A challenge whose name already says its objective stays one row
(clan entries read that way).

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | Lasting on/off |
| `x`, `y` | `10`, `190` | Top-left in the [reference resolution](../configuration.md#placement) |
| `color` | `#e8e8e8` | Normal colour (green/red still win) |
| `max_shown` | `0` | Row cap; `0` is no cap |
| `urgent_within` | `7200` | Seconds before an unfinished deadline turns red; `0` disables |
