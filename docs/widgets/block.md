# Block info

`[widget.block]`. Where you are. Top right by default.

Inside an outpost it shows the outpost name. In the city it shows the region
(the game does not print that). Block-support countdown sits on the same group
when it is running.

Coordinates are off by default (`show_position = false`) because the game
already shows them under the minimap. Turn them on if you want to check df-hud
against the client.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off |
| `x`, `y` | `2340`, `300` | Position at 2560x1440. Top right. |
| `color` | `#9ecbff` | Normal colour |
| `show_position` | `false` | Print `x, y` next to the name |

What is standing on the block is a separate group: [Bosses](bosses.md).
