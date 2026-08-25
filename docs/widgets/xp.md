# XP/hr

`[widget.xp]` — experience per hour, averaged over a sliding window.

The default window is five minutes (`window = 300`). XP arrives in lumps; a
window short enough to contain no kill reads as zero. The rate stays blank until
`min_samples` polls have landed (default 3).

Amber means a recent poll missed. Red means several have. Those colours win over
`color`. After a challenge reward dumps a lump into the average, **X**
(`hotkeys.xp_reset`) or the tray item **Reset xp/hr** starts the window again.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | Lasting on/off |
| `x`, `y` | `220`, `85` | Top-left in the [reference resolution](../configuration.md#placement) |
| `prefix` | `"Xp/Hr: "` | Text before the number |
| `color` | `#ffffff` | Normal colour (stability colours still win) |
| `window` | `300` | Averaging window, seconds |
| `min_samples` | `3` | Samples before a rate is shown |

If `window` is too short to hold `min_samples` at the poll interval, it widens
and the startup log says so.
