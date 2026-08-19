# Position from the game client

The player record is not the best source for where you are. The game CLIENT
publishes its own position as Discord rich presence, and that is both fresher
and more correct than `df_positionx/y`.

## The measurement that decided it

Both running at once, 2026-08-16, walking:

```
17:27:45  presence  1054,986
17:27:50  poll      1054,987   <- 5s later, server still on the old block
17:27:59  poll      1054,987   <- 14s later, still old
17:28:01  presence  1054,985
17:28:06  presence  1055,985
17:28:10  poll      1054,985   <- server moves to a block already left
17:28:12  presence  1055,986
```

The poll cadence was correct throughout - 9-11s, as configured. **The server's
own record is what lags**, by 15-25s, and it SKIPS blocks: `1054,986` never
appeared in any poll at all.

Two earlier comparisons showed the two agreeing exactly, which nearly killed the
idea. Both were taken while standing still or zoning - precisely when the server
catches up. Only movement exposes it, so measure while walking or not at all.

The client is rate limited to roughly one update per 5s by Discord's SDK (the
documented limit is 5 per 20s), and only publishes on a change. So standing on a
block is silence about a position that is still true, which is why
`presenceMaxAge` is generous rather than tight.

## What it publishes

`details` carries four forms, all observed live:

| details | meaning |
| --- | --- |
| `Inner City 1054 x 986` | out on the map, exact block, same space as `df_positionx/y` |
| `Hospital 1058 x 1016` | inside a building, ON that block |
| `Secronom Bunker` | standing in an outpost, by NAME - no coordinates |
| `Loading...` | zoning |

**The building form was missed at first, and it cost the feature most of its
value.** The original parser required the literal prefix `Inner City`, because
every measurement was taken walking the streets, where the label never varies.
Buildings label the coordinates with the building's name instead - `Hospital`,
`Supermarket` - so all the time spent looting fell back to the poll, with the
block sitting right there in the string.

The parser now reads `<label> <x> x <y>` from the END and treats the label as
information rather than as a gate. A building is on the block it stands on, which
is the block whose events matter when you step back out, so indoors is a position
like any other. A bare `1058 x 1016` with no label has never been seen and is
still reported as unparsed rather than quietly accepted.

`state` is `Multiplayer`, and `party.size` looks like players-on-block out of 15
(unverified, unused). The app id is `672536224820625429`.

An outpost is matched against the known names rather than assumed from "not
Inner City": a form nobody has seen yet is reported as unparsed and logged once,
so a format change surfaces instead of inventing an outpost.

## Getting a Proton game's presence onto a Unix socket

The game loads `discord_game_sdk.dll` and talks to the Windows named pipe
`\\.\pipe\discord-ipc-0`. Wine named pipes are per-wineserver and are not
filesystem objects, so something has to bridge them to a Unix socket.

Proton-GE ships `discord-rpc-bridge/bridge.exe` and **only copies it into the
prefix** (`proton:1462`) - it never launches it. Nothing runs it for you, which
is why `output_log.txt` reads `Discord encountered an error.` on a stock setup.

It has to run **inside the game's own wineserver**. Running it with
`protontricks-launch` does not work: that starts its own pressure-vessel
container with its own wineserver, so the pipe it creates is invisible to the
game (and leaves two wineservers on one prefix). Install it as a service
instead - the bridge's own dialog does this - which registers:

```
[System\CurrentControlSet\Services\discord-bridge]
"ImagePath"="c:\\windows\\system32\\discord\\bridge.exe --service"
```

The installer registers it `Start=3` (demand-start), which never fires on its
own. `Start=2` (auto) is what makes `services.exe` bring it up with the prefix.
Install with the game CLOSED: wine caches the registry in memory and flushes it
on wineserver shutdown, so a write made while the game runs is discarded on exit.

## What it is used for

**Position**, preferred over the poll for the reasons measured above.

**The run clock's two edges.** The polled version has to INFER a run from three
weak signals - the outpost flag changing, the position changing, or XP going up -
and in practice it often never fired. It needs two consecutive polls ten seconds
apart, and its position is the server's record, which lags and skips blocks, so
walking in and standing still produced no movement it could see and no clock
started until something was killed.

The client states both edges instead: a position means you are in the city, an
outpost name means you are not. `Loading...` is ignored rather than treated as an
end, because zoning happens mid-run - through a door, into a building - and
ending there would restart the clock at every doorway.

A run restored from a previous df-hud process (`runSeed`) wins over a fresh
presence start, or restarting df-hud mid-run would throw away a clock that is
already an hour old.

**Whether a synthesised key can land.** `Loading...` means the client is on a
loading screen and would discard the keypress, so `Store.ClientInWorld` holds the
FPS key back until it is past. It defaults to TRUE wherever the client has not
said otherwise, so a machine with no bridge is not left unable to send anything.

## df-hud's side

df-hud IS the Discord endpoint - it binds `discord-ipc-0` and answers the
handshake. Discord need not be running, and a real Discord or Vesktop already
holding the socket means df-hud stands down and says so once.

The SDK will not publish until it gets a `READY` dispatch, and treats an
unanswered nonce as an error, so every command is acknowledged even where the
content is ignored. The one that bites: `SUBSCRIBE` carries a literal `null` for
`args`, so decoding it into a map is a nil dereference on the happy path.

**A dropped socket is not recovered.** The game's SDK keeps a dead handle and
never reconnects, so restarting df-hud means restarting the game to get the feed
back. The poll takes over in the meantime, which is what makes that survivable
rather than a failure.
