# df-hud

A Wayland-native heads-up display for Dead Frontier, for Hyprland.

It draws over the game — including fullscreen — using `zwlr_layer_shell_v1`, and
passes every pointer event through to the game underneath. Data comes from the
game's own API, so there is no screen scraping and no OCR anywhere.

Replaces SilverOverlays, a Windows PyQt app that under Proton gives no reliable
always-on-top over a fullscreen game, no global hotkeys on Wayland, and no tray.

## Status

Working today:

- **Block Info** — where you are, which outpost, which region, block-support
  countdown, and **what is standing on your block**: bosses, bandit packs,
  missions, QRF events and outpost attacks
- **Run clock** — time since you started playing, which is not the same as the
  client's uptime (see below)
- **XP/hr** — averaged over five minutes, with the colour carrying whether recent
  polls actually landed, and a tray menu item to start the average again after a
  challenge reward lands a lump of XP in it
- **Challenge tracker** — the whole board, with progress, completion and
  deadlines, filtered by category: event challenges, clan challenges, ordinary
  ones and completed ones are each their own switch
- **Every group placed where you want it.** The clock, the rate, the board and
  block info each carry their own screen coordinates and their own font
- **It gets out of the way.** The overlay is hidden while the game is not
  running, and shown only on the workspace the game is actually on
- **A tray icon** with hide, reset and quit, so a background process with no
  window is still something you can see and stop
- A headless core with `-once`, `-print-view`, `-print-hud`, `-dump-fields`,
  `-dump-challenges`, `-check-config` and `-check-game` for looking at real data
  with no GUI

Planned: a console window for the full board with per-objective detail.

## Keybinds

A Wayland client cannot grab a global hotkey - the compositor owns them, by
design - so df-hud publishes each action on its loopback listener and the binding
is yours:

| | |
|---|---|
| `POST /api/run/start` | start the run clock from now |
| `POST /api/xp/reset` | start the xp/hr average again from now |
| `POST /api/overlay/toggle` | show or hide the overlay by hand |
| `POST /api/console/toggle` | the console window |
| `POST /api/run/click` | a click that *might* be the game's Start button |

Ready-made, with the layer rules: [contrib/df-hud.lua](contrib/df-hud.lua) for
Hyprland's Lua configuration, [contrib/df-hud.hypr.conf](contrib/df-hud.hypr.conf)
for `hyprland.conf`. The defaults are on `SUPER+ALT` rather than the function keys,
which Dead Frontier uses itself.

The Lua one is a module, so it needs a `require` as well as being on
`package.path`, and nothing happens if you only do one of the two:

```sh
ln -s ~/Programming/df-hud/contrib/df-hud.lua ~/.config/hypr/conf.d/df-hud.lua
```

```lua
require("df-hud")     -- in hyprland.lua, next to the other require lines
```

Then `hyprctl reload`, and `hyprctl binds -j | grep df-hud` to see the five binds.
There is a stubbed-`hl` check for that file in
[contrib/df-hud_spec.lua](contrib/df-hud_spec.lua), run by `go test`, because a
mistake in it is otherwise only visible in the compositor's log.

The tray menu offers the same three corrections, and the two stay in step: toggle
the overlay from a key and the tray's tick follows.

### Starting the clock from the game's own Start button

The run clock cannot be inferred reliably, and it is worth being precise about
why. Nothing in the player record marks the client taking control: position,
trade zone and `df_inoutpost` all survive a client exit and relaunch unchanged,
and pressing Start does not even move your character - it lets you move and drops
your AFK invincibility. Meanwhile a whole loot run can happen inside one block, so
"the position changed" can arrive a long way into a run, and killing is not what
you do first.

So `run_start` lets the button itself say so. **df-hud does not watch your input.**
Hyprland passes the click through with a non-consuming bind and calls
`/api/run/click`; df-hud then checks, for itself, whether the cursor was on the
button. Global input monitoring is a keylogger-shaped capability and this program
does not want it.

It has to be the compositor and not an invisible surface of our own, which is the
obvious alternative: a Wayland surface that receives a pointer event **consumes**
it, and the protocol has no forwarding. An invisible box over the Start button
would eat the click, so the button would never be pressed. Only the compositor can
both act on a click and still deliver it, because it is the thing doing the
delivering.

Two things keep a pixel rectangle from being as fragile as it sounds:

- the click must be inside the **game's focused window**, and the rectangle is
  measured from that window's corner rather than the screen's, so it survives the
  game moving to another monitor
- it is **ignored outright once a run is in progress**, which is how the game
  works anyway - you press Start once, then again only after extracting or dying.
  Every click you fire during play is therefore inert, and answered without
  asking the compositor anything.

Two things then keep the bind from being a nuisance, because clicking is also how
you shoot:

- it is **armed only while the game is focused**. A plain global bind on the left
  mouse button spawns a process on every click you make all day, in every
  application. Hyprland has no per-window bind filter, so
  [contrib/df-hud.lua](contrib/df-hud.lua) subscribes to `window.active` and calls
  `set_enabled` on the keybind - it does not exist while you are anywhere else.
- the dispatcher is a **Lua function rather than `exec`**, so it runs inside the
  compositor and decides whether to spawn anything, and at most one report per
  second leaves it. That limit loses nothing: the Start press is a single click,
  and anything it drops was under a second behind a click df-hud has already
  judged.

The `hyprland.conf` dialect can express neither, which is worth knowing before
copying the bind out of the other file.

Rejected alternatives, both measured rather than assumed: the game's memory
(`ptrace_scope` is 1, so it would need root or a machine-wide security change,
and Mono pointer chains break on every patch - on an account that has already
been temp-banned once, for polling), and the process's memory or CPU footprint
(measured live: **Start changes neither**, because the world is fully loaded
before you press it).

## Where each group sits

Four groups, four positions. The clock, the XP rate, the challenge board and block
info each carry their own `x`/`y` under `[widget.*]`, measured in pixels from the
top-left of the screen - the same numbers you would read off a screenshot:

```toml
[widget.session]
x = 350
y = 60
prefix = "IC Time: "

[widget.block]
x = 2340
y = 300
font_size = 14
```

This replaced one stack of rows in a corner with an `order` key on each. The stack
was the wrong shape for an overlay on a game that has its own interface to fit
around: the clock wants to be near the game's clock, block info wants the side you
already glance at, and stacking them means at most one of them is where you would
look. `order` and `hud.anchors` both went with it.

The surface is anchored to all four edges of the monitor so that a coordinate means
the same thing as it does in a screenshot, rather than depending on how wide the
widest row happens to be. `hud.margin_*` insets it, which moves the origin every
group is measured from - useful for pushing everything below a bar, and zero by
default.

`font_family` and `font_size` are per group and optional; absent means "use the
`[hud]` values", so a group only carries the keys it differs on. The defaults were
measured at 2560x1440, so another resolution wants its own numbers - and on a
scaled monitor these are logical pixels, not device ones.

Two things to know. A group placed near the right edge is **clipped, not wrapped**,
because a HUD line that reflows makes everything below it jump as values change
width - so leave room for the longest string a group can produce. And
`click_through = false`, which exists only for debugging, now makes a full-screen
surface swallow every click; df-hud says so at startup rather than leaving you
wondering why the game stopped responding.

## The challenge board, by category

The board is shown in full, in the game's own order, and each category is a switch
rather than a cap on rows:

| key | what it covers |
|---|---|
| `show_repeatable` | limited-time event challenges - currently the Summer ones |
| `show_clan` | your clan's challenges, which are the clan's progress and not yours |
| `show_personal` | the ordinary dailies and weeklies |
| `show_completed` | cuts across all three: rows that will not change again this cycle |

`show_repeatable` is named after the wire, not the event. The board marks these
`repeatable`, verified against a live fetch: `1` on exactly the three Summer
challenges and `0` on every daily, weekly and clan entry. Matching on the name
would break the moment the event changes; matching on the flag will not.

There used to be pinning here, showing two or three challenges chosen by name. It
existed because a dozen rows in a shared corner would bury everything else. With
the board in its own place on screen there is nothing left to bury.

## When the overlay is on screen

A layer surface belongs to a *monitor*, and the protocol has no concept of a
workspace, so a naive overlay is drawn over every workspace of that monitor
forever. Two rules fix that, and both **fail open** — if the compositor cannot be
asked, or the game's window cannot be identified, the HUD is shown. A HUD that is
wrongly visible is an annoyance you can see; one that is wrongly invisible is
indistinguishable from df-hud being broken.

- `hud.only_when_game_running` — no game, no overlay.
- `hud.follow_game_workspace` — df-hud asks Hyprland which workspace the game's
  window is on and hides the surface while you are looking at another one.
  `monitor = "auto"` follows the game's monitor the same way.

`-check-game` reports both halves: the process, and whether the compositor's view
of its window could be matched. The second one has its own silent failure, since
an unmatched window means the workspace rule quietly does nothing.

## The run clock is not the client's uptime

Launching Dead Frontier means a launcher, then a Launch button, then a loading
screen, then a Start button. Process uptime can therefore be minutes ahead of any
playing, and a clock counting that is timing a loading screen, which is exactly
what the first version did and what it was reported for.

No single signal is reliable, so the clock starts on the first of several and each
one is chosen so that being wrong makes it start *late* rather than early:

- the click on Start, described above, which is the only thing that knows for
  certain
- **leaving an outpost**: not the value of `df_inoutpost` but the *edge*, the poll
  where it went from set to clear. The value was already `0` at the launcher,
  before Start was pressed, so it does not mean "your character is docked at an
  outpost" the way the name suggests; the transition still does.
- **movement or XP**, as backstops. Both can arrive a long way into a run, since a
  whole loot run can happen inside one block and killing is not what you do first,
  which is why neither is the primary signal.

The clock ends on entering an outpost or dying, and is discarded when the game
closes or relaunches. `SUPER + ALT + T` and the tray both restart it from now, for
when it starts before you do.

It is persisted with the game's process identity, so restarting df-hud mid-run
resumes it instead of showing zero for a run that is an hour old.

## How it gets your session

The game's API authenticates with `userID` + a per-session password hash + `sc`,
and those exist only inside a logged-in page's JavaScript. A userscript posts
them to a **loopback-only** listener on `127.0.0.1:9275`; df-hud makes every game
request itself.

Endpoints under `hotrods/` — the challenge board among them — additionally need
the **session cookie**, and hashed ones need the request **signing salt**. So the
payload carries all three.

Either script works:

- **the bridge userscript+** (or the bridge userscript) — already posts to that exact
  endpoint. Load the Outpost home page and you are done. Versions before ***
  do not send the signing salt, so the challenge board will not work with them.
- **the bridge userscript** (`the bridge userscript`) — reports the salt too, and
  re-posts every five minutes so a rotated `sc` recovers by itself.

### Credential handling

The payload is account-equivalent, so:

- the listener binds loopback only, enforced in config validation *and* again in
  the server
- **request bodies are never logged, at any level**
- `credentials.json` is written atomically at mode 0600, and the mode is
  re-verified after writing
- `String`, `GoString` and `MarshalJSON` all redact, so a stray `%v` cannot leak
  them; a test asserts no secret ever reaches log output

The session cookie is stored, which reverses an earlier decision here. It was
discarded at first on the reasoning that `userID`+`password`+`sc` authenticates
everything, so a cookie would add a secret without adding capability. That is true
of `get_values` and wrong of endpoints under `hotrods/`: the challenge board
redirects to the site's front page without one. It gets exactly the same treatment
as the rest — 0600, redacted, never logged.

## Being a good citizen

The game server is not ours, and bursty request patterns get accounts temp-banned.

- **Three schedulers, one budget.** Nothing outside them makes a request, so the
  traffic budget is a single number rather than an emergent property.
  `-check-config` prints it: about **540 requests/hour while playing, 60 idle**
  at the defaults (the player record every 10s, the challenge board every 30s,
  the event map every 60s).
- **Two requests are never sent less than 5s apart**, whatever wakes them.
  Credentials arriving, the game launching and compositor events can all fire at
  once; without that floor an event storm becomes a request burst. The floor is
  a single shared gate rather than one per scheduler — otherwise adding the
  second poller would quietly have turned the guarantee into "per endpoint"
  while the documented number stayed the same.
- **The board slows down when you stop playing.** `challenge_interval` is the
  cadence while the game runs; with it closed the board stretches to
  `idle_interval`, so a short value chosen for play cannot become an all-night
  poll.
- **Rejected credentials stop polling outright.** They are not retried — retrying
  a rejected login is exactly the pattern that earns a ban. A fresh bridge
  payload resumes it with no restart.
- **Intervals below their floor are startup errors, not silent clamps.** Quietly
  raising a number teaches you that the value you wrote is the value in use.
- Jitter on every interval; exponential backoff on failure; zero traffic at all
  when the game is not running (`poll.only_when_game_running`, on by default).
- **Write endpoints are unreachable.** `hunger`, `itemspawn` and `modify_values`
  look like reads but mutate the account. There is a compile-time allowlist, plus
  a test that walks the package AST and fails if any of those names appears in a
  string literal anywhere.

## The event map, and somebody else's server

"What is on my block" is the one thing the game's own API will not tell you. The
player record knows where *you* are and the public stats feed knows the map's
geometry, but neither knows what has spawned on it. [DFProfiler](https://www.dfprofiler.com)
publishes that, so `[bossmap]` reads it from there — and since it is not our
server, the budget is set by what their own page already costs them:

- their boss map page re-fetches the same endpoint **every 30 seconds per open
  tab**. df-hud defaults to 60s and **refuses to be configured below 30s**, so it
  can never cost the operator more than one of their own tabs.
- nothing at all while the game is closed, same rule as the game server.
- the User-Agent is df-hud's own, naming the tool, so it can be identified and
  complained about.
- the endpoint 404s without `X-Requested-With: XMLHttpRequest`, so that is sent.
  That is the convention their API is written against, not a claim to be a
  browser. A forged `Referer` *would* be such a claim, so none is sent — and it
  turns out not to be needed.

Turning `bossmap.enabled` off costs that one line of the HUD and nothing else.

Two details worth knowing, both taken from their own `bossmap.js` rather than
guessed: a mission also carries a `special_enemy_type`, so classifying on that
first would file every mission as a plain boss spawn; and **Onslaught** events sit
on block `3000,3000`, which is exactly where the player record puts you while you
are in Onslaught. So they are indexed like any other block and surface only there.
Onslaught cycles shift every five minutes and overlap in practice, so last
cycle's boss is shown too, marked `last:` — out in the city, where cycles are
hourly, it would send you somewhere for nothing.

## Requirements

- Hyprland, or another compositor implementing `zwlr_layer_shell_v1`
- `gtk4-layer-shell` (Arch: `pacman -S gtk4-layer-shell`), GTK 4, cgo
- Go 1.26+

The first build compiles gotk4, which is large and single-package, so expect
several minutes; rebuilds are a few seconds.

```sh
go build -o df-hud .
./df-hud -check-config
./df-hud
```

`go build -tags nolayershell` builds the headless core with no GTK at all, for
CI or a machine with no Wayland.

## Configuration

`~/.config/df-hud/config.toml`, or `-config`. Every value defaults to something
usable, so no file is needed to start. See
[df-hud.example.toml](df-hud.example.toml), which documents every key and is
checked against the built-in defaults by a test so it cannot drift.

Unknown keys are a **startup error** — a typo would otherwise look like a bug in
df-hud rather than a bug in your config.

**Editing the file while running reloads it, including the HUD's own appearance:**
font, colour, size, margins, which groups exist and where each one sits all change
without a restart, as do every interval and both visibility rules. Only
three keys need a restart (`bridge.listen`, `bridge.enabled`, `paths.data_dir`),
and they say so. An edit that fails validation keeps the running config rather
than taking the HUD down mid-game.

`hud.layer` must stay `"overlay"`. The `top` layer sits *below* fullscreen
windows, so a `top` HUD vanishes exactly when you play.

## Notes for the curious

`knowledge/` records what was measured rather than assumed, with the evidence:

- [allstats-map-and-xp.md](knowledge/allstats-map-and-xp.md) — the public feed's
  XP table and city grids, why cumulative XP needs care, and the
  position-to-grid transform that is still unsolved
- [player-record-and-signing.md](knowledge/player-record-and-signing.md) — the
  342-field player record, the two different time encodings (mixing them up is
  worth 38 years), and how requests are signed

GPL-3.0.
