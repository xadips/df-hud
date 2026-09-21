# Manual wiring

df-hud grabs `[hotkeys]` itself on Hyprland and Windows. On any other
layer-shell compositor (Sway, niri, KDE, COSMIC, …) it grabs nothing, so bind
keys yourself and POST to the loopback listener. Most people on Hyprland or
Windows can skip this page. The usual keys and tray are on
[How to use](usage.md).

Steam Deck Desktop Mode is KDE. The overlay still draws. Window-follow
(hide on another virtual desktop) and the grabbed keys need Hyprland IPC, so
they stay off. A missing-Hyprland line in the log is expected, once, not a
failed install.

Every built-in action is reachable, and so are the groups that have no key:

| Action | Request to `http://127.0.0.1:9310` |
| --- | --- |
| City map | `POST /api/widget/map/toggle` |
| Challenge board | `POST /api/widget/challenges/toggle` |
| Restart the run clock | `POST /api/run/start` |
| Reset XP/hr | `POST /api/xp/reset` |
| Show or hide the overlay | `POST /api/overlay/toggle` |
| Other groups | `POST /api/widget/<name>/toggle`, where name is `block`, `bosses`, `session`, `xp`, or `keybinds` |

Hyprland:

```sh
bind = SUPER, G, exec, curl -fsS -X POST http://127.0.0.1:9310/api/widget/map/toggle
```

KDE Plasma 6 (Steam Deck Desktop Mode): System Settings → Keyboard →
Shortcuts → **Add New** → **Command / URL**. Name it (for example `df-hud
map`), command:

```sh
curl -fsS -X POST http://127.0.0.1:9310/api/widget/map/toggle
```

Then click the shortcut slot and press the key. Repeat for Z / J / K / U
with the rows in the table. The tray toggles the same actions if a shortcut
is too much.

One difference matters. The keys df-hud grabs only fire while Dead Frontier is
focused, and it lets go when you alt-tab. A compositor or KDE binding fires
wherever you are, so pick keys you will not want elsewhere.

Window-follow on KWin is not wired yet. KWin does not speak
`wlr-foreign-toplevel-management` (KDE closed that request as intentional; they
point at `kde-plasma-window-management` instead, which is exclusive to one
client). A later Plasma path would be that protocol or a KWin script over
D-Bus, matching the Proton game by title/`pid`. Until then the HUD stays on
the desktop even if the game is on another Activity or virtual desktop.
