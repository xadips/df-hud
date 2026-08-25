# df-hud

A native heads-up display for Dead Frontier on Windows and Hyprland.

It draws over the game — including fullscreen — and passes every pointer event
through to the client underneath. Data comes from the game's own API and
DFProfiler's event map. There is no screen scraping and no OCR.

Replaces SilverOverlays, a Windows PyQt app that under Proton gives no reliable
always-on-top over a fullscreen game, no global hotkeys on Wayland, and no tray.

![df-hud overlay on Dead Frontier](docs/images/overlay.png)

## Features

- **[Block info](docs/widgets/block.md)** — region or outpost, and block support
- **[Bosses](docs/widgets/bosses.md)** — what is standing on your block, or the nearest event
- **[Run clock](docs/widgets/session.md)** — time spent playing, not how long the client has been open
- **[XP/hr](docs/widgets/xp.md)** — a five-minute average
- **[Challenge board](docs/widgets/challenges.md)** — the whole board, filtered by category
- **[City map](docs/widgets/map.md)** — the inner city, summoned with a key

Each group sits where you put it. The map starts hidden.

## Keys

Registered only while Dead Frontier is focused. Defaults:

| Key | Action |
| --- | --- |
| `V` | City map |
| `T` | Challenge board |
| `` ` `` (Grave) | Restart the run clock |
| `X` | Reset the XP/hr average |
| `K` | Show or hide the overlay |

Change them in `[hotkeys]`. Empty unbinds. See [How to use](docs/usage.md).

## Install

Windows, or Linux with a wlroots compositor that speaks layer-shell. Hotkeys on
Linux need Hyprland.

**[Install](docs/install.md)** · **[How to use](docs/usage.md)** · **[Configuration](docs/configuration.md)**

GPL-3.0. How the feeds were measured lives in [knowledge/](knowledge/).
