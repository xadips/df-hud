//! Phase 3: one [`crate::gpu`] renderer on Wayland EGL and Win32 WGL.
//!
//! Keep the Go `df-hud` installed until Phase 8. Do not copy
//! `internal/hud/gtk` or `internal/hud/ebiten`.
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

#[cfg(any(target_os = "linux", windows))]
mod dummy;
#[cfg(target_os = "linux")]
mod egl;
mod font;
#[cfg(any(target_os = "linux", windows))]
mod gpu;
#[cfg(target_os = "linux")]
mod wayland;
#[cfg(windows)]
mod wgl;
#[cfg(windows)]
mod win32;

fn main() {
    if let Err(err) = run() {
        eprintln!("{err}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), Box<dyn std::error::Error>> {
    #[cfg(target_os = "linux")]
    {
        let args = wayland::Args::parse()?;
        println!("df-hud {}", env!("CARGO_PKG_VERSION"));
        wayland::run(args)
    }
    #[cfg(windows)]
    {
        let args = win32::Args::parse()?;
        println!("df-hud {}", env!("CARGO_PKG_VERSION"));
        win32::run(args)
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        println!("df-hud {}", env!("CARGO_PKG_VERSION"));
        Ok(())
    }
}
