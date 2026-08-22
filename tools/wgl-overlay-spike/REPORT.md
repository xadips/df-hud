# wgl-overlay-spike report

Fill this after running `README.md`. Paste the same text back to the Linux rewrite session.

## Machine

- GPU:
- Windows:
- Built with: native `cargo build --release` / other:
- Go df-hud during test: off / on (should be off)

## Game

- Presentation: exclusive fullscreen / borderless / windowed
- Monitor (`--list-monitors`):
- How they actually play (must match at least one A+B run):

## Console (paste)

```
pixel format ...
GL renderer=...
GL version=...
```

## Gates

| Gate | Result | One sentence |
|---|---|---|
| 3 WGL layered HWND (pixels, alpha, 1px inset) | pass / fail | |
| 3+4 over Dead Frontier in their usual mode | pass / fail | |
| 4 exclusive fullscreen (if tested) | pass / fail / not used | |
| Click-through after SwapBuffers | pass / fail | |
| 5 swap-interval 0 @ 1 Hz walking | pass / fail | |
| 5 swap-interval 1 @ 1 Hz (allowed to hitch) | pass / fail / hitch | |
| Premult / outlined text | pass / fail | |
| Hide/show + click-through after | pass / fail | |

Kill Phase 0? **yes / no**

Notes:
