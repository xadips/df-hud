#![cfg_attr(all(windows, not(test)), windows_subsystem = "windows")]
//! Product overlay: GLES 3.0 on `zwlr_layer_shell_v1` / WGL layered HWND.
//! Overlay stays `keyboard_interactivity = none` / `WS_EX_TRANSPARENT`.
//! Hidden HUD unmaps the layer surface / hides the HWND. HTTP overlay toggle
//! and the tray share `overlay_on` (Decide's Enabled).
//!
//! Surface contracts from the 2026-08-23 spikes (do not rediscover):
//!
//! - **Wayland remap** after `attach(null)`: Hyprland sets `m_configured = false`.
//!   Empty commit (re-apply anchor / exclusive / keyboard) → wait for `configure`
//!   → `ack_configure` in the same commit as the first `eglSwapBuffers`. Ack
//!   during `roundtrip` with no buffer maps an empty surface. Swap first →
//!   `layerSurface was not configured, but a buffer was attached`.
//! - **Win32 HWND** on Intel DWM: `DwmExtendFrameIntoClientArea` alone is
//!   invisible. `SetLayeredWindowAttributes(hwnd, 0, 255, LWA_ALPHA)` **and**
//!   `DwmEnableBlurBehindWindow` with an empty region `CreateRectRgn(0,0,-1,-1)`.
//!   Constant 255 multiplies per-pixel alpha; it does not flatten it. Keep the
//!   1px inset. Dummy WGL window uses a distinct class so `WM_DESTROY` on it
//!   does not end the process.

mod app;
mod cli;
mod config;
mod data;
mod format;
mod game;
mod model;
mod net;
#[cfg(any(target_os = "linux", windows))]
mod overlay;
mod wake;

fn main() {
    #[cfg(windows)]
    overlay::win32::init_stdio();
    if let Err(err) = run() {
        eprintln!("{err}");
        #[cfg(windows)]
        overlay::win32::fatal_alert(&err.to_string());
        std::process::exit(1);
    }
}

fn run() -> Result<(), Box<dyn std::error::Error>> {
    match cli::parse()? {
        cli::Launch::Overlay(args) => {
            println!("df-hud {}", env!("CARGO_PKG_VERSION"));
            #[cfg(target_os = "linux")]
            {
                overlay::wayland::run(args.into())
            }
            #[cfg(windows)]
            {
                overlay::win32::run(args.into())
            }
            #[cfg(not(any(target_os = "linux", windows)))]
            {
                let _ = args;
                Ok(())
            }
        }
        other => cli::run(other),
    }
}
