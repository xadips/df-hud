# The game's own keybinds, and where to read them

df-hud's keys are compositor binds, and a consuming bind means the game never sees
that key - including in its own chat box. So "is this key free?" is a question with a
real answer, and it is not in any file the game ships.

## Where

The launcher's Input Configuration writes to the WINE REGISTRY, as Unity PlayerPrefs:

```
HKEY_CURRENT_USER\Software\Creaky Corpse\Dead Frontier
```

which under Proton is a section of the prefix's `user.reg`:

```
~/.local/share/Steam/steamapps/compatdata/<id>/pfx/user.reg
    [Software\\Creaky Corpse\\Dead Frontier]
```

The `<id>` is the compatdata directory for however the game was added to Steam; for a
non-Steam shortcut it is a large generated number rather than an app id.

Entry names are Unity's own mangling - the action name with a hash of it appended:

```
"__Input Key PosChat_h1675764054"="`"
"__Input Key Alt PosChat_h2709696879"="\\"
```

`Pos` and `Neg` are the two directions of a Unity axis; for a plain action only `Pos`
is meaningful. `Alt` is the secondary binding. An empty string means unbound.

To read them all:

```sh
P=~/.local/share/Steam/steamapps/compatdata/<id>/pfx
awk '/^\[Software\\\\Creaky Corpse\\\\Dead Frontier\]/{f=1;next} f&&/^\[/{exit} f' \
    "$P/user.reg" | grep '^"__Input'
```

The same key holds the rest of the game's client-side settings - crosshair colour and
size, quality, target FPS, music, and a per-character block of position and state.
Nothing in it is a credential.

## What was bound, 2026-08-15

| action | primary | alt |
| --- | --- | --- |
| Action | `e` | `f` |
| Chat | `` ` `` | `\` |
| Chat 2 | `\` | |
| EnlargeMinimapToggle | `[` | |
| Fire1 | `mouse 0` | `u` |
| FireToggle | `z` | |
| Run | `space` | `left shift` |
| Submit | `return` | `escape` |
| UICancel | `escape` | `q` |
| Weapon 1 | `1` | |
| Weapon Cycle | `]` | `[`, `=` |

## The caveat that matters

**This is only what the launcher exposes.** `m` opens the map in game and appears
nowhere in the table, so some keys are hardcoded and invisible here. Reading this rules
a collision IN; it cannot rule one out.

It also cost something to learn: the map was briefly bound to a bare `` ` `` with a
comment claiming the game did nothing with grave. Grave is the CHAT key, and a
consuming bind would have eaten it. Check here before claiming a key is free.
