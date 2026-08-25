# Block info

`[widget.block]` — where you are.

Inside an outpost it shows the outpost name. In the city it shows the region
(the game does not print that itself). Block-support countdown sits on the same
group when it is running.

Coordinates are off by default (`show_position = false`) because the game already
shows them under its minimap. Turn them on to check df-hud against the client.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | Lasting on/off |
| `x`, `y` | `2340`, `300` | Top-left in the [reference resolution](../configuration.md#placement) |
| `color` | `#9ecbff` | Normal colour |
| `show_position` | `false` | Print `x, y` next to the name |

What is standing on the block is a separate group: [Bosses](bosses.md).
