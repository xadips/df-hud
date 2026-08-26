# Manual wiring

df-hud grabs `[hotkeys]` itself on Hyprland and Windows. On any other
layer-shell compositor (Sway, niri, KDE, COSMIC, …) it grabs nothing, so bind
keys yourself and POST to the loopback listener. Most people on Hyprland or
Windows can skip this page. The usual keys and tray are on
[How to use](usage.md).

Every built-in action is reachable, and so are the groups that have no key:

| Action | Request to `http://127.0.0.1:9310` |
| --- | --- |
| City map | `POST /api/widget/map/toggle` |
| Challenge board | `POST /api/widget/challenges/toggle` |
| Restart the run clock | `POST /api/run/start` |
| Reset XP/hr | `POST /api/xp/reset` |
| Show or hide the overlay | `POST /api/overlay/toggle` |
| Other groups | `POST /api/widget/<name>/toggle`, where name is `block`, `bosses`, `session`, `xp`, or `keybinds` |

```sh
bind = SUPER, G, exec, curl -fsS -X POST http://127.0.0.1:9310/api/widget/map/toggle
```

One difference matters. The keys df-hud grabs only fire while Dead Frontier is
focused, and it lets go when you alt-tab. A compositor binding fires wherever
you are, so pick keys you will not want elsewhere.
