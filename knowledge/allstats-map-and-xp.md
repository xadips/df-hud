# What the public allstats feed actually contains

Source: `get_allstats.php?printvars=1` (unauthenticated, ~1MB, ~35,900 keys).
Census taken 2026-08-11 against `testdata/allstats.txt`.

This note records what was measured, not what was assumed. Every number below is
pinned by a test in `catalog_test.go`, so if the feed changes, the tests say so.

## XP table: `exp_lvl<N>`

414 keys, `exp_lvl2` .. `exp_lvl415`. There is no `exp_lvl1`. `exp_lvl2 = 125`,
`exp_lvl415 = 176000000`. (One unrelated `exp_` key exists: `exp_connection_good`.)

**`exp_lvl<N>` is the XP needed *within* level N-1 to reach level N** — not a
cumulative total. This is settled by the game's own code, not inferred:

- `base.js:1848` calls `checkIfLevelUp(EXPTABLE_exp_lvl[level+1], df_exp, ...)`
- `base.js:475` on level-up writes `df_exp = currentExp - neededExp`

So `df_exp` is progress inside the current level and resets (to the carry-over)
every level-up. The sidebar renders it as `df_exp / exp_lvl[level+1]`
(`base.js:1660-1662`).

Consequence for XP/hr: a delta on `df_exp` goes **negative once per level**. The
cumulative counter is `sum(exp_lvl 2..level) + df_exp`, which is continuous by
construction — the XP that disappears from `df_exp` reappears in the prefix sum.
`TestCumulativeXPIsContinuousAcrossLevelUp` pins that gaining one XP across a
level boundary moves the cumulative total by exactly 1.

`df_exptotal` is referenced **nowhere** in the saved web client, so do not count
on it existing in `get_values`; the reconstruction above is the reliable tier.

Level 415 is the cap, hardcoded in `checkIfLevelUp` (`curLevel < 415`).

### When the reset actually happens, and why it is not a mid-run problem

- **The game client never levels you up.** `checkIfLevelUp` is called from the
  *website's* sidebar render, gated on `checkstuff === "1"`
  (`base.js:1846-1848`). The transaction happens on a page load, i.e. when you
  die or return to an outpost — never mid-fight.
- **It also requires `freePoints === 0`**, so with unspent stat points `df_exp`
  sits above the threshold indefinitely and no level-up occurs at all. Any
  "about to level" display would be wrong without checking freepoints.
- **`df_exp` therefore overshoots, sometimes hugely.** A long run can bank 300M
  XP against a 7M threshold, and coming back cashes it in as ~20 levels at once
  (one `checkIfLevelUp` call per level, each carrying the remainder over).

So during play `df_exp` is already a monotonically rising counter, and a plain
delta would work for the duration of a run. The reconstruction still earns its
keep at the boundary: **it is invariant to a multi-level jump**, because whatever
the prefix sum gains from the levels, `df_exp` loses. Pinned by
`TestCumulativeXPSurvivesAMultiLevelJump`, which banks 600M at level 200,
replays the website's per-level carry-over to level 235, and asserts the
cumulative total does not move.

At level 415, the cap, there are no level-ups at all and a raw `df_exp` delta is
correct on its own.

**Future widget idea this exposes:** "levels pending" — with a banked `df_exp`
and the threshold table, df-hud can say how many levels you will gain when you
next reach an outpost, which nothing in the game shows you while you are out.

### Independent confirmation of the formula

DFProfiler publishes a cumulative `total_exp` in `POST /profile/json/<pid>`, and
its value matches this reconstruction:

```
sum(exp_lvl 2..415)         = 16,576,044,375
+ in-level exp (level 415)  =  df_exp
                            = table reconstruction
DFProfiler total_exp        = table reconstruction + 393,591   (gap 393,591, ~0.0016%)
```

The gap is one scrape-timing difference between their two fields (a few hundred
thousand XP is a single boss at that level), not a formula difference. So
`total_exp = sum(exp_lvl 2..level) + df_exp` is what an independent
implementation also computes.

Their `files/trackers/estimates.php?pid=<id>` page is a 30s poll of that JSON
with a delta between changes and a reset after 300s of no change — the same
shape as df-hud's XP widget, but sourced from their scrape rather than live game
data.

## Neighbourhood names: `zones_<x>_<y>_name`

169 keys, a complete 13x13 grid, 1-based. 160 distinct names — a few repeat
(`Dallwood`, `Ravenhurst`, `Wallmill`, `Molebow`, `Anstenbow`, `Shackelstable`,
`South Moorhurst`, `Overhill`). `zones_1_1_name = Staleston`.

Some values have a trailing space (`Dawntown `, `Moorton `, `Ravenhurst `,
`Rottmill `, `Terraston `, `Wallsley `). Trim before display.

## City blocks: `areas_<x>_<y>_<subkey>`

A complete 39x39 grid = 1521 cells, 1-based. Note 39 = 3 x 13, so each named
neighbourhood covers exactly a 3x3 block of areas.

| subkey | cells | notes |
|---|---|---|
| `building_size` | 1521 | **always `0`** — carries no information, not stored |
| `building` | 1014 | 15 distinct values |
| `building_direction` | 1014 | north/south/east/west, which way the entrance faces |
| `type` | 958 | **always `street`** — a flag, not an enumeration |
| `helicopter` | 1 | `areas_25_26_helicopter = 1` |

Every street cell also has a building (overlap is exactly 958), so street and
building are **not alternatives**: the building is what you can enter from that
street. 56 cells have a building but no street. 507 cells say nothing at all, so
"no data for this block" is a normal answer, not an error.

Building values, by frequency: apartments 162, supermarket 123, offices 102,
hotel 86, restaurant 82, public_toilet 81, fancyrestaurant 78, warehouse 64,
electronic_boutique 45, hardware_boutique 36, clothes_boutique 36,
misc_boutique 35, sports_boutique 34, warehouse_small 33, gun_boutique 17.

## The unsolved part: positions do not map onto this grid yet

`df_positionx/y` are ~1000-centred. The seven outposts (`newoutpost.js:13-36`)
are at (1000,1000), (1005,985), (1012,1019), (1029,1003), (1054,987),
(1032,985), (1058,1019) — an x spread of 58 and a y spread of 34, and observed
live positions sit in the same range.

That does not fit a 39-wide grid directly, so there is an **offset and probably
also a scale**. One suggestive arithmetic: 13 neighbourhoods x 3 areas = 39 cells
over what looks like a ~78-unit span would be 2 position units per cell. That is
numerology until it is measured, so nothing in df-hud relies on it.

Why there is no shortcut: **the saved web client never references `zones_` or
`areas_` at all.** Only the standalone client uses them, so there is no
JavaScript to port. `grep -rn "zones_\|areas_" dfsource/*.js` returns nothing.

Therefore:

- `catalog.go` exposes lookups by **grid index only**, never by position.
- Block Info v1 works from `df_positionx/y`, `df_tradezone` and
  `df_dangerlevel`, which need no transform at all.
- The neighbourhood name and building are a bonus that unlocks once the
  transform is known.

**Best anchor for solving it:** `areas_25_26_helicopter`. A single uniquely
identifiable landmark on both sides of the mapping is worth more than any amount
of street-hit-rate fitting — stand on the crash site, read `df_positionx/y`, and
the offset falls out. Two such points would also give the scale.

## Tradezones are a different, coarser thing

`df_tradezone` is 1..9 for a 3x3 division of the city, plus 10 = Wastelands,
21 = Outpost, 22 = Valcrest (`base.js:2017+`, `tradezoneNamer`). Names: North
Western, Northern, North Eastern, Western, Central, Eastern, South Western,
Southern, South Eastern. Note 39/3 = 13, so each tradezone covers 13x13 areas —
consistent with the grids above, and usable for Block Info with no calibration.
