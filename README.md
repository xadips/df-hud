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
| `POST /api/widget/<group>/toggle` | show or hide one group: `block`, `bosses`, `session`, `xp`, `challenges`, `map` |
| `POST /api/console/toggle` | the console window - **not built yet**, answers 503, and has no default bind |
| `POST /api/run/click` | a click that *might* be the game's Start button |

Ready-made, with the layer rules: [contrib/df-hud.lua](contrib/df-hud.lua) for
Hyprland's Lua configuration, [contrib/df-hud.hypr.conf](contrib/df-hud.hypr.conf)
for `hyprland.conf`. Keep off the function keys and the number row, which Dead
Frontier uses itself; the file itself is where the current combinations live, since
they are yours to change and any list here goes stale the first time you do.

The Lua one is a module, so it needs a `require` as well as being on
`package.path`, and nothing happens if you only do one of the two:

```sh
ln -s ~/Programming/df-hud/contrib/df-hud.lua ~/.config/hypr/conf.d/df-hud.lua
```

```lua
require("df-hud")     -- in hyprland.lua, next to the other require lines
```

**The keys exist only while Dead Frontier is focused.** Hyprland has no per-window
bind filter, so [contrib/df-hud.lua](contrib/df-hud.lua) subscribes to
`window.active` and calls `set_enabled` on each bind: while you are in a browser they
are not registered at all. That is what makes a BARE key affordable for the map -
outside the game a lone backtick or tab is still a backtick or a tab in every
terminal you have, which is why the file uses one. Two
consequences worth knowing before leaving it on - the overlay toggle stops
working when you alt-tab off the game, which is sometimes exactly when you want
it (the overlay is hidden by workspace, not by focus, so a window in front of the
game on the same workspace still has the HUD over it), and a disabled bind is
silent, which looks identical to df-hud being down. `only_when_game_focused =
false` at the top of that file turns the whole thing off; `always = true` on one
`bind_action` exempts one key.

The click catcher is gated the same way and not optionally: a global bind on the left
mouse button forks a curl on every click you make all day. `hyprland.conf` can
express none of this - see the note at the top of
[contrib/df-hud.hypr.conf](contrib/df-hud.hypr.conf) for the nearest approximation.

Then `hyprctl reload`, and `hyprctl binds -j | grep df-hud` to see the six binds.
That lists them whether or not they are armed - Hyprland does not report the enabled
flag - so the way to check the gate is to press one in another window and get nothing.
There is a stubbed-`hl` check for that file in
[contrib/df-hud_spec.lua](contrib/df-hud_spec.lua), run by `go test`, because a
mistake in it is otherwise only visible in the compositor's log.

The tray menu offers the same corrections, and the two stay in step: toggle the
overlay or the challenge board from a key and the tray's tick follows.

A group toggle is deliberately **not** written to the config and does not survive a
restart. It answers a different question from `enabled`: `enabled` is "do I ever
want this group", which belongs in a file, while the key is "get the board off my
screen for a minute", which you undo thirty seconds later. A HUD that started with
a group missing because of a keypress in a previous session would be
indistinguishable from a broken one. The status banner cannot be hidden at all - it
is how df-hud says it cannot do its job.

### Starting the clock from the game's own Start button

**Off by default since 2026-08-14, because it crashed the compositor.** A left click
segfaulted Hyprland 0.56.2 inside its own Lua bindings: the mouse button reached
`CKeybindManager::handleKeybinds`, which `pcall`ed this bind's Lua function, which
called `hl.exec_cmd`, and the argument check for that died on a null dereference. The
session went with it. That is a Hyprland bug - a compositor must not segfault on a
callback its own config handed it - but this bind is the trigger, and it is the only
one in `df-hud.lua` whose dispatcher is a Lua *function* rather than a dispatcher
object built at load time. Every key is dispatched in C++ and none of them appeared in
the backtrace. `catch_start_button = true` turns it back on; `SUPER + T` does the same
job by hand. The long note in that file has the evidence.

The rest of this section is why the feature exists at all.

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

`font_family`, `font_size` and `color` are per group and optional; absent means
"use the `[hud]` values", so a group only carries the keys it differs on. The
position defaults were measured at 2560x1440, so another resolution wants its own
numbers - and on a scaled monitor these are logical pixels, not device ones.

A group's `color` is its **normal** colour. The colours that carry meaning still
win over it: the error banner's red, an outpost attack's red, the amber on a block
with bandits on it, and the amber or red on a rate whose polls have been landing
badly. Those say something the words do not, so a colour preference does not switch
them off - which is why the state rules in the built-in sheet are scoped one level
deeper than the per-group ones rather than merely appearing earlier in it.

`[widget.status]` has no `color` at all: the banner is red when you cannot fix the
problem and amber when you can, and that is the whole message. It takes a position
and a font like every other group.

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
| `show_sections` | a divider naming each category, and a shared prefix said once |

`show_repeatable` is named after the wire, not the event. The board marks these
`repeatable`, verified against a live fetch: `1` on exactly the three Summer
challenges and `0` on every daily, weekly and clan entry. Matching on the name
would break the moment the event changes; matching on the flag will not.

There used to be pinning here, showing two or three challenges chosen by name. It
existed because a dozen rows in a shared corner would bury everything else. With
the board in its own place on screen there is nothing left to bury.

Each challenge is a group: the challenge on one row, its objectives indented
underneath. A weekly with four objectives is why - four of them on one line is a
wall of text, and the sum of four different tasks is not something you can act on
anyway. A challenge whose name already says its objective stays one row, which is
how every clan entry reads.

Three levels of hierarchy, built out of weight, size and alpha rather than colour,
so they compose with whatever `color` the group is set to:

```
── event ───────────────────────────────
Summer Death
  Kill Regular Infected  55/100
Summer Loot                              green, struck through
  Loot Anything          10/10
── yours ───────────────────────────────
Nearly There             20m             red: 20 minutes left
  Kill Dogs              95/100
── clan - Weekly Challenge ─────────────
Kill Infected            159,487/162,401
Travel Blocks            366/360
```

The progress is bold, because it is what you scan the board for. The objective is
dimmed, because it is subordinate to the challenge it belongs to. A finished
challenge or objective is **green and struck through**; one that is unfinished with
less than `urgent_within` left (default two hours) is **red**.

The dividers pay for their own rows. Every clan entry is called "Weekly Challenge -
something", so five of them said that eighteen-character prefix five times and
pushed every figure on the board right by as much; it moves into the heading
instead, where it is said once. A prefix has to be shared by at least two
challenges and end at a separator - "Kill Infected" and "Kill Dog Infected" share
`Kill ` and stripping that would leave "Infected" and "Dog Infected" - and it is
only ever taken away when there is a heading to move it to. Categories appear in
the board's own order, so the dividers describe what is on screen without
rearranging it, and a single category on screen gets no heading at all.

They are drawn as text, not as a CSS border, because a GTK label is only as wide as
its own content: `border-bottom` would stop at the end of the word.

Every value sits in one column, and every challenge after the first gets a few
pixels above it, so a board of nineteen rows reads as groups. The column is built
out of padding characters, which is exact in a monospace font and approximate in
anything else - and is why the objective is dimmed rather than drawn a size
smaller, since a narrower character would make the same count of padding a
different width and drift the column on exactly the rows it exists to line up.

Everything from the board is escaped before it reaches Pango. That is not
optional: Pango refuses to parse malformed markup and GTK answers with an empty
label, so one challenge named with an ampersand would silently blank its own row.

### Which way to walk

Most blocks have nothing on them, and an empty group says nothing at all. So when
the feed has no event where you are standing, the boss group reports the nearest
one instead:

```
nearest 4 up 1 left  1057, 1016
```

Direction in block moves, then the block's own coordinates. Both, on purpose: the
words are what you read at a glance, and the coordinates are the form the game
itself shows you, so they are both the actionable version and the way to catch this
being wrong.

**Nearest means nearest to walk to.** The city is not a rectangle: 1716 of the 3245
coordinates in its bounding box are blocks, and the rest are gaps you have to go
around. So df-hud holds the city's shape ([citymap.txt](citymap.txt), and
[knowledge/city-map.md](knowledge/city-map.md) for where it comes from and what was
checked), searches outward from the block you are on, and picks the event with the
shortest walk - which is not always the one with the smallest difference in
coordinates. When the walk is longer than the directions add up to, the row says so:

```
nearest 3 up 1 left, 8 blocks  1015, 1024
```

**Up is `y` decreasing**, which was the one claim here that could have sent someone
the wrong way. It started as an inference - DFProfiler's map is an HTML table whose
rows are `y`, so the smallest `y` renders topmost - and was checked in the game on
2026-08-13 by walking one block north and watching the second coordinate fall. The
coordinates stay beside the words regardless, since they are what the game's own
readout shows.

That also settles the map's orientation: [citymap.txt](citymap.txt) is stored with
`y` increasing downwards and drawn the same way, so north on the overlay is north in
the game.

Anything more than a dozen blocks away is not reported - it would be furniture
rather than information - and `show_nearest = false` turns it off.

## The city map

The map key draws the whole city: one cell per block, shaded by difficulty
band the way DFProfiler's own map shades it, the gaps left empty, the district
lines heavier, an identifier on every active event and a white ring on the block
you are standing on.

```
    +---------------------------------------+   B5 6m24s  6 x Bandits
    |  the city, 59 x 55 blocks             |   DH 6m24s  1 x Devil Hound
    |  gaps are gaps: you walk around them  |   N7 6m24s  3 x Evolved Longarms
    |  N D P F S C Z are the outposts       |              1 x Irradiated Wraith
    |  B5 DH N7 I3 say what is standing     |              1 x Mega Mother
    +---------------------------------------+   I3 6m24s  4 x Evolved Longarms
```

### The legend

**DFProfiler's scheme, because their map is the one people already read.** A letter for
what sort of place it is, a number for which one:

| | |
|---|---|
| `B1`..`B6` | bandit camps, ascending into the endgame |
| `I1`..`In` | inner city bosses - one enemy type |
| `N1`..`Nn` | nests - several types on one block |
| `M1`..`M5` | missions, one per outpost |
| `Δ` | a QRF, numbered only when more than one is up |
| `DH` `VL` `BH` `LB` | today's daily: Devil Hound, Volatile Leaper, Behemoth, Legendary Bandits |

The numbers are the **game's own event slots** (`boss_num`), not a ranking of anything
about you, and that is the point: `B4` means the same camp all cycle, and the same camp
their map calls `B4`, so it is a thing you can say out loud to another player. Within a
type the slots ascend with difficulty - measured on a real cycle, the camps sit at
slots 1, 2, 3, 14, 16 carrying 1, 2, 2, 4 and 6 bandits - so ranking the active ones
gives `B1..B6` in the order the city gets harder. A mission's slot **is** its outpost
(0 Nastya's, 1 Fort Pastor, 2 Dogg's, 3 Precinct 13, 4 Secronom), so `M2` is Fort
Pastor's every time.

This replaced identifiers renumbered nearest-first, which read beautifully and could
not be said to anyone: they changed as you walked. The **key** is still sorted nearest
first, because the order of a row is not the name of it.

**A ring only where a ring says something the identifier does not:** today's daily, a
mission, a QRF. All four dailies share one colour - the question a ring answers is "is
today's event here", and the initials already say which it is. Bandits, bosses and
nests have no ring at all now; `B`, `I` and `N` name themselves, and ringing all three
put a coloured box around two thirds of the map. A nest that happens to contain the
daily keeps its `N` number and gains the ring, since a nest of six things is not "the
Devil Hound".

Not on `M`, which is the game's own map key. A consuming compositor bind should mean
the game never sees the keypress, but a client that polls raw key state does not care
what else is held down, so bind something the game does nothing with - and the game's
own bindings are readable rather than guessable, in the Wine registry under
`HKCU\Software\Creaky Corpse\Dead Frontier`. See
[knowledge/game-keybinds.md](knowledge/game-keybinds.md), which exists because a bare
`` ` `` looked free and is the game's **chat** key.

`radius` crops it to a square around you - `radius = 15` draws 31x31 blocks - and
cropping **zooms in rather than shrinking**: `scale` is a pixel budget for the longest
side, so the same 1180px over 31 blocks gives 38px cells where 59 blocks gave 20. The
key is cropped to match, since a row about a boss you cannot see on the map is a row
about nowhere. At the full 59x55 most of the picture is somewhere you are not going.
The window is clamped into the city rather than hanging off the edge, so its size
never changes and the map does not jump sideways as you approach a boundary; near an
edge you are simply off-centre.

`offset_x` and `offset_y` nudge it in pixels - negative is left and up - applied **on
top of** the centring rather than instead of it, so "60 up from centre" keeps meaning
that when the scale, the radius or the monitor changes. That is the whole reason not to
use `x`/`y` for it.

**It starts hidden**, and the key brings it up. That is not the same as
`enabled = false`: this is something you summon to decide where to walk and dismiss
ten seconds later, and a thousand pixels of city permanently over the game would not be a
HUD, it would be a wall. It is `center`ed on the monitor by default, because the
right coordinate depends on both the monitor and `scale`.

`scale` is the only size key: **one number sizes the grid and the key beside it**, so
the two cannot drift apart. It is a pixel budget for the longest side rather than a
size per block - 1.0 is 1180px across, 20 per block at the full city, a 13pt key -
which is what makes `radius` zoom in: the same budget over 31 blocks gives 38px cells.

Everything about it follows from being **a widget group rather than a window**, and
each of the alternatives was tried first:

- an ordinary window sits *behind* a fullscreen one, so a map opened while playing
  is drawn under the game - invisible at exactly the moment it is wanted. It also
  gets tiled and resized, which silently clipped the east side of the city off a
  grid that had asked for its full width.
- a second layer surface would need its own copy of the monitor pinning, the
  workspace following and the show/hide rules the HUD already has.
- 1716 labels was the first draft, for the sake of per-cell tooltips - which are
  worth nothing on a surface that passes every pointer event through to the game.
  One `GtkDrawingArea` draws the same thing in one widget.

**The identifiers are numbered by distance**, over what is visible: 1 is the nearest
thing, and the key runs down the page in that order. They are assigned per frame rather
than taken from the feed's own order, which had two problems - a cropped map showed a
sparse scatter of whatever characters the feed happened to hand those events (G, K, Q,
V), and a busy cycle of thirty-odd events overflowed the digits and the capitals that
are not already an outpost's letter, so it drew a lowercase `c` beside Camp Valcrest's
`C`. The cost is that a boss's character changes as the order shifts, which is the right
trade: it is a lookup within one glance, not a name.

**The ring colour says what it is**, on the grid and as the chip behind the same
letter in the key: magenta for a nest, red for a single boss, amber for a bandit pack,
blue for a mission, green for a QRF. Those are four different decisions - a nest is
somewhere to avoid unless you came for it, a bandit pack is loot, a mission is not a
fight at all - and one colour for all of them made the map say "something is here"
and nothing else. A nest is a spawn carrying more than one enemy type; bandits are
recognised by name, because the feed hands them over in the same field a boss arrives
in.

Onslaught is left out unless you are standing in it. Its cycles sit on `3000,3000`,
which is a real coordinate but not a place on the grid, so out in the city they are
several rows a cycle you can do nothing about - and they are filtered before
identifiers are handed out, so the letters on the map have no gaps in them.

So **the mouse still reaches the game through it**, and there is no hover. The key
beside the grid does that job instead: one entry per event, nearest first, the
identifier in its own colour on a dark chip so it can be found at a glance, the
countdown, and the enemy types - one per row for a nest, since seven of them joined
into one line is 140 characters and runs off the screen taking the dangerous part
with it.

One entry per **event**, not per block. The feed puts the same bandit pack on a
dozen blocks at once - 185 marks from 30 events in one live capture - so a row each
made the list "+173 more" with the same enemies and the same countdown repeated a
dozen times. The entry is ordered by the nearest of its blocks, which is why the
distance itself is not written: the map is where you see where something is.

The shades are reproduced because they make the map recognisable to anyone who
already knows the real one. **What they mean is not known** - whether a band equals
a `df_dangerlevel` range has not been checked - so nothing is derived from them,
they are only drawn. See [knowledge/city-map.md](knowledge/city-map.md).

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
closes or relaunches. `SUPER + T` and the tray both restart it from now, for
when it starts before you do.

**Every run is written to the journal as it ends**, since the clock leaving the screen
is otherwise the last you hear of it:

```
$ journalctl --user -u df-hud | grep "run ended"
Aug 14 21:04:12 df-hud[71701]: session: run ended after 23m41s (the game closed)
Aug 14 22:17:03 df-hud[71701]: session: run ended after 8m12s (you died)
Aug 14 22:41:55 df-hud[71701]: session: run ended after 31m07s (the record says outpost)
```

All four endings go through one place, which they did not before: outpost and death
were logged, while closing the game - the commonest way a run ends - cleared the clock
silently.

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

The city's own shape comes from there too, and this one is **not** a runtime
request. `citymap.txt` is generated once by `tools/citymapgen` from their bossmap
stylesheet - the only published thing that knows which coordinates are blocks and
which are the gaps between them - then committed and embedded. The map changes when
the game changes, so re-deriving it from someone else's CSS on every start would be
both fragile and rude. `knowledge/city-map.md` records what was checked against it.

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
make            # go build, with the commit stamped into -version
./df-hud -check-config
./df-hud
```

`go build -tags nolayershell` builds the headless core with no GTK at all, for
CI or a machine with no Wayland. `make check` is everything CI runs: `gofmt`,
`go vet`, `go test -race`, and that headless build.

## Running it

```sh
make install    # binary to ~/.local/bin, unit to ~/.config/systemd/user
make enable     # start it, and at every login from now on
make logs       # journalctl -f
```

A **systemd user service**, not a terminal you have to keep open and not an
`exec-once` in `hyprland.conf`. What that buys, in the order it matters:

- **It comes back.** `Restart=on-failure`, and five failures in a minute is taken
  as a broken config rather than something to retry forever - a restart loop
  buries the reason in a thousand identical journal entries.
- **The logs are somewhere.** `journalctl --user -u df-hud` has yesterday's, which
  is what makes "why did that take five seconds" a question you can answer.
- **Its lifetime is the compositor's.** `PartOf=graphical-session.target`, so it
  starts with Hyprland and stops with it. A leftover df-hud with no compositor
  would sit there failing to find one, once a second, until you noticed.
- **The running binary is not the one you are editing.** `make install` copies to
  `~/.local/bin`; rebuilding under a running process otherwise leaves it holding
  the old inode, so the log claims one version and the file on disk is another.

`systemctl --user reload df-hud` re-reads the config **without restarting**, so the
run clock and the XP window survive - SIGHUP is handled for exactly that reason,
since its default disposition is to terminate and a "reload" that kills the daemon
is not a reload. A restart is safe too: the run is tied to the game's own process
and resumes rather than starting from zero.

`make restart` rebuilds, reinstalls and restarts in one step, which is the edit
loop while working on df-hud itself.

Nothing here is required. `./df-hud` from a terminal still works and is still the
right way to try a change before installing it.

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
  XP table, why cumulative XP needs care, and the two map grids in that feed that
  turned out not to describe this city at all
- [city-map.md](knowledge/city-map.md) — where the city's shape comes from, and
  the three independent checks it passed before anything was routed with it
- [bossmap.md](knowledge/bossmap.md) — the event feed: its cycles, the
  changeover that arrives before it happens, and what Onslaught's 3000,3000 means
- [player-record-and-signing.md](knowledge/player-record-and-signing.md) — the
  342-field player record, the two different time encodings (mixing them up is
  worth 38 years), and how requests are signed

GPL-3.0.
