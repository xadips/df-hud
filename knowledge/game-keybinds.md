# The game's own keybinds, and where to read them

df-hud's keys are compositor binds, and a consuming bind means the game never sees
that key - including in its own chat box. So "is this key free?" is a question with a
real answer.

There are two sources, and they are not the same list.

## Factory defaults: `mainData` InputManager

Baked into `DeadFrontier_Data/mainData`, Unity classID 13, Unity 4.7.2f1. This is
what the client uses if the resolution dialog never runs (see
[sending-keys.md](sending-keys.md)). It is the stock keyboard. Read 2026-08-26 from
the live Proton prefix.

| action | negative | primary | alt-neg | alt-pos |
| --- | --- | --- | --- | --- |
| Horizontal | `left` | `right` | `a` | `d` |
| Vertical | `down` | `up` | `s` | `w` |
| Fire1 | | `mouse 0` | | |
| Fire2 | | `x` | | |
| Reload | | `r` | | |
| Run | | `left shift` | | `right shift` |
| Logout | | `l` | | |
| Inventory | | `i` | | |
| Weapon 1 | `mouse 1` | `1` | | |
| Weapon 2 | | `2` | | |
| Weapon 3 | | `3` | | |
| Weapon Cycle | `[` | `]` | | `space` |
| Chat | | `tab` | | `\` |
| Chat 2 | | `` ` `` | | |
| Chat 3 | | `return` | | |
| Barricade | | `b` | | |
| Map | | `m` | | |
| Menu | | `f1` | | `escape` |
| Instance Follow | | `f2` | | |
| Action | | `e` | | `f` |
| Toggle Fullscreen | | `f4` | | `f3` |
| Toggle PVP | | `p` | | |
| Toggle HUD | | `h` | | |
| Outpost Mode | | `o` | | |
| Run2 | | `c` | | |
| UIAccept | | `space` | | `e` |
| UICancel | | `escape` | | `t` |
| EnlargeMinimapToggle | | `v` | | |
| MinimapSnippetToggle | | `n` | | |
| FPSToggle | | `y` | | |
| Submit | | `return` | | joystick 0 |
| Submit | | `enter` | | `space` |
| Cancel | | `escape` | | joystick 1 |
| UIConsume | | `c` | | |
| FireToggle | | `q` | | |

Mouse-look axes (`Mouse X` / `Mouse Y` / `Mouse ScrollWheel`) are type-1 pointer
axes, not keys.

Letters that do **not** appear: `g` `j` `k` `u` `z`.

## What the launcher exposes

The Input Configuration dialog writes a **subset** of those axes to the Wine
registry, as Unity PlayerPrefs:

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

Axes the launcher does **not** list cannot be rebound there: Fire2 (`x`), Reload
(`r`), Logout (`l`), Inventory (`i`), Map (`m`), Toggle HUD (`h`), FPSToggle (`y`),
and the rest of the table above that is missing from the dialog. Those stay on the
`mainData` defaults.

A prefix's registry is that player's current rebinds, not the factory set. The
2026-08-15 dump (and the live 2026-08-26 prefix) already differ from `mainData`:
FireToggle rebound to `z`, Chat to `\`, EnlargeMinimapToggle to `tab`.

## The caveat that matters

A collision in either table rules a key IN. The launcher table cannot rule one out.
`Alt+T` still declines an item: a modifier does not hide the letter from Unity.

df-hud defaults are the factory-free letters, closest first: map `g`, challenges
`z`, overlay `j`, run clock `k`, xp reset `u`.
