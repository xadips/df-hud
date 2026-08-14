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
routinely still standing there - so the previous cycle is shown for that block
alone, marked `last:`. City cycles are hourly, where the previous boss has gone.


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

The schedule that falls out of this: fetch shortly after the next boundary in the
data, on arriving at a new block (which is the only question the feed answers),
and otherwise on a heartbeat for the random spawns. The minimum gap is a floor
none of those can breach, and jitter is applied before the floor rather than
after, so it can never reduce it.
