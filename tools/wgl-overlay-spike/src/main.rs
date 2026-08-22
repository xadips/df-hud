//! Throwaway Windows overlay spike for the df-hud rewrite.
//!
//! This is raw WGL on a layered HWND. It is **not** GLFW, Ebitengine, winit, or
//! `tools/windows-overlay-spike` (that crate only proves the current product's
//! engine). The rewrite talks to `opengl32.dll` itself.
//!
//! Native Windows only. Wine is not a DWM proof.

use std::error::Error;
use std::time::{Duration, Instant};

fn main() {
    if let Err(err) = run() {
        eprintln!("FAILED: {err}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), Box<dyn Error>> {
    let args = args::Args::parse()?;
    if args.help {
        args::print_help();
        return Ok(());
    }

    #[cfg(not(windows))]
    {
        let _ = args;
        eprintln!(
            "wgl-overlay-spike only runs on native Windows (not Wine, not this Linux box).\n\
             \n\
             On a Windows machine with Rust:\n\
               cd tools/wgl-overlay-spike\n\
               cargo build --release\n\
               .\\target\\release\\wgl-overlay-spike.exe --list-monitors\n\
             \n\
             From Linux (needs zig + cargo-zigbuild + the windows-gnu std):\n\
               cargo zigbuild --release --target x86_64-pc-windows-gnu --manifest-path tools/wgl-overlay-spike/Cargo.toml\n\
             \n\
             Read tools/wgl-overlay-spike/README.md before testing."
        );
        std::process::exit(2);
    }

    #[cfg(windows)]
    {
        windows_main(args)
    }
}

#[cfg(windows)]
mod font;
#[cfg(windows)]
mod gpu;
#[cfg(windows)]
mod wgl;
#[cfg(windows)]
mod win32;

#[cfg(windows)]
fn windows_main(args: args::Args) -> Result<(), Box<dyn Error>> {
    use windows_sys::Win32::System::LibraryLoader::GetModuleHandleW;

    use crate::gpu::Gpu;
    use crate::wgl::GlSurface;
    use crate::win32::{
        closed, enable_per_monitor_v2, list_monitors, pick_monitor, register_class, OverlayWindow,
    };

    enable_per_monitor_v2();
    let instance = unsafe { GetModuleHandleW(std::ptr::null()) };
    if instance.is_null() {
        return Err("GetModuleHandleW failed".into());
    }
    register_class(instance)?;

    let monitors = list_monitors()?;
    for monitor in &monitors {
        eprintln!(
            "monitor: {}  {}x{} at {},{}  dpi {}{}",
            monitor.name,
            monitor.width,
            monitor.height,
            monitor.left,
            monitor.top,
            monitor.dpi,
            if monitor.primary { "  primary" } else { "" }
        );
    }
    if args.list_monitors {
        return Ok(());
    }

    let monitor = pick_monitor(&monitors, args.monitor.as_deref())?;
    let inset = args.inset;
    let win = OverlayWindow::create(instance, monitor, inset, !args.no_clickthrough)?;
    let buf_w = monitor.width - 2 * inset;
    let buf_h = monitor.height - 2 * inset;

    let surface = GlSurface::create(instance, win.hwnd, args.swap_interval)?;
    let gpu = Gpu::new(surface, buf_w, buf_h)?;
    win.extend_dwm_frame()?;
    win.reassert_exstyle();
    win.show()?;

    eprintln!(
        "hwnd layered+topmost+tool+noactivate{}  swap-interval={}  {:.0} Hz  inset={inset}",
        if args.no_clickthrough {
            ""
        } else {
            "+transparent"
        },
        args.swap_interval,
        args.hz
    );
    eprintln!("done-when: tint sits over native-Windows Dead Frontier; mouse-look still hits the game");

    let mut swaps = 0u32;
    let mut mapped = true;
    present(&gpu, &win, monitor, &args, buf_w, buf_h, &mut swaps)?;

    let started = Instant::now();
    let period = Duration::from_secs_f32(1.0 / args.hz);
    let mut next_present = Instant::now() + period;
    let mut hide_at = args.hide_at.map(|d| started + d);
    let mut show_at: Option<Instant> = None;

    loop {
        OverlayWindow::pump();
        if closed() {
            eprintln!("clean shutdown after {swaps} swaps (window closed)");
            return Ok(());
        }
        if args.duration > Duration::ZERO && started.elapsed() >= args.duration {
            eprintln!("clean shutdown after {swaps} swaps");
            return Ok(());
        }

        let now = Instant::now();
        if let Some(at) = hide_at {
            if now >= at && mapped {
                win.hide();
                mapped = false;
                hide_at = None;
                show_at = Some(now + Duration::from_secs(5));
            }
        }
        if let Some(at) = show_at {
            if now >= at && !mapped {
                show_at = None;
                mapped = true;
                win.show()?;
                present(&gpu, &win, monitor, &args, buf_w, buf_h, &mut swaps)?;
                next_present = now + period;
            }
        }
        if mapped && now >= next_present {
            present(&gpu, &win, monitor, &args, buf_w, buf_h, &mut swaps)?;
            next_present = now + period;
        }

        let sleep = if mapped {
            next_present.saturating_duration_since(Instant::now())
        } else {
            show_at
                .unwrap_or(now + Duration::from_millis(50))
                .saturating_duration_since(Instant::now())
        };
        std::thread::sleep(sleep.min(Duration::from_millis(50)));
    }
}

#[cfg(windows)]
fn present(
    gpu: &gpu::Gpu,
    win: &win32::OverlayWindow,
    monitor: &win32::Monitor,
    args: &args::Args,
    buf_w: i32,
    buf_h: i32,
    swaps: &mut u32,
) -> Result<(), Box<dyn Error>> {
    win.reassert_exstyle();
    gpu.draw(&gpu::Frame {
        buf_w,
        buf_h,
        monitor_name: &monitor.name,
        dpi: monitor.dpi,
        inset: args.inset,
        swaps: *swaps,
        hz: args.hz,
        swap_interval: args.swap_interval,
        grid: args.grid,
        solid: args.solid,
        text_only: args.text_only,
    })?;
    *swaps += 1;
    Ok(())
}

mod args {
    use super::*;

    pub struct Args {
        pub help: bool,
        pub monitor: Option<String>,
        pub duration: Duration,
        pub swap_interval: i32,
        pub hz: f32,
        pub inset: i32,
        pub grid: bool,
        pub solid: bool,
        pub text_only: bool,
        pub hide_at: Option<Duration>,
        pub list_monitors: bool,
        pub no_clickthrough: bool,
    }

    impl Args {
        pub fn parse() -> Result<Self, Box<dyn Error>> {
            let mut help = false;
            let mut monitor = None;
            let mut duration = Duration::from_secs(30);
            let mut swap_interval = 0;
            let mut hz = 10.0;
            let mut inset = 1;
            let mut grid = false;
            let mut solid = false;
            let mut text_only = false;
            let mut hide_at = None;
            let mut list_monitors = false;
            let mut no_clickthrough = false;

            let mut args = std::env::args().skip(1);
            while let Some(arg) = args.next() {
                let mut next = |name: &str| -> Result<String, Box<dyn Error>> {
                    args.next()
                        .ok_or_else(|| format!("{name} needs a value").into())
                };
                match arg.as_str() {
                    "-h" | "--help" => help = true,
                    "--list-monitors" => list_monitors = true,
                    "--grid" => grid = true,
                    "--solid" => solid = true,
                    "--text-only" => text_only = true,
                    "--no-clickthrough" => no_clickthrough = true,
                    "--monitor" => monitor = Some(next("--monitor")?),
                    "--duration" => {
                        let secs: f32 = next("--duration")?.parse()?;
                        duration = if secs <= 0.0 {
                            Duration::ZERO
                        } else {
                            Duration::from_secs_f32(secs)
                        };
                    }
                    "--swap-interval" => swap_interval = next("--swap-interval")?.parse()?,
                    "--hz" => hz = next("--hz")?.parse()?,
                    "--inset" => inset = next("--inset")?.parse()?,
                    "--hide-at" => {
                        hide_at = Some(Duration::from_secs_f32(next("--hide-at")?.parse()?));
                    }
                    other => return Err(format!("unknown flag {other} (see --help)").into()),
                }
            }
            if hz <= 0.0 {
                return Err("--hz must be positive".into());
            }
            Ok(Self {
                help,
                monitor,
                duration,
                swap_interval,
                hz,
                inset,
                grid,
                solid,
                text_only,
                hide_at,
                list_monitors,
                no_clickthrough,
            })
        }
    }

    pub fn print_help() {
        eprintln!(
            "\
wgl-overlay-spike — raw WGL on a layered HWND, over native-Windows Dead Frontier

  --monitor NAME        Win32 device (\\\\.\\DISPLAY1). default: primary
  --list-monitors       print monitors and exit
  --duration SECS       exit after SECS; 0 runs until the window is closed (default 30)
  --hz N                present rate (default 10; use 1 for the hitch test)
  --swap-interval 0|1   wglSwapIntervalEXT (default 0)
  --inset PX            shrink every edge so DWM keeps alpha (default 1; 0 is the hole test)
  --solid               opaque magenta fullscreen (proves pixels land)
  --text-only           outlined white text + alpha ramps (premult / fringe)
  --grid                1px lines every 32 px
  --hide-at SECS        hide at T, show 5s later (context must survive)
  --no-clickthrough     omit WS_EX_TRANSPARENT (negative test)

Read tools/wgl-overlay-spike/README.md for the test protocol and report template.
"
        );
    }
}
