# Run clock

`[widget.session]`. Time spent playing, not how long the client has been open.

Launching Dead Frontier means a launcher, a loading screen, then Start. Process
uptime is ahead of any playing, so this clock waits until you actually start:
the client reports a position, you leave an outpost, or you move.

It stops if you die or the game closes (or relaunches). Restarting df-hud
mid-run keeps the same clock. The run is tied to the game's process.

**Grave** (backtick, `hotkeys.run_start`) and the tray item **Restart run clock**
start it from now, for when it started before you did. On a default Dead
Frontier install backtick is also chat. Rebind one of them. See
[How to use](../usage.md#keys).

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off |
| `x`, `y` | `350`, `60` | Position at 2560x1440 |
| `prefix` | `"IC Time: "` | Text before the clock |
| `color` | `#ffffff` | Normal colour |
