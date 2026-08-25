# City map

`[widget.map]` — the inner city as a grid.

One cell per block, shaded the way DFProfiler's map shades it, gaps left empty,
district lines heavier, an identifier on every active event, and a white ring on
the block you are standing on. The mouse still reaches the game through it;
there is no hover. The key beside the grid is the legend.

**It starts hidden.** `V` (`hotkeys.map`) brings it up. This is something you
summon to decide where to walk and dismiss a few seconds later.
`enabled = false` drops the key as well.

Onslaught is not on this grid; the group hides there and the
[bosses](bosses.md) panel covers it.

## Legend

Letter for what sort of place it is, number for which one — the game's own event
slots (`boss_num`), so `B4` is the same camp all cycle and the same name
DFProfiler's map uses.

| | |
| --- | --- |
| `B1`..`B6` | Bandit camps, ascending into the endgame |
| `I1`..`In` | Inner-city bosses — one enemy type |
| `N1`..`Nn` | Nests — several types on one block |
| `M1`..`M5` | Missions, one per outpost (`M2` is Fort Pastor) |
| `Δ` | A QRF, numbered only when more than one is up |
| `DH` `VL` `BH` `LB` | Today's daily: Devil Hound, Volatile Leaper, Behemoth, Legendary Bandits |
| `N` `D` `P` `F` `S` `C` `Z` | Outposts |

A ring only where a ring says something the identifier does not: today's daily,
a mission, a QRF. Bandits, bosses, and nests have no ring; the letter names
them. A nest that contains the daily keeps its `N` number and gains the ring.

The list beside the grid is sorted nearest-first, one entry per event (not per
block — the feed repeats the same pack on many tiles). Each row is the
identifier, the countdown, and the enemy types.

## Size and crop

`scale` is one number for the grid and the key. `1.0` is 1180 pixels across the
longest side. `radius` crops to a square around you (`15` draws 31×31 blocks)
and **zooms in** rather than shrinking: the same pixel budget over fewer blocks
makes larger cells. `0` is the whole city.

`center = true` (default) ignores `x` / `y` and sits on the monitor.
`offset_x` / `offset_y` nudge a centred map (negative is left and up). Set
`center = false` to place it with `x` / `y` like any other group.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | Lasting on/off (off also drops the hotkey) |
| `center` | `true` | Centre on the monitor |
| `offset_x`, `offset_y` | `-100`, `350` | Nudge when centred |
| `x`, `y` | `700`, `240` | Top-left when `center = false` |
| `radius` | `8` | Crop in blocks; `0` is the full city |
| `scale` | `0.4` | Size of grid and key together |
| `opacity` | `0.9` | Extra fade on the grid (markers stay solid) |
| `show_list` | `true` | Legend beside the grid |
| `max_listed` | `10` | Cap; overflow is `+N more` |

The shades match DFProfiler's map so the picture is recognisable. Nothing is
derived from them. See [knowledge/city-map.md](../../knowledge/city-map.md) for
where the city's shape comes from.
