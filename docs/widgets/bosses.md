# Bosses

`[widget.bosses]`. What is on your block. Sits under [block info](block.md) on
the right.

![Bosses](../images/bosses.png)

One row per enemy type, plus **OUTPOST ATTACK** with the time left while the
outposts are under attack. Attacks last 20 minutes, every three hours. For the
last `outpost_warning` seconds before one, an amber **Next outpost attack** row
counts down to it instead. The warning stays out of Onslaught. Both rows go
away with `show_outpost_attack = false` (tray **Show outpost attacks**) or
`[bossmap] enabled = false`. Most blocks are empty, so this group is often
blank.

When your own block is empty, it shows the nearest event instead:

```
nearest 4 up 1 left  1054, 1015
```

Up is north (`y` going down). Distance is walking distance around gaps, not a
straight line. If the walk is longer than the directions add up to, the row
says how many blocks:

```
nearest 3 up 1 left, 8 blocks  1015, 1024
```

Anything more than a dozen blocks away is left off. `show_nearest = false`
turns the row off.

In Onslaught this group becomes the cycle panel (prev, now, and next)
instead of the city list. The [city map](map.md) hides there. Onslaught is not
on that grid.

![Onslaught cycles](../images/onslaught.png)

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off |
| `anchor` | `right` | `x` is an inset from `hud.reference_width` |
| `x`, `y` | `320`, `384` | 320px in from the right, under block info |
| `show_nearest` | `true` | Nearest event when this block is empty |
| `show_outpost_attack` | `true` | OUTPOST ATTACK and its countdown. Tray **Show outpost attacks** |
| `outpost_warning` | `900` | Seconds before an outpost attack to show its countdown; `0` is off, at most `10800` |

These rows are the longest thing the HUD draws. Leave room on the right. Lines
clip, they do not wrap.
