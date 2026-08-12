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
