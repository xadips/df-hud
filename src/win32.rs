//! Layered click-through HWND. Owns DWM compositing and the 1s message wait.
//!
//! Intel DWM recipe (do not drop): `SetLayeredWindowAttributes(..., 255, LWA_ALPHA)`
//! **and** `DwmEnableBlurBehindWindow` with empty `CreateRectRgn(0,0,-1,-1)`.
//! Extend-frame alone was invisible on Iris Xe. 1px inset stays.
//!
//! Dummy WGL bootstrap uses [`DUMMY_CLASS`], not the overlay class. Sharing the
//! class made `DestroyWindow` on the dummy fire `WM_DESTROY` and end the process.

use std::collections::HashSet;
use std::error::Error;
use std::mem::size_of;
use std::path::PathBuf;
use std::ptr;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use chrono::Utc;

use glow::Context as Glow;
use windows_sys::core::BOOL;
use windows_sys::Win32::Foundation::{
    GetLastError, HANDLE, HWND, LPARAM, LRESULT, POINT, RECT, WPARAM,
};
use windows_sys::Win32::Graphics::Dwm::{
    DwmEnableBlurBehindWindow, DwmExtendFrameIntoClientArea, DWM_BB_BLURREGION, DWM_BB_ENABLE,
    DWM_BLURBEHIND,
};
use windows_sys::Win32::Graphics::Gdi::{
    CreateRectRgn, DeleteObject, EnumDisplayMonitors, GetMonitorInfoW, HDC, HMONITOR,
    MONITORINFOEXW,
};
use windows_sys::Win32::System::LibraryLoader::GetModuleHandleW;
use windows_sys::Win32::UI::Controls::MARGINS;
use windows_sys::Win32::UI::HiDpi::{
    GetDpiForMonitor, SetProcessDpiAwarenessContext, DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2,
    MDT_EFFECTIVE_DPI,
};
use windows_sys::Win32::UI::WindowsAndMessaging::{
    CreateWindowExW, DefWindowProcW, DestroyWindow, DispatchMessageW, GetWindowLongPtrW,
    LoadCursorW, MsgWaitForMultipleObjects, PeekMessageW, RegisterClassExW,
    SetLayeredWindowAttributes, SetWindowLongPtrW, SetWindowPos, ShowWindow, TranslateMessage,
    CS_HREDRAW, CS_OWNDC, CS_VREDRAW, GWL_EXSTYLE, HWND_TOPMOST, IDC_ARROW, LWA_ALPHA,
    MONITORINFOF_PRIMARY, MSG, PM_REMOVE, QS_ALLINPUT, SWP_FRAMECHANGED, SWP_NOACTIVATE,
    SWP_NOMOVE, SWP_NOSIZE, SWP_NOZORDER, SWP_SHOWWINDOW, SW_HIDE, SW_SHOWNA, WM_CLOSE, WM_DESTROY,
    WNDCLASSEXW, WS_EX_LAYERED, WS_EX_NOACTIVATE, WS_EX_TOOLWINDOW, WS_EX_TOPMOST,
    WS_EX_TRANSPARENT, WS_POPUP,
};

use crate::app;
use crate::config;
use crate::gpu::Gpu;
use crate::layout::Viewport;
use crate::present;
use crate::scene;
use crate::wgl::GlSurface;

pub const OVERLAY_CLASS: &str = "df-hud";
pub const DUMMY_CLASS: &str = "df-hud-wgl-dummy";
const WINDOW_TITLE: &str = "df-hud";
const WINDOW_INSET: i32 = 1;

static CLOSED: AtomicBool = AtomicBool::new(false);

pub struct Args {
    monitor: Option<String>,
    duration: Duration,
    list_monitors: bool,
    requested: bool,
    config: Option<PathBuf>,
    print_view: bool,
    print_hud: bool,
}

impl Args {
    pub fn parse() -> Result<Self, Box<dyn Error>> {
        let mut monitor = None;
        let mut duration = Duration::ZERO;
        let mut list_monitors = false;
        let mut requested = false;
        let mut config = None;
        let mut print_view = false;
        let mut print_hud = false;

        let mut args = std::env::args().skip(1);
        while let Some(arg) = args.next() {
            let mut next = |name: &str| -> Result<String, Box<dyn Error>> {
                args.next()
                    .ok_or_else(|| format!("{name} needs a value").into())
            };
            match arg.as_str() {
                "-h" | "--help" => {
                    print_help();
                    std::process::exit(0);
                }
                "--list-monitors" => {
                    requested = true;
                    list_monitors = true;
                }
                "--monitor" => {
                    requested = true;
                    monitor = Some(next("--monitor")?);
                }
                "--duration" => {
                    requested = true;
                    let secs: f32 = next("--duration")?.parse()?;
                    duration = if secs <= 0.0 {
                        Duration::ZERO
                    } else {
                        Duration::from_secs_f32(secs)
                    };
                }
                "-config" | "--config" => {
                    requested = true;
                    config = Some(PathBuf::from(next("-config")?));
                }
                "-print-view" | "--print-view" => print_view = true,
                "-print-hud" | "--print-hud" => print_hud = true,
                other => return Err(format!("unknown flag {other} (see --help)").into()),
            }
        }
        Ok(Self {
            monitor,
            duration,
            list_monitors,
            requested,
            config,
            print_view,
            print_hud,
        })
    }
}

fn print_help() {
    eprintln!(
        "\
df-hud — overlay (live derive)

  -once                 poll once, print the view, and exit
  -print-view [PATH]    fixture JSON (PATH) or live JSON each update
  -print-hud            print HUD text lines each update
  -dump-fields          with -once / -dump-challenges, print the player record (secrets withheld)
  -dump-challenges      fetch the challenge board once and print it
  -check-config         validate TOML and print the request budget
  -check-game           report whether the game client is detected
  -headless             run pollers without the overlay window
  -version              print the version and exit
  -config PATH          config file (also --config)
  --monitor NAME        Win32 device (\\\\.\\DISPLAY1). overrides hud.monitor
  --list-monitors       print monitors and exit
  --config PATH         TOML. default: %APPDATA%\\df-hud\\config.toml (missing = built-in defaults)
  --duration SECS       exit after SECS; 0 runs until Ctrl-C (default 0)
"
    );
}

pub fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain(std::iter::once(0)).collect()
}

pub fn last_err(op: &str) -> Box<dyn Error> {
    let code = unsafe { GetLastError() };
    format!("{op}: Win32 error {code}").into()
}

fn enable_per_monitor_v2() {
    let ok = unsafe { SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2) };
    if ok == 0 {
        eprintln!(
            "warning: SetProcessDpiAwarenessContext(PerMonitorV2) failed ({})",
            unsafe { GetLastError() }
        );
    } else {
        eprintln!("DPI: PerMonitorV2");
    }
}

#[derive(Clone, Debug)]
pub struct Monitor {
    pub name: String,
    pub left: i32,
    pub top: i32,
    pub width: i32,
    pub height: i32,
    pub dpi: u32,
    pub primary: bool,
}

impl Monitor {
    fn matches(&self, want: &str) -> bool {
        let want = want.trim();
        self.name.eq_ignore_ascii_case(want)
            || self
                .name
                .rsplit('\\')
                .next()
                .is_some_and(|tail| tail.eq_ignore_ascii_case(want))
    }
}

fn list_monitors() -> Result<Vec<Monitor>, Box<dyn Error>> {
    let mut out: Vec<Monitor> = Vec::new();
    let ok = unsafe {
        EnumDisplayMonitors(
            ptr::null_mut(),
            ptr::null(),
            Some(enum_monitor),
            &mut out as *mut _ as LPARAM,
        )
    };
    if ok == 0 {
        return Err(last_err("EnumDisplayMonitors"));
    }
    if out.is_empty() {
        return Err("EnumDisplayMonitors returned no monitors".into());
    }
    Ok(out)
}

unsafe extern "system" fn enum_monitor(
    handle: HMONITOR,
    _hdc: HDC,
    _rect: *mut RECT,
    lparam: LPARAM,
) -> BOOL {
    let out = unsafe { &mut *(lparam as *mut Vec<Monitor>) };
    let mut info: MONITORINFOEXW = unsafe { std::mem::zeroed() };
    info.monitorInfo.cbSize = size_of::<MONITORINFOEXW>() as u32;
    if unsafe { GetMonitorInfoW(handle, &mut info as *mut _ as *mut _) } == 0 {
        return 1;
    }
    let mut dpi_x = 0u32;
    let mut dpi_y = 0u32;
    unsafe { GetDpiForMonitor(handle, MDT_EFFECTIVE_DPI, &mut dpi_x, &mut dpi_y) };
    if dpi_x == 0 {
        dpi_x = 96;
    }
    let mon = info.monitorInfo.rcMonitor;
    out.push(Monitor {
        name: utf16_z(&info.szDevice),
        left: mon.left,
        top: mon.top,
        width: mon.right - mon.left,
        height: mon.bottom - mon.top,
        dpi: dpi_x,
        primary: info.monitorInfo.dwFlags & MONITORINFOF_PRIMARY != 0,
    });
    1
}

fn utf16_z(buf: &[u16]) -> String {
    let end = buf.iter().position(|&c| c == 0).unwrap_or(buf.len());
    String::from_utf16_lossy(&buf[..end])
}

fn pick_monitor<'a>(
    monitors: &'a [Monitor],
    want: Option<&str>,
    warned: &mut HashSet<String>,
) -> Result<&'a Monitor, Box<dyn Error>> {
    if let Some(want) = want.filter(|s| !s.is_empty()) {
        if let Some(found) = monitors.iter().find(|m| m.matches(want)) {
            return Ok(found);
        }
        if warned.insert(want.to_string()) {
            let names: Vec<&str> = monitors.iter().map(|m| m.name.as_str()).collect();
            eprintln!(
                "hud: no monitor named {want:?} (have {}); using primary",
                names.join(", ")
            );
        }
    }
    monitors
        .iter()
        .find(|m| m.primary)
        .or_else(|| monitors.first())
        .ok_or_else(|| "no monitors".into())
}

pub fn register_classes(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
) -> Result<(), Box<dyn Error>> {
    register_class(instance, OVERLAY_CLASS, Some(wndproc))?;
    register_class(instance, DUMMY_CLASS, Some(DefWindowProcW))?;
    Ok(())
}

fn register_class(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
    name: &str,
    wndproc: Option<unsafe extern "system" fn(HWND, u32, WPARAM, LPARAM) -> LRESULT>,
) -> Result<(), Box<dyn Error>> {
    let class = wide(name);
    let wc = WNDCLASSEXW {
        cbSize: size_of::<WNDCLASSEXW>() as u32,
        style: CS_OWNDC | CS_HREDRAW | CS_VREDRAW,
        lpfnWndProc: wndproc,
        cbClsExtra: 0,
        cbWndExtra: 0,
        hInstance: instance,
        hIcon: ptr::null_mut(),
        hCursor: unsafe { LoadCursorW(ptr::null_mut(), IDC_ARROW) },
        hbrBackground: ptr::null_mut(),
        lpszMenuName: ptr::null(),
        lpszClassName: class.as_ptr(),
        hIconSm: ptr::null_mut(),
    };
    if unsafe { RegisterClassExW(&wc) } == 0 {
        let err = unsafe { GetLastError() };
        // already registered in this process is fine
        if err != 1410 {
            return Err(last_err(&format!("RegisterClassExW {name}")));
        }
    }
    Ok(())
}

unsafe extern "system" fn wndproc(hwnd: HWND, msg: u32, wparam: WPARAM, lparam: LPARAM) -> LRESULT {
    match msg {
        WM_CLOSE | WM_DESTROY => {
            CLOSED.store(true, Ordering::SeqCst);
            0
        }
        _ => unsafe { DefWindowProcW(hwnd, msg, wparam, lparam) },
    }
}

struct OverlayWindow {
    hwnd: HWND,
}

impl OverlayWindow {
    fn create(
        instance: windows_sys::Win32::Foundation::HINSTANCE,
        monitor: &Monitor,
    ) -> Result<Self, Box<dyn Error>> {
        let inset = WINDOW_INSET;
        if 2 * inset >= monitor.width || 2 * inset >= monitor.height {
            return Err(format!(
                "inset {inset} is invalid for monitor {}x{}",
                monitor.width, monitor.height
            )
            .into());
        }
        let ex =
            WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE;
        let class = wide(OVERLAY_CLASS);
        let title = wide(WINDOW_TITLE);
        let w = monitor.width - 2 * inset;
        let h = monitor.height - 2 * inset;
        let hwnd = unsafe {
            CreateWindowExW(
                ex,
                class.as_ptr(),
                title.as_ptr(),
                WS_POPUP,
                0,
                0,
                w,
                h,
                ptr::null_mut(),
                ptr::null_mut(),
                instance,
                ptr::null(),
            )
        };
        if hwnd.is_null() {
            return Err(last_err("CreateWindowExW overlay"));
        }
        let win = Self { hwnd };
        win.place(monitor)?;
        Ok(win)
    }

    fn place(&self, monitor: &Monitor) -> Result<(), Box<dyn Error>> {
        let inset = WINDOW_INSET;
        let x = monitor.left + inset;
        let y = monitor.top + inset;
        let w = monitor.width - 2 * inset;
        let h = monitor.height - 2 * inset;
        let ok = unsafe { SetWindowPos(self.hwnd, HWND_TOPMOST, x, y, w, h, SWP_NOACTIVATE) };
        if ok == 0 {
            return Err(last_err("SetWindowPos"));
        }
        eprintln!(
            "window {w}x{h} at {x},{y}  inset {inset}  (1px gap at the monitor edge is expected)"
        );
        Ok(())
    }

    fn extend_dwm_frame(&self) -> Result<(), Box<dyn Error>> {
        // WS_EX_LAYERED stays invisible until SetLayeredWindowAttributes,
        // UpdateLayeredWindow, or DWM starts compositing the GL swapchain.
        // Constant alpha 255 multiplies per-pixel alpha (does not flatten it).
        let ok = unsafe { SetLayeredWindowAttributes(self.hwnd, 0, 255, LWA_ALPHA) };
        if ok == 0 {
            return Err(last_err("SetLayeredWindowAttributes"));
        }

        let margins = MARGINS {
            cxLeftWidth: -1,
            cxRightWidth: -1,
            cyTopHeight: -1,
            cyBottomHeight: -1,
        };
        let hr = unsafe { DwmExtendFrameIntoClientArea(self.hwnd, &margins) };
        if hr < 0 {
            return Err(format!("DwmExtendFrameIntoClientArea HRESULT 0x{hr:x}").into());
        }

        // Empty blur region is the DWM switch that composites WGL alpha.
        // DwmExtendFrame alone left this HWND fully invisible on Intel.
        let region = unsafe { CreateRectRgn(0, 0, -1, -1) };
        let bb = DWM_BLURBEHIND {
            dwFlags: DWM_BB_ENABLE | DWM_BB_BLURREGION,
            fEnable: 1,
            hRgnBlur: region,
            fTransitionOnMaximized: 0,
        };
        let hr = unsafe { DwmEnableBlurBehindWindow(self.hwnd, &bb) };
        if !region.is_null() {
            unsafe { DeleteObject(region) };
        }
        if hr < 0 {
            return Err(format!("DwmEnableBlurBehindWindow HRESULT 0x{hr:x}").into());
        }
        eprintln!("DWM: layered alpha 255 + extend-frame + blur-behind empty region");
        Ok(())
    }

    fn reassert_exstyle(&self) {
        let mut ex = unsafe { GetWindowLongPtrW(self.hwnd, GWL_EXSTYLE) };
        ex |= (WS_EX_LAYERED
            | WS_EX_TRANSPARENT
            | WS_EX_TOPMOST
            | WS_EX_TOOLWINDOW
            | WS_EX_NOACTIVATE) as isize;
        unsafe { SetWindowLongPtrW(self.hwnd, GWL_EXSTYLE, ex) };
        unsafe {
            SetWindowPos(
                self.hwnd,
                ptr::null_mut(),
                0,
                0,
                0,
                0,
                SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_FRAMECHANGED | SWP_NOACTIVATE,
            )
        };
    }

    fn hide(&self) {
        unsafe { ShowWindow(self.hwnd, SW_HIDE) };
        eprintln!("unmapped (WGL context kept)");
    }

    fn show(&self) -> Result<(), Box<dyn Error>> {
        unsafe { ShowWindow(self.hwnd, SW_SHOWNA) };
        self.reassert_exstyle();
        self.extend_dwm_frame()?;
        let ok = unsafe {
            SetWindowPos(
                self.hwnd,
                HWND_TOPMOST,
                0,
                0,
                0,
                0,
                SWP_SHOWWINDOW | SWP_NOACTIVATE | SWP_NOMOVE | SWP_NOSIZE,
            )
        };
        if ok == 0 {
            return Err(last_err("SetWindowPos show"));
        }
        eprintln!("shown (click-through must still hold)");
        Ok(())
    }

    fn pump() {
        let mut msg = MSG {
            hwnd: ptr::null_mut(),
            message: 0,
            wParam: 0,
            lParam: 0,
            time: 0,
            pt: POINT { x: 0, y: 0 },
        };
        while unsafe { PeekMessageW(&mut msg, ptr::null_mut(), 0, 0, PM_REMOVE) } != 0 {
            unsafe {
                TranslateMessage(&msg);
                DispatchMessageW(&msg);
            }
        }
    }

    fn wait(timeout_ms: u32, extra: Option<HANDLE>) {
        unsafe {
            match extra {
                Some(h) => {
                    MsgWaitForMultipleObjects(1, &h, 0, timeout_ms, QS_ALLINPUT);
                }
                None => {
                    MsgWaitForMultipleObjects(0, ptr::null(), 0, timeout_ms, QS_ALLINPUT);
                }
            }
        }
    }
}

impl Drop for OverlayWindow {
    fn drop(&mut self) {
        if !self.hwnd.is_null() {
            unsafe { DestroyWindow(self.hwnd) };
        }
    }
}

pub fn run(args: Args) -> Result<(), Box<dyn Error>> {
    CLOSED.store(false, Ordering::SeqCst);
    enable_per_monitor_v2();

    let instance = unsafe { GetModuleHandleW(ptr::null()) };
    if instance.is_null() {
        if args.requested {
            return Err("GetModuleHandleW failed".into());
        }
        eprintln!("no desktop (GetModuleHandleW failed); overlay skipped");
        return Ok(());
    }
    register_classes(instance)?;

    let mut monitors = match list_monitors() {
        Ok(monitors) => monitors,
        Err(err) => {
            if args.requested {
                return Err(err);
            }
            eprintln!("no desktop ({err}); overlay skipped");
            return Ok(());
        }
    };
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

    let mut watch = config::Watch::open(args.config.clone())?;
    let mut warned_monitors = HashSet::new();
    let want_name = config::overlay_monitor(args.monitor.as_deref(), &watch.cfg.hud.monitor, "");
    let monitor = pick_monitor(
        &monitors,
        if want_name.is_empty() {
            None
        } else {
            Some(want_name.as_str())
        },
        &mut warned_monitors,
    )?;
    let mut current_monitor = monitor.name.clone();
    let mut buf_w = monitor.width - 2 * WINDOW_INSET;
    let mut buf_h = monitor.height - 2 * WINDOW_INSET;

    let win = OverlayWindow::create(instance, monitor)?;
    let surface = GlSurface::create(instance, win.hwnd)?;
    let gl = unsafe { Glow::from_loader_function(|name| surface.load_proc(name)) };
    let mut gpu = Gpu::new(gl, buf_w, buf_h)?;
    win.extend_dwm_frame()?;
    win.reassert_exstyle();
    win.show()?;

    eprintln!(
        "hwnd layered+topmost+tool+noactivate+transparent  swap-interval=0  inset={WINDOW_INSET}"
    );
    eprintln!("done-when: live HUD (block / XP / challenges / map) at 1 Hz over the game; clicks still pass through");

    let handle = app::start_with(
        watch.cfg.clone(),
        app::PrintOpts {
            view: args.print_view,
            hud: args.print_hud,
        },
    )?;
    let mut swaps = 0u32;
    let mut mapped = true;
    let mut needs_present = true;

    let started = Instant::now();
    let mut next_tick = started + Duration::from_secs(1);

    loop {
        OverlayWindow::pump();
        if CLOSED.load(Ordering::SeqCst) {
            eprintln!("clean shutdown after {swaps} swaps (window closed)");
            return Ok(());
        }
        if handle.stopped() {
            eprintln!("clean shutdown after {swaps} swaps");
            return Ok(());
        }
        if args.duration > Duration::ZERO && started.elapsed() >= args.duration {
            eprintln!("clean shutdown after {swaps} swaps");
            return Ok(());
        }

        let now = Instant::now();
        if now >= next_tick {
            next_tick = now + Duration::from_secs(1);
            needs_present = true;
            if watch.poll() {
                handle.replace_config(watch.cfg.clone());
            }
        }
        let vis = handle.visible.load(Ordering::SeqCst);
        if mapped && !vis {
            win.hide();
            mapped = false;
            needs_present = false;
        } else if !mapped && vis {
            let vis_mon = handle.vis.state().monitor;
            let want_name =
                config::overlay_monitor(args.monitor.as_deref(), &watch.cfg.hud.monitor, &vis_mon);
            if let Ok(list) = list_monitors() {
                monitors = list;
            }
            if let Ok(m) = pick_monitor(
                &monitors,
                if want_name.is_empty() {
                    None
                } else {
                    Some(want_name.as_str())
                },
                &mut warned_monitors,
            ) {
                if m.name != current_monitor {
                    win.place(m)?;
                    let next_w = m.width - 2 * WINDOW_INSET;
                    let next_h = m.height - 2 * WINDOW_INSET;
                    if next_w != buf_w || next_h != buf_h {
                        buf_w = next_w;
                        buf_h = next_h;
                        gpu.resize(buf_w, buf_h);
                    }
                    current_monitor = m.name.clone();
                    eprintln!("hud: pinned to {}", current_monitor);
                }
            }
            win.show()?;
            mapped = true;
            needs_present = true;
        }
        if mapped && needs_present {
            win.reassert_exstyle();
            surface.make_current()?;
            let view =
                present::from_view(&handle.store.derive(Utc::now()), &watch.cfg, &handle.groups);
            let built = scene::build(
                &view,
                &watch.cfg,
                Viewport {
                    width: buf_w as f32,
                    height: buf_h as f32,
                    game_width: 0.0,
                    game_height: 0.0,
                },
            );
            gpu.draw(buf_w, buf_h, buf_w, buf_h, &built)?;
            surface.swap()?;
            swaps += 1;
        }

        let timeout_ms = poll_timeout_ms(now, started, args.duration, next_tick);
        OverlayWindow::wait(timeout_ms, Some(handle.wake.event_handle()));
        handle.wake.take();
        needs_present = true;
    }
}

fn poll_timeout_ms(
    now: Instant,
    started: Instant,
    duration: Duration,
    next_tick: Instant,
) -> u32 {
    let mut deadline: Option<Instant> = Some(next_tick);
    let consider = |acc: &mut Option<Instant>, at: Instant| {
        *acc = Some(match *acc {
            Some(prev) => prev.min(at),
            None => at,
        });
    };
    if duration > Duration::ZERO {
        consider(&mut deadline, started + duration);
    }
    match deadline {
        None => 1000,
        Some(at) => at
            .saturating_duration_since(now)
            .as_millis()
            .min(u32::MAX as u128) as u32,
    }
}
