# Changelog

Notable changes to df-hud. Release notes get cut from the Unreleased section.
Format loosely follows [Keep a Changelog](https://keepachangelog.com/); newest
first within each section.

## [Unreleased]

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
