# Pressing a key in the game's window

The client's FPS readout is bound to `y` and starts OFF at every launch. There is
nothing to preset, so the only way to have it on is to press the key.

## Why there is no setting to set

The game keeps its settings in the Wine registry under
`Software\Creaky Corpse\Dead Frontier` (see [game-keybinds.md](game-keybinds.md)).
Every `$Settings$` entry that exists, read live 2026-08-16:

```
AutoQuality  Brightness  DisableMPBarricades  HideHud  HUDOutlinesSetting
LastTrack  MusicOn  PrivateInstanceKeyword  Quality  SeenInstructions
SinglePlayerOn  SoundVolume  TargetFPS2  VsyncOn2
```

`TargetFPS2` is the frame rate CAP. Nothing here is the readout's visibility, and
`y` is absent from the launcher's input table too - so like `m` for the map, it is
hardcoded and unpersisted.

## How the key is delivered

Hyprland 0.56 replaced the string dispatchers with a Lua API. The old
`dispatch sendshortcut MOD,key,window` is now:

```
dispatch hl.dsp.send_shortcut{mods="",key="y",window="address:0x55808d27d4f0"}
```

spoken straight to `.socket.sock`. Replies are `ok`, or `warning:`/`error:` with a
reason, plus a trailing note about Lua syntax on every dispatch that is guidance
for a person and noise in a log.

**Address the window, never the class or the pid.** The game and its configuration
dialogs are ONE process reporting ONE class, so both of those selectors can hit
either window. `findGameWindow` has already excluded the launcher by title, and its
address names exactly the window it matched.

The key and the address are interpolated into Lua source, so both are checked
against a strict alphabet and refused rather than escaped - a value needing an
escape is a value that could close the string and run something.

## What SilverOverlays does, for comparison

Its `utils.window_listener.wait_for_window_and_send_key` spawns a daemon thread
that polls `pygetwindow.getWindowsWithTitle()` every 0.1 s for an exact title
match, gives up after a timeout, then **calls `win.activate()`**, sleeps 0.5 s and
presses the key with `pynput`.

It has to focus the game first because pynput types wherever focus is. Addressing
the window means df-hud never steals focus, so the key can be sent while you are
doing something else. `settings.json` gates it on `enable_clock`, and the launcher
one on `skip_configuration`.

## Timing

The delay is measured from when the game's OWN window is mapped, not from when the
process starts - the process starts when the LAUNCHER opens, and that can sit on
screen indefinitely. `windowPlacement.Known` is false for both "only the launcher
is up" and "nothing is mapped yet", which makes it the gate for free.

One send per game process, claimed before the send rather than after: a failing
compositor must not become a keypress every second aimed at the game.

## The launcher dialog: dismissed, never skipped

**Do not skip the Unity resolution dialog. It is what applies your input
bindings.** This was built, shipped, and reverted the same evening.

The dialog CAN be stopped from appearing - the details are below and they all
work. What they also do is silently revert every rebind. The game reads
`__Input Key ...` out of the Wine registry only as part of the dialog running; if
the dialog does not run, the InputManager defaults baked into `mainData` are used
instead. The registry entries are still there, still correct, and never read.

Measured from `mainData`'s InputManager object (classID 13), the defaults it falls
back to:

```
Run            left shift  /  right shift
Weapon Cycle   [  /  ]  /  space
FPSToggle      y
```

so a sprint rebound to space becomes shift, and space cycles weapons. That is the
exact symptom, and it is how the cause was found.

This is also why SilverOverlays dismisses rather than skips. That looked like the
crude option; it is the only one that keeps custom keys.

So df-hud presses `Return` on the dialog, which activates its default button -
Play - by the same path clicking it takes, so the bindings are applied normally.
`launcher_title` has to be narrower than `game.window_title_ignore`: that list
holds "configuration", which matches the 215x78 **Input Configuration**
key-capture box as well, and Return there means something else.

### The skip that must not be used

Kept because the measurement is real and someone will otherwise redo it. Unity
**4.7.2f1**:

- No command-line switch suppresses it. The player binary contains
  `show-screen-selector`, which FORCES it on, and nothing to turn it off.
- No registry switch. `Screenmanager Resolution Width/Height` and
  `Is Fullscreen mode` store the dialog's ANSWER (2560x1440, fullscreen), so
  skipping it loses nothing.
- The mode is baked into `DeadFrontier_Data/mainData`, PlayerSettings (classID
  129), field `displayResolutionDialog`: `1` = Enabled, `2` = HiddenByDefault
  (shown only while Alt is held), `0` = Disabled.

To find it, parse the serialized-file header (metadata size, file size, version 9,
data offset) and the object table, take the object whose classID is 129, then
search **inside that object** for the two consecutive `-1` ints
(`iosShowActivityIndicatorOnLoading`, `androidShowActivityIndicatorOnLoading`); the
next int32 is the field. That landmark appears once inside the object and 53 times
across the file, so the object bounds are what make it unambiguous - and a
hardcoded offset would silently patch the wrong byte after an update.

Corroborating fields either side, all consistent with the running game:
`m_RenderingPath=1` (Forward), `m_ActiveColorSpace=0` (Gamma), `runInBackground=1`,
`usePlayerLog=1` (`output_log.txt` exists), `resizableWindow=0`,
`defaultScreenWidth/Height=1280x800`.

All of that is correct and all of it is a trap. The dialog is load-bearing.
