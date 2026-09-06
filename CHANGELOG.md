# Changelog

Notable changes to df-hud. Release notes get cut from the Unreleased section.
Format loosely follows [Keep a Changelog](https://keepachangelog.com/); newest
first within each section.

## [Unreleased]

### Added

- `DF_HUD_LOG=error|warn|info|debug` (default `info`) sets how much goes to
  stderr, or to `df-hud.log` on Windows.

### Fixed

- The public-record probe no longer switches to authenticated requests for
  good after one transient error (a Cloudflare hiccup, say); it tries the
  public record again after an hour.
- Windows: GL objects were deleted after the WGL context was already gone on
  shutdown, and a `wglGetProcAddress` failure value of `-1` was treated as a
  valid pointer. Both fixed.
- `--once` JSON: the `Challenge` serializer declared the wrong field count
  (harmless with serde_json, now correct).
- Sticky challenge-completion memory is pruned once a cycle ends instead of
  growing forever. Old `state.json` files still load.
- `~/` in config paths (`paths.data_dir`, the font) expands on Windows too, via
  `USERPROFILE`.

### Changed

- The overlay skips the redraw and buffer swap when nothing on screen changed,
  so an idle frame costs a comparison.
- Fewer background wakeups: config reload (`SIGHUP`), the state saver,
  presence, and the headless loops block instead of polling, and the catalog
  and city-map fetches share one timer thread. Windows game detection
  re-checks the known process instead of snapshotting every process each tick.
- One HTTP connection pool for every request (was up to four). The city map
  is only re-parsed when its `bosshash` changes.
- The bridge and presence servers cap concurrent connections at 8; the bridge
  answers 503 past that.
- Credentials are redacted from debug output, and the wake pipe is
  close-on-exec.
- Internal: `Config` is shared as an `Arc`, one generic board poller, a shared
  overlay tick, `enum Group`, `Option` instead of `has_*` flags, SAFETY
  comments on every `unsafe`, `[lints]` at deny, and an MSRV CI job.

## [0.4.12] - 2026-09-06

### Fixed

- A second browser tab (including an incognito one with the bridge script)
  can no longer overwrite the session while `DeadFrontier.exe` is running,
  or for a few seconds after Launch Standalone (`source=launch`) before the
  process appears. The same account still refreshes. Launch while the client
  is already running does not switch; close it first, then load or Launch
  Standalone from the other account. Update [DF HUD Bridge](https://greasyfork.org/en/scripts/592954-df-hud-bridge)
  to 1.11 as well: the Inner City lobby has no `userVars`, so the script
  reads the Back to Outpost form and POSTs `source=launch` before following
  Launch Standalone.

## [0.4.11] - 2026-08-28

### Added

- Masteries widget (`[widget.masteries]`, **off by default**): your mastery
  levels and progress to the next one, one row each, polled from the game's
  masteries endpoint at `poll.mastery_interval` (30s while playing, like the
  challenge board). Masteries
  whose every bonus has hit its cap are hidden by default (`show_mastered`),
  Artisan is hidden by default because it only levels from outpost work
  (`show_artisan`), and `pin = ["Melee Expert"]` shows only the masteries you
  are actively watching. The tray item reads **Enable masteries widget** while
  the widget is off in the config and enables it in the file with one click;
  after that it is a runtime visibility toggle (**Show masteries**), same as
  `POST /api/widget/masteries/toggle`. Needs the bridge script, like the
  challenge board.
- `--dump-masteries` prints your masteries once (levels, per-bonus values and
  caps), or the raw fields with `--dump-fields`.
- `hotkeys.masteries` (default `4`, the first number past the game's weapon
  slots) toggles the masteries widget. The key is only grabbed while
  `[widget.masteries]` is enabled, so an opted-out install leaves `4` with
  the game.
- The tray has **Check for updates**: one probe of the GitHub release page,
  only when clicked.

### Fixed

- First-start seeding on a panel that is not 2560x1440 (Windows stamps
  `hud.reference_*` from the primary monitor) now rescales the widget
  coordinates to that panel, so every group seeds at its authored screen
  fraction instead of drifting down - or off - shorter screens.

### Changed

- The example config's comments are one or two lines per key; the reasoning
  moved to `docs/configuration.md` and `docs/widgets/`.
