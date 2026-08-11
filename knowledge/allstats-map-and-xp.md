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

Level 415 is the cap, hardcoded in `checkIfLevelUp` (`curLevel < 415`).

### `df_exptotal` exists, and it is the better source

Corrected finding. The saved web client's *code* never references `df_exptotal`,
but the captured page `userVars` all carry it, with real values. So the player
record already contains a server-side cumulative XP counter, and **XP/hr does not
need the table at all** — the table is the fallback for a record that lacks the
field, plus the source of "XP to next level".

Its relationship to the table is a **fixed offset**, not an exact match. From
five independent captures:

```
df_exptotal - df_exp = 16,576,044,375 + 393,591   (identical in all five)
sum(exp_lvl 2..415)  = 16,576,044,375
offset               =        393,591
```

That the difference is *constant across captures* is the load-bearing part, and
it is a real result rather than a coincidence of sampling: the captures were
taken while playing, so `df_exp` climbs between them by 19M, 0.7M, 8M and 346M
respectively — yet the difference does not move by a single point. Both counters
advance by exactly the same amount, so either one gives an identical rate. Only
the absolute totals differ. The likely cause is historical — DF has
rebalanced the XP curve over the years, so an account's accumulated total does
not have to match today's thresholds — but it is unverified and nothing depends
on it.

(An earlier version of this note attributed the same 393,591 gap seen in
DFProfiler's `total_exp` to scrape timing between two of their fields. That was
wrong: DFProfiler simply reports `df_exptotal`, and the gap is this systematic
offset.)

So the two tiers are:

1. `df_exptotal` when present — authoritative, needs no catalog.
2. `sum(exp_lvl 2..level) + df_exp` otherwise — provably continuous, see below.

The active tier is logged on the first poll, so which one is in use is never a
guess.

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

### At the cap, `df_exp` is an unbounded accumulator

You keep earning XP after level 415, and since `checkIfLevelUp` refuses to run at
the cap (`curLevel < 415`), it all piles into `df_exp` permanently. The captured
account reads `df_exp` in the **billions** against an `exp_lvl415` of 176M, which
is 40x the largest threshold in the table.

Consequences, all handled in `parseSnapshot`:

- There is no `exp_lvl416`, so `ExpNeeded` reports nothing at the cap and no
  widget renders progress toward a level that cannot exist. (The website's own
  sidebar prints the threshold as "I have no life" there.)
- `PendingLevels` is 0 at the cap by the same route.
- A raw `df_exp` delta is a perfectly correct rate at the cap, since nothing
  ever resets it.
- Any code that assumes `df_exp < exp_lvl[level+1]` is wrong twice over: once
  because of banked overshoot mid-run, and once because of this.

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
