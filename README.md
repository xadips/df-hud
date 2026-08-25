# df-hud

A HUD overlay for Dead Frontier on Windows and Hyprland.

It sits on top of the game, including fullscreen, and clicks go through to the
game. It reads the game's API and DFProfiler's event map. No screen scraping,
no OCR.

![df-hud overlay on Dead Frontier](docs/images/overlay.png)

## Features

- [Block info](docs/widgets/block.md): region or outpost, and block support. Top right.
- [Bosses](docs/widgets/bosses.md): what is on your block, or the nearest event
- [Run clock](docs/widgets/session.md): time spent playing, not how long the client has been open
- [XP/hr](docs/widgets/xp.md): a one-minute average
- [Challenge board](docs/widgets/challenges.md): the whole board, filtered by category
- [City map](docs/widgets/map.md): the inner city. Press a key to show it

Each group sits where you put it. The map starts hidden.

## Keys

These only work while Dead Frontier is focused.

| Key | Action |
| --- | --- |
| `V` | City map |
| `T` | Challenge board |
| `` ` `` (Grave) | Restart the run clock |
| `X` | Reset the XP/hr average |
| `K` | Show or hide the overlay |

Change them in `[hotkeys]`. An empty value turns that one off. See
[How to use](docs/usage.md).

## Install

Windows, or Linux with a wlroots compositor that speaks layer-shell. On Linux
the hotkeys need Hyprland.

You also need
[DF HUD Bridge](https://greasyfork.org/en/scripts/592954-df-hud-bridge), a
userscript that hands df-hud your session on `127.0.0.1`. Without it the
overlay runs but has nothing to show, unless you set `df.user_id`, which covers
everything except the challenge board.

[Install](docs/install.md), [how to use](docs/usage.md),
[configuration](docs/configuration.md).

GPL-3.0. Notes on the game feeds are in [knowledge/](knowledge/).
