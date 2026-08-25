# The player record, and request signing

Everything here was read off the live server on 2026-08-11 with
`df-hud -once -dump-fields`, or out of the game's own JavaScript. Field names are
game schema; values below are either public (the signing salt, served in a public
JS file) or harmless (a level number).

## `get_values` needs no credentials

Measured 2026-08-17:

```
GET https://fairview.deadfrontier.com/onlinezombiemmo/get_values.php?userID=<id>
-> 342 fields, the same count the credential-carrying POST returns
```

No password, no `sc`, no cookie. Every field df-hud reads is present, and nothing
credential-shaped comes back. SilverOverlays has used this form all along, at a
31s cadence with User-Agent `Silver`; finding it there is what prompted checking.

df-hud now polls this way. It matters because the record is read every 10s while
you play: the old path put an account-equivalent triple in roughly 360 request
bodies an hour to read data the server hands to anyone who asks, and it coupled
the HUD's main loop to a browser session that a re-login elsewhere can rotate.

The credentials are still needed - the user id comes from them, and
`hotrods/load_challenge` genuinely requires the triple plus a cookie. This
narrows what travels; it does not remove the dependency.

The form is undocumented, so the authenticated POST stays as a fallback. It is
probed **once per process**: a server that stopped answering the GET would
otherwise mean two requests per poll forever, which is the opposite of polite.

There is deliberately no setting for this. The fallback is automatic, so a switch
would only ever save that one probe - and the failure it would otherwise guard
against, the public form returning plausible-but-wrong data, is ruled out by two
consecutive GETs returning different XP values, so nothing is caching it.

It also confirms the record is readable by anyone who knows a member id. That was
already true via DFProfiler's public feed; it is worth stating rather than
implying.

## `get_values` returns 342 fields

Far more than df-hud uses. The ones that matter, with their verified meanings:

| field | meaning |
| --- | --- |
| `df_level`, `df_exp` | level, and XP **within** that level |
| `df_exptotal` | cumulative XP since level 1 — see [allstats-map-and-xp.md](allstats-map-and-xp.md) |
| `df_expstart` | cumulative XP at the start of something; see below |
| `df_expdeath` | XP since the last death (matches DFProfiler's `exp_since_death`) |
| `df_expdeathrecord`, `*_daily`, `*_weekly` | best-ever and periodic variants |
| `df_positionx/y/z` | block coordinates; z is a floor index, 0 is the street |
| `df_tradezone` | 1..9 region, 10 wastelands, 21 outpost, 22 Valcrest |
| `df_inoutpost` | "1" inside an outpost |
| `df_dangerlevel` | present, but the web client never renders it, so its scale is unknown |
| `df_block_support_until` | block support expiry; **always 0 in every observation so far** |
| `df_hpcurrent`, `df_hpmax` | health |
| `df_cash`, `df_bankcash` | cash on hand, cash in the bank |
| `df_hungerhp`, `df_hungerhpmax`, `df_hungertime` | nourishment |
| `df_boostexpuntil`, `df_boostdamageuntil`, `df_boostspeeduntil` | boost expiry, plus `_ex` variants |
| `df_servertime` | server clock, compact epoch |
| `df_dead`, `df_freepoints` | death flag, unspent stat points |
| `account_name`, `id_member`, `id_character`, `newpms` | account-level, not `df_`-prefixed |

Nothing credential-shaped comes back: no password, no `sc`. The only ambiguous
name is `df_session3d`, which `-dump-fields` withholds on the grounds that a
field with "session" in the name gets the benefit of the doubt.

**`df_cash = 0` is not a parse failure.** Observed live alongside a nine-figure
`df_bankcash`: cash on hand really can be zero while the bank holds everything.
Worth remembering before hunting for a bug in the parser.

## Two time encodings, and a 38-year trap

The record uses **two different epochs**, and applying the wrong one is worth
almost four decades of error. This was caught by live data: the HUD reported an
XP boost expiring in 49 years.

1. **Compact epoch — unix minus `TIME_OFFSET` (`1_200_000_000` in
   `src/data/mod.rs`).** Used by `df_servertime` and `df_hungertime`. Live:
   `df_servertime = 586484051` at a unix time of `1786484051`.
2. **Plain unix seconds.** Used by the `*until` expiry fields. Settled by the
   game's own arithmetic:

   ```js
   durationLeft = df_boostexpuntil - (df_servertime + 1200000000)
   ```

   The right-hand side is unix, so the left-hand side is too.

**`2147483647` means "never expires".** That is int32 max — 2038-01-19, the
classic end of 32-bit time — and it is what the live server returns for a
permanent boost. Treating anything more than 600,000 seconds out as infinite
arrives at the same place from the other direction.

df-hud models this as a state (`dfDeadline.Forever`) rather than a very large
number, and additionally refuses any deadline more than a year away. That guard
exists specifically for `df_block_support_until`, whose encoding is **unverified**
because it has been 0 in every observation: if it turns out to use the compact
epoch, the HUD omits the line instead of confidently showing a decades-wrong
countdown.

## `df_inoutpost` does not mean what it looks like

It was the obvious signal for "am I playing", and it is wrong for that. Observed
live on 2026-08-12: with the launcher on screen and **Start not yet pressed**, the
record already read `df_inoutpost = 0`. A run clock built on it therefore started
counting the loading screen, which is what it was reported for.

What it does mean is unresolved. Two candidates fit the observation - "the browser
is on an outpost page" and "the character is not docked, including after exiting
the client from the city" - and nothing so far distinguishes them.

df-hud does not use `df_inoutpost` to start the clock. The client's own
position (Discord rich presence: a city block or an outpost) starts it when
that pipe is fresh. If presence is quiet, the first poll that sees
`df_positionx/y/z` change or a leave-outpost transition starts it instead.
Nothing on the server moves your character while you are looking at a
launcher. `df_inoutpost` is kept only as an end-of-run condition, where being
wrong stops a clock early rather than inventing playing time.

## There is no "session started" timestamp

Checked exhaustively rather than assumed: of 342 fields, exactly three hold a
value anywhere near the current time, and none of them is a session marker.

| field | value at capture | meaning |
| --- | --- | --- |
| `df_servertime` | now | the server clock |
| `df_hungertime` | now, to the second | the nourishment tick, which is continuous |
| `df_looteditem_time` | 79 minutes earlier | the last item looted |

No field advanced when Start was pressed either: `df_expstart` did **not** change
across a launcher-to-playing transition that df-hud was watching. So the moment
you enter the city is not recoverable from the record after the fact; it can only
be observed as it happens.

## `df_expstart` — probably XP at the start of the run

Observed mid-run, out in the city:

```
df_exptotal = 10,000,000
df_expstart = 9,000,000
difference  =      1,000,000
```

A million XP is a plausible amount for one trip into the city at level 415, so
this looks like the cumulative total at the moment the run began — which would
make "XP this run" free, with no averaging window at all.

**Not yet confirmed, and now less likely.** Live on 2026-08-12 it was observed
*equal* to `df_exptotal` while playing, having not moved at any point between the
launcher appearing and Start being pressed. That rules out "set when the run
begins" as df-hud would need it, and points instead at "set when the last run
ended", which is the same value from the outside but useless as a run marker.

df-hud parses it and no widget renders it. A change in it is logged when it
happens, so the next transition adds evidence rather than requiring the question
to be asked again from scratch.

## Request signing

`hash` and the salt live in a **public** file, `md5.js`, loaded by the game at
`hotrods/hotrods_v<version>/HTML5/js/md5.js`:

```js
var SKeyGen = "y27bigaOAA1";          // md5.js:3

function hash(params) {                // md5.js:162
    var a = params.split("&");
    var b = [];
    for (var i = 0; i < a.length; i++) b.push(a[i].split("="));
    var c = SKeyGen;
    for (var i = 0; i < b.length; i++) c += b[i][1];
    return MD5(c);
}
```

Notes that matter:

- **The salt is not a secret.** It is a global game constant served in a public
  JS file, identical for every player. It is safe in a config file and safe in a
  log. What makes it awkward is only that it changes when the game updates.
- **`split("=")` then `[1]` takes only the text between the first and second
  `=`.** A value containing `=` therefore contributes just its leading fragment
  to the digest, while the full value is still sent in the body. Getting this
  wrong makes every hashed request fail.
- The URL is **version-stamped** (`hotrods_v9_1_3` at time of writing), and the
  version only appears in page context, so df-hud cannot fetch the salt without a
  session. Hence the current design: the bridge reports it from page context and
  a reported value wins, with `df.skeygen` as a manual fallback.

### Digests verified against the real algorithm

`src/net/dfclient.rs` pins hardcoded digests. They were re-derived from the game's
actual `md5.js` by an independent implementation, and all three agree:

| params | digest |
| --- | --- |
| `userID=999&password=hashhash&sc=sc123&action=get` | `7fe50eb1872ba13897d0b7cc8b83e5e4` |
| `k1=a&k2=b=c` | `adedaf7c7c63f8c975c6e129793aa786` |
| `k1=a&k2=b=c` taking whole values (**wrong**) | `2873e7b6d53e25072ecd596888c6e4ce` |

The third row is the bug the second row guards against, kept so the difference
stays visible.
