# XP/hr

`[widget.xp]`. Experience per hour, averaged over a sliding window.

The default window is one minute (`window = 60`). XP arrives in lumps. A
window short enough to hold no kill reads as zero. The rate stays blank until
`min_samples` polls have landed (default 3).

Amber means a recent poll missed. Red means several have. Those colours win
over `color`. After a challenge reward dumps a lump into the average, **X**
(`hotkeys.xp_reset`) or the tray item **Reset xp/hr** starts the window again.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off |
| `x`, `y` | `220`, `80` | Position at 2560x1440 |
| `prefix` | `"Xp/Hr: "` | Text before the number |
| `color` | `#ffffff` | Normal colour. Amber and red still win. |
| `window` | `60` | Averaging window, seconds |
| `min_samples` | `3` | Samples before a rate is shown |

If `window` is too short to hold `min_samples` at the poll interval, it widens
and the startup log says so.
