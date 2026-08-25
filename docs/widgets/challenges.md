# Challenge board

`[widget.challenges]`. The board grouped by category: event, yours, then clan.

Each category is a switch, not a row limit:

| Key | Default | What it covers |
| --- | --- | --- |
| `show_repeatable` | `false` | Limited-time event challenges (event currency, not XP) |
| `show_clan` | `true` | Clan challenges. That progress is the clan's, not yours. |
| `show_personal` | `true` | Ordinary dailies and weeklies |
| `show_completed` | `true` | Finished rows from any of the three |
| `show_sections` | `true` | A divider naming each category. Off keeps the groups, without the heading. |

`T` (`hotkeys.challenges`) and the tray item **Show challenges** hide the board
until you restart. That is not saved. `enabled` in the file is what stays.

A finished challenge or objective is green and struck through. An unfinished
one with less than `urgent_within` left (default two hours) is red.

Each challenge is a group: the name on one row, objectives indented under it.
If the name already says the objective, it stays one row. Clan entries look
like that.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off |
| `x`, `y` | `10`, `190` | Position at 2560x1440 |
| `color` | `#e8e8e8` | Normal colour. Green and red still win. |
| `max_shown` | `0` | Row cap. `0` means no cap. |
| `urgent_within` | `7200` | Seconds before an unfinished deadline turns red. `0` turns that off. |
