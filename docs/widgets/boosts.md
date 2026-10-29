# Boosts

`[widget.boosts]`. Boost timers. The game client does not show them, only
DFProfiler does. Sits under [bosses](bosses.md) on the right.

Normal boosts and EX boosts each cover movement speed, damage and XP. A
trinity boost covers all three and gives every stat the same expiry, so one
row stands for it:

```
Trinity      2h 10m
Trinity EX      35m
```

Stats only get their own rows when their expiries differ. The row then names
them:

```
EXP+DMG EX      35m
SPEED EX     1h 02m
```

Timers right-align in a column as wide as `35m 05s`, so it holds still when a
countdown drops its seconds on the minute.

The Ultra Boost is its own item, not a trinity boost: it sells as 262980 hours
(30 years) on all three stats. That runs past 2038, so the record stores the
32-bit "never expires" value, and the row is just `UB: Yes`. It is the only
permanent boost; there is no EX version, so EX boosts always count down,
stacked under it. Expired boosts are left off, and the group is
blank with none active.

It starts shown. There is no hotkey. Tray **Show boosts** and `POST
/api/widget/boosts/toggle` hide it for this run, `enabled = false` for good.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off |
| `anchor` | `right` | `x` is an inset from `hud.reference_width` |
| `x`, `y` | `320`, `494` | Lined up with bosses, below five rows of them |
| `color` | `#c9a8ff` | Row names; timers are white |
| `show_ultra` | `true` | `false` drops the `UB: Yes` row |

Five bosses rows fit above the default spot: a full Onslaught panel (title,
prev, ended, now, next). More than that (OUTPOST ATTACK on top of the panel, a
second event in one cycle, a big nest on a city block) runs into the boost
rows. Raise `y` if you see it. Below, the keybinds start low enough for six
boost rows, the most there can be (both tiers split per stat).
