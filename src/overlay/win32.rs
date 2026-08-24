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
use std::path::{Path, PathBuf};
use std::ptr;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

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
use windows_sys::Win32::System::Console::GetConsoleProcessList;
use windows_sys::Win32::System::LibraryLoader::GetModuleHandleW;
use windows_sys::Win32::UI::Controls::{
    TaskDialogIndirect, MARGINS, TASKDIALOGCONFIG, TASKDIALOG_BUTTON,
    TDF_ALLOW_DIALOG_CANCELLATION, TDF_SIZE_TO_CONTENT, TD_ERROR_ICON,
};
use windows_sys::Win32::UI::HiDpi::{
    GetDpiForMonitor, SetProcessDpiAwarenessContext, DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2,
    MDT_EFFECTIVE_DPI,
};
use windows_sys::Win32::UI::WindowsAndMessaging::{
    CreateWindowExW, DefWindowProcW, DestroyWindow, DispatchMessageW, GetWindowLongPtrW,
    LoadCursorW, MessageBoxW, MsgWaitForMultipleObjects, PeekMessageW, RegisterClassExW,
    SetLayeredWindowAttributes, SetWindowLongPtrW, SetWindowPos, ShowWindow, TranslateMessage,
    CS_HREDRAW, CS_OWNDC, CS_VREDRAW, GWL_EXSTYLE, HWND_TOPMOST, IDC_ARROW, LWA_ALPHA,
    MB_ICONERROR, MB_OK, MONITORINFOF_PRIMARY, MSG, PM_REMOVE, QS_ALLINPUT, SWP_FRAMECHANGED,
    SWP_NOACTIVATE, SWP_NOMOVE, SWP_NOSIZE, SWP_NOZORDER, SWP_SHOWWINDOW, SW_HIDE, SW_SHOWNA,
    WM_CLOSE, WM_DESTROY, WNDCLASSEXW, WS_EX_LAYERED, WS_EX_NOACTIVATE, WS_EX_TOOLWINDOW,
    WS_EX_TOPMOST, WS_EX_TRANSPARENT, WS_POPUP,
};

use crate::app;
use crate::cli::OverlayArgs;
use crate::config;
use crate::overlay::{self, gpu::Gpu, wgl::GlSurface};

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
    print_hud: bool,
}

impl From<OverlayArgs> for Args {
    fn from(args: OverlayArgs) -> Self {
        Self {
            monitor: args.monitor,
            duration: args.duration,
            list_monitors: args.list_monitors,
            requested: args.requested,
            config: args.config,
            print_hud: args.print_hud,
        }
    }
}

pub fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain(std::iter::once(0)).collect()
}

const ID_OPEN_LOG: i32 = 100;
const ID_CLOSE: i32 = 2;

/// Explorer and the Run key allocate a console that vanishes on exit. Keep a
/// dialog up so a bad config is readable. `cmd` / `cargo run` already have
/// stderr; skip the box there.
pub fn fatal_alert(err: &str) {
    let log = write_fatal_log(err);
    if shared_console() {
        return;
    }
    if let Some(path) = &log {
        if show_fatal_task_dialog(err, path) {
            return;
        }
    }
    let text = wide(&format!(
        "{err}\n\nThe overlay did not start. Fix the problem and launch df-hud again."
    ));
    let title = wide("df-hud");
    unsafe {
        MessageBoxW(
            ptr::null_mut(),
            text.as_ptr(),
            title.as_ptr(),
            MB_OK | MB_ICONERROR,
        );
    }
}

fn write_fatal_log(err: &str) -> Option<PathBuf> {
    let path = fatal_log_path()?;
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).ok()?;
    }
    std::fs::write(&path, format!("{err}\n")).ok()?;
    Some(path)
}

fn fatal_log_path() -> Option<PathBuf> {
    if let Ok(dir) = std::env::var("LOCALAPPDATA") {
        if !dir.is_empty() {
            return Some(PathBuf::from(dir).join("df-hud").join("df-hud.log"));
        }
    }
    Some(config::default_path().parent()?.join("df-hud.log"))
}

fn show_fatal_task_dialog(err: &str, log: &Path) -> bool {
    let title = wide("df-hud");
    let instruction = wide("The overlay did not start");
    let content = wide(&format!(
        "{err}\n\nFix the problem and launch df-hud again."
    ));
    let open_label = wide("Open log");
    let close_label = wide("Close");
    let buttons = [
        TASKDIALOG_BUTTON {
            nButtonID: ID_OPEN_LOG,
            pszButtonText: open_label.as_ptr(),
        },
        TASKDIALOG_BUTTON {
            nButtonID: ID_CLOSE,
            pszButtonText: close_label.as_ptr(),
        },
    ];
    let mut cfg = TASKDIALOGCONFIG::default();
    cfg.cbSize = size_of::<TASKDIALOGCONFIG>() as u32;
    cfg.pszWindowTitle = title.as_ptr();
    cfg.pszMainInstruction = instruction.as_ptr();
    cfg.pszContent = content.as_ptr();
    cfg.cButtons = buttons.len() as u32;
    cfg.pButtons = buttons.as_ptr();
    cfg.nDefaultButton = ID_CLOSE;
    cfg.dwFlags = TDF_ALLOW_DIALOG_CANCELLATION | TDF_SIZE_TO_CONTENT;
    cfg.Anonymous1.pszMainIcon = TD_ERROR_ICON;
    let mut button = 0i32;
    let hr = unsafe { TaskDialogIndirect(&cfg, &mut button, ptr::null_mut(), ptr::null_mut()) };
    if hr < 0 {
        return false;
    }
    if button == ID_OPEN_LOG {
        if let Err(open_err) = crate::app::autostart::open_file(log) {
            eprintln!("could not open log: {open_err}");
        }
    }
    true
}

fn shared_console() -> bool {
    let mut pids = [0u32; 8];
    let n = unsafe { GetConsoleProcessList(pids.as_mut_ptr(), pids.len() as u32) };
    n > 1
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
        eprintln!("unmapped (WGL context dropped)");
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

    fn wait(timeout_ms: u32, extra: Option<HANDLE>) -> u32 {
        unsafe {
            match extra {
                Some(h) => MsgWaitForMultipleObjects(1, &h, 0, timeout_ms, QS_ALLINPUT),
                None => MsgWaitForMultipleObjects(0, ptr::null(), 0, timeout_ms, QS_ALLINPUT),
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

fn create_gpu(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
    hwnd: HWND,
    buf_w: i32,
    buf_h: i32,
    font: &str,
) -> Result<(GlSurface, Gpu), Box<dyn Error>> {
    let started = Instant::now();
    let surface = GlSurface::create(instance, hwnd)?;
    let gl = unsafe { Glow::from_loader_function(|name| surface.load_proc(name)) };
    let gpu = Gpu::new(gl, buf_w, buf_h, font)?;
    eprintln!(
        "WGL context ready in {:.0}ms",
        started.elapsed().as_secs_f64() * 1000.0
    );
    Ok((surface, gpu))
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
    let (mut surface, mut gpu) = {
        let (s, g) = create_gpu(instance, win.hwnd, buf_w, buf_h, &watch.cfg.hud.font)?;
        (Some(s), Some(g))
    };
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
            hud: args.print_hud,
        },
    )?;
    let mut swaps = 0u32;
    let mut mapped = true;
    let mut needs_present = true;
    let mut monitor_request = want_name;
    let mut wait_failed = false;
    let mut runtime_cfg = handle.cfg.lock().unwrap().clone();

    let started = Instant::now();
    let mut next_tick = overlay::start_tick(started);

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
        if overlay::expired(started, args.duration) {
            eprintln!("clean shutdown after {swaps} swaps");
            return Ok(());
        }

        let now = Instant::now();
        if overlay::due(now, &mut next_tick) {
            needs_present = true;
            if let Some(cfg) = overlay::take_reload(&mut watch) {
                handle.note_config_watch(&watch, true);
                runtime_cfg = match gpu.as_mut() {
                    Some(gpu) => overlay::push_config(&handle, gpu, &cfg),
                    None => handle.replace_config(cfg),
                };
            } else {
                handle.note_config_watch(&watch, false);
            }
        }
        let vis_mon = handle.vis.state().monitor;
        let next_monitor_request =
            config::overlay_monitor(args.monitor.as_deref(), &runtime_cfg.hud.monitor, &vis_mon);
        if mapped && next_monitor_request != monitor_request {
            if let Some(surface) = &surface {
                let _ = surface.make_current();
            }
            gpu.take();
            surface.take();
            win.hide();
            mapped = false;
            needs_present = false;
        }
        monitor_request = next_monitor_request;
        let vis = handle.visible.load(Ordering::SeqCst);
        if mapped && !vis {
            if let Some(surface) = &surface {
                let _ = surface.make_current();
            }
            gpu.take();
            surface.take();
            win.hide();
            mapped = false;
            needs_present = false;
        } else if !mapped && vis {
            if let Ok(list) = list_monitors() {
                monitors = list;
            }
            if let Ok(m) = pick_monitor(
                &monitors,
                if monitor_request.is_empty() {
                    None
                } else {
                    Some(monitor_request.as_str())
                },
                &mut warned_monitors,
            ) {
                if m.name != current_monitor {
                    win.place(m)?;
                    buf_w = m.width - 2 * WINDOW_INSET;
                    buf_h = m.height - 2 * WINDOW_INSET;
                    current_monitor = m.name.clone();
                    eprintln!("hud: pinned to {}", current_monitor);
                }
            }
            match create_gpu(instance, win.hwnd, buf_w, buf_h, &runtime_cfg.hud.font) {
                Ok((s, g)) => {
                    surface = Some(s);
                    gpu = Some(g);
                    win.show()?;
                    mapped = true;
                    needs_present = true;
                }
                Err(err) => eprintln!("WGL: {err}"),
            }
        }
        if mapped && needs_present {
            if let (Some(surface), Some(gpu)) = (surface.as_ref(), gpu.as_mut()) {
                win.reassert_exstyle();
                surface.make_current()?;
                gpu.set_font(&runtime_cfg.hud.font);
                let built = overlay::scene(&handle, buf_w as f32, buf_h as f32);
                gpu.draw(buf_w, buf_h, buf_w, buf_h, &built)?;
                surface.swap()?;
                swaps += 1;
                needs_present = false;
            }
        }

        let timeout_ms = overlay::wait_ms(now, started, args.duration, next_tick);
        let result = OverlayWindow::wait(timeout_ms, Some(handle.wake.event_handle()));
        match overlay::classify_win32_wait(result, 1) {
            overlay::Win32Wait::Wake => {
                handle.wake.take();
                runtime_cfg = handle.cfg.lock().unwrap().clone();
                needs_present = true;
                wait_failed = false;
            }
            overlay::Win32Wait::Messages | overlay::Win32Wait::Timeout => {
                wait_failed = false;
            }
            overlay::Win32Wait::Failed(code) if !wait_failed => {
                eprintln!(
                    "overlay wait failed: result {code:#x}, {}",
                    last_err("wait")
                );
                wait_failed = true;
            }
            overlay::Win32Wait::Failed(_) => {}
        }
    }
}
