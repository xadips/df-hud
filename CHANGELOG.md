# Changelog

Notable changes to df-hud. Release notes get cut from the Unreleased section.
Format loosely follows [Keep a Changelog](https://keepachangelog.com/); newest
first within each section.

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
