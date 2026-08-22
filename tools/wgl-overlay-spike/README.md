# wgl-overlay-spike

Throwaway **raw WGL** overlay for the df-hud rewrite. Native Windows only.

This is the kill-test the Linux session could not run. Linux EGL on layer-shell already passed over fullscreen Proton Dead Frontier (Hyprland + Mesa). Do not start Phase 0 product code until this spike passes **over the real game**.

`tools/windows-overlay-spike` is a different program. It uses **Ebitengine/GLFW**. A pass there does not prove the rewrite. Test **this** crate.

Wine / Proton on Linux is not a DWM proof.

## What is being tested

Plan gates from `.cursor/plans/overlay_rewrite_architecture_bc10a5ef.plan.md`:

| # | Gate | Fail means |
|---|---|---|
| 3 | Raw WGL on a layered HWND (no GLFW): `WS_EX_LAYERED \| TRANSPARENT \| TOPMOST \| NOACTIVATE \| TOOLWINDOW`, GL 3.3 core, alpha pixel format, 1px inset | Pick a different Windows overlay stack. Do not port `Derive`. |
| 4 | Same spike over Dead Frontier in the presentation mode the user actually plays | Exclusive fullscreen can hide every overlay. If that is how they play and this cannot show, the rewrite cannot either. |
| 5 | `wglSwapIntervalEXT(0)` at 1 Hz while walking | Interval 0 must feel like no overlay. Interval 1 is **allowed to hitch**. |
| 11 | This `opengl32` path actually loads on a real Windows box | Cross-compile success is not a GL proof. |

Should-pass (fail = tweak, not abandon): premult outlined text, hide/show with context kept, click-through still on after `SwapBuffers` and after hide/show.

The product already proved a *transparent GLFW* overlay. The unknown is **our** `CreateWindowEx` + WGL, especially DWM punching an opaque hole if the window is full-monitor with no inset or no alpha bits.

## Do not

- Use Wine.
- Test over a terminal or Notepad as the only proof. A desktop window is a sanity check; the gate is **Dead Frontier**.
- Run the Go `df-hud.exe` at the same time (it will fight for the topmost layered slot).
- Use `tools/windows-overlay-spike` (Ebitengine).
- Start rewrite Phase 0 from a fail of gates 3–4.

## Prebuilt exe (this branch)

`tools/wgl-overlay-spike/wgl-overlay-spike.exe` is a Linux-cross-compiled `x86_64-pc-windows-gnu` build. Copy it to the Windows machine and run it from a console. SmartScreen may warn; that is not a spike fail.

If it will not start (missing CRT), rebuild natively below.

## Build

On the Windows laptop (preferred — MSVC or gnu both fine):

```bat
cd tools\wgl-overlay-spike
cargo build --release
target\release\wgl-overlay-spike.exe --help
```

From Linux (optional):

```sh
cargo zigbuild --release --target x86_64-pc-windows-gnu --manifest-path tools/wgl-overlay-spike/Cargo.toml
```

If SmartScreen blocks the gnu exe, that is not a spike fail. Unblock or rebuild natively.

Keep a **console** visible. This binary is a console subsystem app so `FAILED:` and GL logs are readable.

## Setup

1. Install / launch Dead Frontier the way the user plays (Steam, exclusive vs borderless vs windowed). Write down which.
2. Quit or disable the installed Go df-hud (tray exit). Two overlays confuse the test.
3. `--list-monitors` first. Pin with `--monitor \\.\DISPLAY1` (or whatever name owns the game).
4. Look at the **game monitor**, not the Cursor laptop screen if they are different.

Expected chrome: a **1px gap** at the monitor edge (`--inset 1`). That is the DWM alpha workaround, not a bug. `--inset 0` is the negative test for the opaque hole.

## Runs (in this order)

Each command is a separate process. Default duration is 30s.

### A — pixels exist (`--solid`)

```bat
target\release\wgl-overlay-spike.exe --monitor \\.\DISPLAY1 --solid --hz 10 --swap-interval 0 --duration 15
```

**Pass:** opaque magenta covers the game (or the whole monitor minus 1px). Console prints `GL renderer=...` and `alpha 8` (or more).
**Fail:** nothing; or a black/white **opaque hole** that hides the game but is not magenta; or the game is exclusive-fullscreen and the overlay is invisible. Screenshot + Unity fullscreen setting.

### B — click-through (must-pass)

```bat
target\release\wgl-overlay-spike.exe --monitor \\.\DISPLAY1 --hz 10 --swap-interval 0 --duration 25
```

Centered **cyan** panel + outlined text. For ~20s: mouse-look, click, WASD in the game.

**Pass:** cyan is on the game; look and clicks still hit Dead Frontier; window does not steal focus (taskbar / click the game).
**Fail:** cursor sticks on the HUD; clicks do nothing in-game; overlay activates and Alt-Tabs the game. Re-asserting `WS_EX_TRANSPARENT` is an acceptable product fix; *unable* to keep passthrough after `SwapBuffers` is a kill.

### C — hitch (must-pass at interval 0)

Walk around in-game:

```bat
target\release\wgl-overlay-spike.exe --monitor \\.\DISPLAY1 --hz 1 --swap-interval 0 --duration 20
```

Then the allowed-to-fail contrast:

```bat
target\release\wgl-overlay-spike.exe --monitor \\.\DISPLAY1 --hz 1 --swap-interval 1 --duration 20
```

**Pass:** interval 0 feels like no overlay.
**Allowed fail:** interval 1 hitches. Report it; do not kill the stack for that alone.

### D — premult / text (should-pass)

```bat
target\release\wgl-overlay-spike.exe --monitor \\.\DISPLAY1 --text-only --hz 10 --duration 15
```

White 5×7 text, black 8-neighbor outline, four white alpha ramps (25/50/75/100%).

**Pass:** outline is crisp, not a sooty halo. Ramps look milky over the scene, not black-edged.
**Fail:** dark fringes around glyphs or ramps. Note it; shader already outputs premult.

### E — hide / show (should-pass)

```bat
target\release\wgl-overlay-spike.exe --monitor \\.\DISPLAY1 --hide-at 5 --duration 18 --hz 10
```

Cyan ~5s, gone ~5s, back until exit. Console: `hidden` then `shown`. After it returns, mouse-look still hits the game.

**Fail:** crash, black hole, or clicks captured after show.

### F — exclusive vs borderless (must-pass 4)

If the user plays **exclusive fullscreen**, repeat **A** and **B** in that mode. If the overlay cannot appear, that is a **kill for exclusive fullscreen**, not for borderless. Record both if they can switch.

`--inset 0 --solid` (optional): if magenta becomes an opaque hole, the 1px inset is required. That is a design note, not a kill.

## What to put in the report

Paste this block back to the Linux session (or fill `REPORT.md` and commit it on this branch). One line per gate. Quote console lines for `GL renderer`, `pixel format`, `alpha`.

```
## wgl-overlay-spike report

Machine: <GPU, Windows version>
Game: Dead Frontier, <exclusive / borderless / windowed>, monitor <\\.\DISPLAYn>
Built: <native cargo / zigbuild gnu>
Go df-hud: off

3 WGL layered HWND over desktop: pass / fail — <one sentence>
3+4 over Dead Frontier (their usual mode): pass / fail — <visible? magenta/cyan?>
4 exclusive fullscreen (if tested): pass / fail / not used
Click-through after SwapBuffers: pass / fail
5 swap-interval 0 @ 1 Hz walking: pass / fail
5 swap-interval 1 @ 1 Hz: pass / fail / hitch (allowed)
Premult / outlined text: pass / fail
Hide/show + click-through after: pass / fail
Alpha bits (console): <n>
GL renderer/version: <paste>
Inset 0 hole (optional): <observed / skipped>

Kill Phase 0? yes / no
Notes:
```

**Kill Phase 0** if gates 3 or 4 fail on the machine and presentation mode they actually play.

## Linux spike (already done, do not re-run here)

`tools/linux-overlay-spike` on Hyprland 0.56 + RX 7900 XTX + Mesa: EGL GLES 3 on `zwlr_layer_shell_v1` overlay, exclusive -1, click-through, `eglSwapInterval(0)` at 1 Hz while walking, over Proton `DeadFrontier.exe` `fs=2`. Remap after `attach(null)` needs empty commit → configure → ack → swap.

## Flags

```
--monitor \\.\DISPLAY2
--list-monitors
--duration 0          (until the window is closed)
--hz 1 --swap-interval 0
--solid
--text-only
--hide-at 5
--inset 0
--no-clickthrough     (negative test: overlay should eat clicks)
```
