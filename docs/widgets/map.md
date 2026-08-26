# City map

`[widget.map]`. The inner city as a grid.

One cell per block, shaded like DFProfiler's map, gaps left empty, district
lines heavier, a label on every active event, and a white ring on the block you
are standing on. The mouse still reaches the game through it. There is no hover.
The list beside the grid is the legend.

It starts hidden. `G` (`hotkeys.map`) brings it up. Show it, pick a walk, hide
it again. `enabled = false` turns the key off too.

Onslaught is not on this grid. The group hides there and the
[bosses](bosses.md) panel covers it.

## Legend

A letter for the kind of place, a number for which one. The numbers are the
game's own event slots (`boss_num`), so `B4` is the same camp all cycle and the
same name DFProfiler's map uses.

| | |
| --- | --- |
| `B1`..`B6` | Bandit camps, easier to harder |
| `I1`..`In` | Inner-city bosses, one enemy type |
| `N1`..`Nn` | Nests, several types on one block |
| `M1`..`M5` | Missions, one per outpost (`M2` is Fort Pastor) |
| `Δ` | Wasteland QRF |
| `▼` | Death Row QRF |
| `DH` `VL` `BH` `LB` | Today's daily: Devil Hound, Volatile Leaper, Behemoth, Legendary Bandits |
| `N` `D` `P` `F` `S` `C` `Z` | Outposts |

A ring only when the letter is not enough: today's daily, a mission, either QRF.
Bandits, bosses, and nests have no ring. The letter already says what they are.
A nest that contains the daily keeps its `N` number and gets the ring.

Two events on one block (a QRF over a boss or bandit camp) share the cell: each
glyph is half size, stacked, the same as DFProfiler's map. The list still has
one row per event.

The list beside the grid is nearest first, one entry per event, not per block.
The feed repeats the same pack on many tiles. Each row is the label and the
enemy types, then a countdown to the right of the name when that event has its
own clock.

Bosses, bandits, and QRFs share the city hour, so that countdown is printed
once, on the nearest of them. Dailies (`DH` `VL` `BH` `LB`) are random spawns
and keep their own time. Nests last about two hours, not one, and keep theirs.
Missions do too.

## Size and crop

`scale` sizes the grid and the list together. `1.0` is 1180 pixels across the
longest side. `radius` crops to a square around you (`15` draws 31x31 blocks)
and zooms in, it does not shrink the picture. Same pixel budget, fewer blocks,
bigger cells. `0` is the whole city.

`center = true` (the default) ignores `x` / `y` and sits on the monitor.
`offset_x` / `offset_y` nudge a centred map (negative is left and up). Set
`center = false` to place it with `x` / `y` like any other group.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off. Off also drops the hotkey. |
| `center` | `true` | Centre on the monitor |
| `offset_x`, `offset_y` | `-100`, `350` | Nudge when centred |
| `x`, `y` | `700`, `240` | Position when `center = false` |
| `radius` | `8` | Crop in blocks. `0` is the full city. |
| `scale` | `0.4` | Size of grid and list together |
| `opacity` | `0.9` | Extra fade on the grid. Markers stay solid. |
| `show_list` | `true` | Legend beside the grid |
| `max_listed` | `10` | Cap. Overflow is `+N more`. |

The shades match DFProfiler's map so the picture looks familiar. Nothing is
worked out from them. See [knowledge/city-map.md](../../knowledge/city-map.md)
for where the city's shape comes from.
