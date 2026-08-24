# The city's shape

The inner city is not a rectangle. Its bounding box is 59 x 55 - x 1000..1058,
y 981..1035 - but only **1716** of those 3245 coordinates are blocks. The rest are
gaps: not places, and not somewhere you can cut through on the way to somewhere
else. Anything that measures distance by subtracting coordinates is therefore
wrong some of the time, and wrong in the worst way, because it points confidently
at a route that does not exist.

## Where it comes from

Nothing the game publishes says which coordinates are blocks:

| source | knows | does not know |
| --- | --- | --- |
| `get_values.php` | where you are, `df_tradezone`, `df_dangerlevel` | anything about anywhere else |
| `get_allstats.php` | a 39x39 `areas_` grid of streets and buildings, a 13x13 `zones_` grid of neighbourhood names | how either grid maps onto city coordinates - and neither can, see below |
| DFProfiler's bossmap JSON | what has spawned, and where | the map it is standing on |

**DFProfiler's stylesheet does**, because it draws the city: `bosstable.css` lists
every real block by coordinate and paints it. `.coord` defaults to `opacity:0`, and
a block is a coordinate the stylesheet gives a colour and `opacity:1;cursor:pointer`.

`assets/citymap.txt` is generated from that stylesheet, committed, and embedded.
It is rebuilt by hand, once - the city changes when the game changes, which is to say
almost never - and **df-hud never fetches the stylesheet at runtime**. Re-deriving
its own map from someone else's CSS on every start would be both fragile and rude.
The map's shape is the game's, but this encoding of it is DFProfiler's work, and
df-hud already depends on their bossmap feed; credit is in the README.

## What was checked before building on it

1. **All 201 event locations** in the recorded feed land on a block. Not one falls
   in a gap. If the gaps were an artefact of how their page is laid out rather than
   the city's real shape, the game would be spawning bosses in them.
2. **All 7 outposts** land on a block, at the coordinates `newoutpost.js` gives.
3. **The 1716 blocks are one connected component** under four-way movement, reached
   from Nastya's Holdout. A random subset of a rectangle is not connected; a city
   you can walk around is.
4. Two coordinates the old straight-line code used as test positions - 1058,1018
   and 1050,1010 - turn out to be **gaps**, so the walk-based tests had to move to
   real blocks. That is the failure mode in miniature: 1058,1018 is directly north
   of Ground Zero, and there is no such place. The only way out of that outpost is
   west.

Each of these is in `src/citymap.rs` tests, so a regenerated map that breaks one of them
fails the build rather than quietly changing where df-hud sends you.

## Orientation: `y` increases southwards

**Verified in the game 2026-08-13** by walking one block north and watching
`df_positiony` fall. Until then it was an inference from their map being an HTML
table whose rows are `y`, which put the smallest `y` at the top.

So the file is stored with `y` increasing downwards, the grid is drawn the same way,
and "up" in the HUD's `nearest 3 up 1 left` means north in the game. This was the
last unverified claim that could have sent someone the wrong way.

## What is in the file

- `origin` and `size`: the bounding box, so a coordinate is an index.
- `divider x|y`: the district boundaries, drawn on their map as white cell borders.
  They fall before x 1020 and 1041 and before y 994, 1007 and 1020 - three columns
  by three rows over the main map, plus the southern strip below y 1020. That is
  nine districts, which is what `df_tradezone` counts (`tradezoneNamer` names 1..9
  "North Western" through "South Eastern"), so the two should agree. **Not yet
  cross-checked against a live `df_tradezone`.**
- `shade`: the sixteen colours, dark to light. They are a difficulty gradient
  outward from Nastya's Holdout plus a handful of special areas, and reproducing
  them is what makes the console map recognisable. **Whether a shade equals a
  `df_dangerlevel` band is not known**, so nothing is derived from them - they are
  only drawn. The check, when it is worth doing, is to log `df_dangerlevel` against
  the shade of the block you are standing on over a few runs.
- the map: one character per coordinate, `.` for a gap.

## The one assumption left

Two adjacent blocks are taken to be connected. Nothing published states that, and
it is possible that somewhere in the city there is a wall between two real blocks.
The white borders in their CSS are not it - those run the full length of columns
and rows, so they are district boundaries, not barriers.

If a wall does exist, the cost is a block or two of estimate on one route. That is
a different order of error from the one this replaces, which was pointing straight
at a boss across a hole in the ground.
