# wgl-overlay-spike report

Fill this after running `README.md`. Paste the same text back to the Linux rewrite session.

## Machine

- GPU: overlay on Intel(R) Iris(R) Xe Graphics (iGPU); game on NVIDIA GeForce RTX 4060 Laptop GPU (D3D9)
- Windows: Microsoft Windows 11 Pro 10.0.26200
- Built with: native `cargo build --release` (`stable-x86_64-pc-windows-gnu` 1.98.0), not the zigbuild prebuilt
- Go df-hud during test: off

## Game

- Presentation: Unity 4.7.2 `Screenmanager Is Fullscreen mode=1` at 1920x1200 (native), HWND `WS_POPUP` covering the monitor — exclusive-style D3D9 fullscreen (Windowed unchecked)
- Monitor (`--list-monitors`): `\\.\DISPLAY1` 1920x1200 at 0,0 dpi 96 primary (only display)
- How they actually play (must match at least one A+B run): fullscreen as above; A+B were this mode

## Console (paste)

```
DPI: PerMonitorV2
monitor: \\.\DISPLAY1  1920x1200 at 0,0  dpi 96  primary
window 1918x1198 at 1,1  inset 1  (1px gap at the monitor edge is expected)
pixel format 2  color 32  alpha 8  flags=0x8025
GL renderer=Intel(R) Iris(R) Xe Graphics version=3.3.0 - Build 32.0.101.7084
DWM: layered alpha 255 + extend-frame + blur-behind empty region
hwnd layered+topmost+tool+noactivate+transparent
```

## Gates

| Gate | Result | One sentence |
|---|---|---|
| 3 WGL layered HWND (pixels, alpha, 1px inset) | pass | Opaque magenta + outlined text covered the full monitor minus 1px; alpha 8, GL 3.3 core. |
| 3+4 over Dead Frontier in their usual mode | pass | Magenta then cyan sat over the live fullscreen game, not only the desktop. |
| 4 exclusive fullscreen (if tested) | pass | Usual mode is Unity fullscreen=1 / D3D9 exclusive-style; overlay still visible. |
| Click-through after SwapBuffers | pass | Cyan box visible; mouse-look, clicks, WASD still hit Dead Frontier. |
| 5 swap-interval 0 @ 1 Hz walking | pass | Walking felt like no overlay. |
| 5 swap-interval 1 @ 1 Hz (allowed to hitch) | pass | Walking was fine (briefly maybe-hitch, then confirmed fine). |
| Premult / outlined text | pass | Outline crisp; alpha ramps milky, no dark fringe. |
| Hide/show + click-through after | pass | Cyan vanished and came back; clicks passed through on both appearances. Console `hidden` then `shown`. |

Kill Phase 0? **no**

Notes:

- Class is `df-hud-wgl-spike`, not GLFW `GLFW30`. One `SetWindowPos` onto the HMONITOR.
- Zigbuild prebuilt died after 1 swap: dummy WGL `DestroyWindow` set the process `CLOSED` flag via the shared wndproc. Fix: `clear_closed()` after dummy teardown.
- First fair 15s run with only `DwmExtendFrameIntoClientArea` was fully invisible (no magenta over game or desktop). Visible after `SetLayeredWindowAttributes(..., 255, LWA_ALPHA)` plus `DwmEnableBlurBehindWindow` with an empty region (`CreateRectRgn(0,0,-1,-1)`). Still raw WGL / `windows-sys` / `dwmapi`, not GLFW.
- `--inset 0` hole test skipped.
- Windowed / borderless contrast not run; user plays this fullscreen mode.
