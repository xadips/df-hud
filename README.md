# df-hud

A Wayland-native heads-up display for Dead Frontier, for Hyprland.

It draws over the game — including fullscreen — using `zwlr_layer_shell_v1`, and
passes every pointer event through to the game underneath. Data comes from the
game's own API, so there is no screen scraping and no OCR anywhere.

Replaces SilverOverlays, a Windows PyQt app that under Proton gives no reliable
always-on-top over a fullscreen game, no global hotkeys on Wayland, and no tray.

## Status

Working today:

- **Block Info** — where you are, which outpost, which region, danger level,
  block-support countdown
- **Session clock** — time since the game client launched, read from the game
  process's own start time, so it stays correct if df-hud starts late or is
  restarted mid-run
- **XP/hr** — averaged over a window, with the colour carrying whether recent
  polls actually landed
- **Challenge tracker** — personal and clan, with progress, completion and
  deadlines. Pin by name, or let it show whatever you are closest to finishing
- A headless core with `-once`, `-print-view`, `-print-hud`, `-dump-fields`,
  `-dump-challenges`, `-check-config` and `-check-game` for looking at real data
  with no GUI

Planned: a console window for pin toggles, and a boss map for Block Info.

## How it gets your session

The game's API authenticates with `userID` + a per-session password hash + `sc`,
and those exist only inside a logged-in page's JavaScript. A userscript posts
them to a **loopback-only** listener on `127.0.0.1:9275`; df-hud makes every game
request itself.

Either script works:

- **the bridge userscript** (or the bridge userscript) — already posts to that exact endpoint.
  Load the Outpost home page and you are done.
- **the bridge userscript** (`the bridge userscript`) — also reports the request
  signing salt, which hashed endpoints such as the challenge board need, and
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
- the session cookie is accepted for wire compatibility and then **discarded** —
  the API does not need it, so keeping it would put another secret on disk for
  nothing

## Being a good citizen

The game server is not ours, and bursty request patterns get accounts temp-banned.

- **Two schedulers, one budget.** Nothing outside them makes a request, so the
  traffic budget is a single number rather than an emergent property.
  `-check-config` prints it: about **480 requests/hour while playing, 60 idle**
  at the defaults (the player record every 10s, the challenge board every 30s).
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
df-hud rather than a bug in your config. Editing the file while running reloads
it; three keys need a restart and say so.

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
