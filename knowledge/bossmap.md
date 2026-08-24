# The city event map

What has spawned on the map: bosses, bandit packs, missions, QRF events, outpost
attacks. **Not available from the game's own API** - the player record knows where
you are, and the public stats feed knows the map's geometry (buildings, streets),
but neither knows what is standing on it. DFProfiler publishes it.

```
GET https://www.dfprofiler.com/bossmap/json/
X-Requested-With: XMLHttpRequest      <- required; 404 without it
```

Verified 2026-08-12: no cookie, no Referer and no cache-buster are needed, and an
honest df-hud User-Agent is served normally. Their own page adds a `_=<millis>`
param and a Referer; neither changes the response.

## Shape

A JSON object whose keys are event indices as strings, with three scalars mixed in
at the same level: `bosshash` (a change marker), `servertime`, `version`. Every
value inside an event is a **string**, including numbers and booleans.

| field | meaning |
| --- | --- |
| `locations` | `[["1058","1016"], ...]` - the same coordinate space as `df_positionx/y`, so no transform is needed |
| `special_enemy_type` | `"6 x Bandits"`, or several joined by `<br />`; `"0"` means none |
| `need_briefing` | `"1"` marks a **mission** |
| `event_type` | `""`, `mission`, `qrf`, `qrfdr` |
| `dfp_objectives` | an object when present, an **empty array** when not - so it cannot be decoded straight into a map |
| `isoa` | `"1"` means every outpost is under attack; map-wide, not a block |
| `started` / `ended` | `ended = "1"` is last cycle's |
| `start_time` / `end_time` | plain unix seconds |
| `boss_num` | the game's own event slot, numbered **per event type** - see below |

## boss_num is the legend

`boss_num` is what makes an identifier on the map mean something to another player,
and it took a capture to see why. Within one `event_type` the slots **ascend with
difficulty**. From the 30-event capture in `testdata/bossmap.json`:

| slots | what sits there |
| --- | --- |
| 1, 2, 3, 14, 16 | bandit camps, carrying 1, 2, 2, 4 and 6 bandits in that order |
| 4 … 11 | nests, from two Flaming Zombies and a Riot Shield Guy up to a five-type bear pit |
| 17 … 27 | single-type spawns, Flaming Zombie through Irradiated Titan |

So ranking the *active* events of one category by slot gives `B1..B6` in the order the
city gets harder, which is what DFProfiler's map means by those numbers - it reads the
same feed. Onslaught has its own slot space (a nest at slot 1 alongside a bandit camp
at slot 1), which costs nothing because Onslaught events are filtered out unless you
are standing in it.

For a **mission** the slot IS the outpost, zero-based: 0 Nastya's, 1 Fort Pastor,
2 Dogg's, 3 Precinct 13, 4 Secronom. Valcrest and Ground Zero carried none in the
capture. Hence `M1..M5` with no ranking at all.

**QRFs all arrive on slot 0** - two at once in the capture, `qrf` and `qrfdr` - so they
cannot be numbered from it and are counted in feed order instead. Worth knowing before
trusting the slot as a general key: it is unique per type for spawns and missions, and
useless for QRFs.

## Three things taken from their own bossmap.js rather than guessed

1. **The classification order matters.** A mission also carries a
   `special_enemy_type`, so testing that field first files every mission as a
   plain boss spawn. Their order is: `isoa`, then `need_briefing`, then
   `special_enemy_type`, then `event_type`, then "Unknown GPS".
2. **`3000,3000` is Onslaught, not null.** Their page treats it as "off screen"
   because the city grid has no such cell, but the player record reports exactly
   `3000,3000` while you are in Onslaught - so it is a real coordinate in the same
   space, and indexing it like any other block makes those events surface exactly
   when you are there.
3. **Their page polls this every 30 seconds** (`setTimeout(I, 3e4)`). That sets
   df-hud's floor: 30s, defaulting to 60s, so one df-hud can never cost the
   operator more than one of their own open tabs.

Onslaught cycles are five minutes long and overlap - last cycle's boss is
routinely still standing there, and the next one is already in the feed before
it starts - so the previous and next cycles get their own panel
(`onslaughtPanel` in hudlines.go, `bossesWidget` in widget_bosses.go), coloured
to match the `onslaught_bosses` userscript this mirrors: grey for `prev`, red
for `now`, blue for `next`, a dimmer grey for an empty section
("cleared"/"nothing this cycle"/"not announced") or the "ended Nm ago" line.

The panel's outline went through both extremes before landing. The base
sheet's `0 0 4px` glow smears once a dozen close-set rows each halo onto their
neighbours; removing the shadow entirely then left the text blending into
whatever the game drew behind it. What works is neither: four 1px hard offsets,
no blur, which draws a black border around each glyph instead of a glow around
the block. Four corners rather than the map key's three - these rows are
thinner and a missing corner shows.
City cycles are hourly, where the previous boss has gone and the next one is
still just a countdown - so `threatLines` (the plain, uncoloured display used
everywhere else on the map) never shows Onslaught's prev/next fields at all,
only `onslaughtPanel` does.

**How long the thing on your block has left goes on its first row, once.** A
nest is one event carrying up to seven enemy types with a single `end_time`
between them, so a countdown per row would print the same number seven times
down a group that is already seven rows tall. The map's key had settled this
first: `mapRow.Timer` is set on an entry's first row and empty on its
continuations. Every one of the 30 events in the recorded capture carries an
`end_time` - 5, 60, 120 and 700 minute windows - but nothing in their format
promises it, so a missing or already-passed one shows no countdown rather than a
placeholder. `withTimeLeft` uses `e.End` and NOT `onslaughtCycleEnd`: that adds
a cycle per extra name, which is right for an Onslaught bundle of consecutive
cycles and an hour wrong for a city nest whose types are all standing there at
the same time.

**Each row is two GTK labels, not one with a mixed-colour markup span.** The
"prev"/"now"/"next" word stays one fixed grey regardless of section, while the
content beside it takes the section's own colour - and both want the panel's
own outline rather than the base glow, which GTK cannot apply to only part of
one label's text. So
`onslaughtRow` carries `Label`/`Content`/`ContentClass` separately, and
`onslaughtRowWidget` (widget_bosses.go) lays them out side by side, the label
always `onslaught-label`. A single label with an inline `foreground=` span was
tried first and looked right in isolation, but its shadow could not be split
from the content's - two widgets is the only way to make good on the "no
shadow, one grey label" requirement, not just the colour.

**A joined `special_enemy_type` on an Onslaught event is not a real nest.**
Confirmed live: `"3 x Irradiated Giant Spider<br />3 x Mega Giant Spider"`
looked like a two-type spawn but was the feed bundling two different cycles'
own single bosses into one entry, the one listed LAST being current.
`onslaughtEventRows` keeps only the last name for this reason - `eventRows`
itself is unchanged and still shows every type on a real city nest, where a
joined list genuinely means several enemies spawned together.

**A bundle's `start_time`/`end_time` describe only the FIRST cycle in it.**
Captured at 00:28:51: one entry read start 00:15:01, end 00:20:01 carrying two
names, while the 00:25 cycle was a separate entry - so the second name really
ran 00:20-00:25, and the entry's own end is that boss's START. Ageing the
displayed boss from `e.End` therefore said "ended 8m ago" about something three
minutes gone, one full cycle early per extra name. `onslaughtCycleEnd` shifts
it by `(len(Enemies)-1)` windows, taking the cycle length from the entry's own
window rather than hardcoding five minutes. This is also why
`bossMapPastWindow` has to be well over one cycle: a bundle's declared end is
already stale by the time it is the thing worth showing.

`AtEnded`/`AtUpcoming` narrow to whichever events share the single most recent
end time / soonest start time (`edgeGroup`, ported from the same idea in the
`onslaught_bosses` userscript). `bossMapPastWindow` is 12 minutes, not one
cycle: Onslaught skips slots often enough that the last SPAWN can be more than
five minutes back, and 12 is what the userscript settled on after 6 lost
`prev` too early in practice.

A countdown rides along (`BlockBoundary`, `View.OnslaughtCountdown`): the
current cycle's own end while something is up, the next one's start once it
isn't. No timer of df-hud's own drives "next" becoming "now" becoming
"prev" - it happens because `Derive` re-reads `ActiveAt`/`EndedRecentlyAt`/
`UpcomingAt` against the real clock on every one-second UI tick, the same as
everything else here. The countdown is only ever a display of how long until
that already-true reevaluation lands on the boundary, never what causes it to.

**`servertime` is not a clock. It is how stale the data is.** Measured
2026-08-17 against an NTP-synced local clock: 19s behind, and 53s behind on a
fetch 14 minutes later, so it drifts rather than sitting at an offset - it is
when their backend last synced with the game. `cf-cache-status` is `DYNAMIC`,
so nothing in between is caching it; the lag is theirs.

Everything therefore compares against the **local clock**, and there is no
skew adjustment anywhere in `bossmap.go`. Two things justify that. The feed's
timestamps are absolute unix seconds sitting on the game's own schedule -
every Onslaught boundary is `unix % 300 == 2`, e.g. `19:25:02 → 19:30:02 →
19:35:02` - so they are computed from the cycle, not observed on a wonky
clock. And the local clock is NTP-synced, which makes it the better reading of
absolute time of the two.

This was a real bug twice over. First, right after a restart the countdown
showed a full extra minute against a cycle genuinely ~15s from turning over,
because the freshest possible skew was the most misleading one. Then, once
`BlockBoundary` alone had been switched to the local clock, it disagreed with
the rows beside it: the countdown ticked out and restarted at 5:00 while the
panel kept last cycle's boss as "now" for however stale the feed was - 5
seconds when reported, up to a minute in principle. Adding skew delayed every
changeover by the data's age.

With one clock, the shift happens by construction and needs no state of its
own. At the boundary instant the ended cycle fails `ActiveAt` and becomes
`prev`, the cycle starting there becomes `now`, `next` empties until the feed
publishes the one after (~100s ahead), and `BlockBoundary` returns that
new cycle's end.

**Onslaught has its own poll period** (`bossmap.onslaught_interval`, 30s). The
city interval is sized against a 3600s cycle; against Onslaught's 300s one, a
five-minute heartbeat is a whole cycle wide and can miss a turnover outright.
These are the only settings where df-hud costs dfprofiler what one of their own
open tabs does rather than half of it - bounded, since they apply only while
standing on 3000,3000 with the game running. `RequestsPerHour` deliberately
does not fold that floor in: it reports what an hour of normal play costs, and
an hour entirely inside Onslaught is a different activity. `max_interval` and
`onslaught_max_interval` are unused config keys kept so existing files load.


## Cycles, from the player rather than from the data

Not everything in the feed follows the hourly cycle, which matters for how often
it is worth fetching:

| what | cycle |
| --- | --- |
| city bosses, bandit packs, QRF | on the hour, `XX:00` |
| Onslaught (block `3000,3000`) | every 5 minutes |
| Devil Hound, Behemoth, Volatile Leaper, 8x Bandits | **once a day at a random time**, lasting 3 hours for the zombies and 2 for the bandits |

The last row is why a heartbeat exists at all: nothing in the data predicts a
random daily spawn appearing, so no amount of boundary arithmetic will catch one.

## The feed carries the changeover before it happens

Observed live: around `:59` the next cycle is already in the response with
`started = "0"`, and for some minutes after `:00` the previous one is still there
with `ended = "1"`. Both carry their own `start_time` and `end_time`.

So a single fetch made at `:59` contains everything needed to be correct at
`:01`, and df-hud derives an event's state from those timestamps against the
clock rather than from the `started`/`ended` flags. The changeover then costs no
request at all and lands on the second rather than at the next poll.

The fetch loop is a heartbeat: `interval` in the city, `onslaught_interval` in
Onslaught. Random daily spawns are why it cannot be an event-boundary scheduler.
A shared 1s request gate still spaces this fetch from the player and challenge
pollers.
