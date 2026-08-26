# Run clock

`[widget.session]`. Time spent playing, not how long the client has been open.

Launching Dead Frontier means a launcher, a loading screen, then Start. Process
uptime is ahead of any playing, so this clock waits until you actually start.
The client's own position (Discord rich presence) wins when it is fresh: a
city block or an outpost. If that pipe is quiet, the next poll that sees you
leave an outpost or move starts it instead.

It stops if you die or the game closes (or relaunches). Restarting df-hud
mid-run keeps the same clock. The run is tied to the game's process.

**K** (`hotkeys.run_start`) and the tray item **Restart run clock**
start it from now, for when it started before you did.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off |
| `x`, `y` | `350`, `60` | Position at 2560x1440 |
| `prefix` | `"IC Time: "` | Text before the clock |
| `color` | `#ffffff` | Normal colour |
