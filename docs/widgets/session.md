# Run clock

`[widget.session]` — time spent playing, not how long the client has been open.

Launching Dead Frontier means a launcher, a loading screen, then Start. Process
uptime is ahead of any playing, so this clock waits for a play signal: the
client reporting a position, leaving an outpost, or moving.

It ends if you die or the game closes (or relaunches). Restarting df-hud mid-run
resumes the same clock; the run is tied to the game's process.

**Grave** (backtick, `hotkeys.run_start`) and the tray item **Restart run clock**
start it from now, for when it started before you did. On a default Dead
Frontier install backtick is also chat — rebind one of them. See
[How to use](../usage.md#keys).

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | Lasting on/off |
| `x`, `y` | `350`, `60` | Top-left in the [reference resolution](../configuration.md#placement) |
| `prefix` | `"IC Time: "` | Text before the clock |
| `color` | `#ffffff` | Normal colour |
