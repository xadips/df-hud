# Bosses

`[widget.bosses]` — what the city event feed says is on your block.

One row per enemy type, plus a map-wide **OUTPOST ATTACK** line when every
outpost is under attack. Most blocks are empty, so this group is often blank.

When your own block is empty, the nearest event is reported instead:

```
nearest 4 up 1 left  1054, 1015
```

**Up is north** (`y` decreasing). Distance is walking distance around gaps in
the city, not a straight line. If the walk is longer than the directions add up
to, the row says how many blocks:

```
nearest 3 up 1 left, 8 blocks  1015, 1024
```

Anything more than a dozen blocks away is omitted. `show_nearest = false` turns
the row off.

In Onslaught this group becomes the cycle panel (current, last, and next)
instead of the city list. The [city map](map.md) is hidden there; Onslaught is
not on that grid.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | Lasting on/off |
| `x`, `y` | `2240`, `344` | Top-left in the [reference resolution](../configuration.md#placement) |
| `show_nearest` | `true` | Nearest event when this block is empty |

These rows are the longest thing the HUD draws. Place the group with room on the
right; lines clip rather than wrap.
