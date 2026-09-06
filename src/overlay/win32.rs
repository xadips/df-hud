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
use std::fs::OpenOptions;
use std::io::Write;
use std::mem::size_of;
use std::os::windows::io::AsRawHandle;
use std::path::{Path, PathBuf};
use std::ptr;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use glow::Context as Glow;
use windows_sys::Win32::Foundation::{
    GENERIC_WRITE, GetLastError, HANDLE, HWND, INVALID_HANDLE_VALUE, LPARAM, LRESULT, POINT, RECT,
    WPARAM,
};
use windows_sys::Win32::Graphics::Dwm::{
    DWM_BB_BLURREGION, DWM_BB_ENABLE, DWM_BLURBEHIND, DwmEnableBlurBehindWindow,
    DwmExtendFrameIntoClientArea,
};
use windows_sys::Win32::Graphics::Gdi::{
    CreateRectRgn, DeleteObject, EnumDisplayMonitors, GetMonitorInfoW, HDC, HMONITOR,
    MONITORINFOEXW,
};
use windows_sys::Win32::Storage::FileSystem::{
    CreateFileW, FILE_SHARE_READ, FILE_SHARE_WRITE, OPEN_EXISTING,
};
use windows_sys::Win32::System::Console::{
    ATTACH_PARENT_PROCESS, AttachConsole, GetConsoleProcessList, STD_ERROR_HANDLE,
    STD_OUTPUT_HANDLE, SetStdHandle,
};
use windows_sys::Win32::System::LibraryLoader::GetModuleHandleW;
use windows_sys::Win32::UI::Controls::{
    MARGINS, TASKDIALOG_BUTTON, TASKDIALOGCONFIG, TD_ERROR_ICON, TDF_ALLOW_DIALOG_CANCELLATION,
    TDF_SIZE_TO_CONTENT, TaskDialogIndirect,
};
use windows_sys::Win32::UI::HiDpi::{
    DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, GetDpiForMonitor, MDT_EFFECTIVE_DPI,
    SetProcessDpiAwarenessContext,
};
use windows_sys::Win32::UI::WindowsAndMessaging::{
    CS_HREDRAW, CS_OWNDC, CS_VREDRAW, CreateWindowExW, DefWindowProcW, DestroyWindow,
    DispatchMessageW, GWL_EXSTYLE, GetWindowLongPtrW, HWND_TOPMOST, IDC_ARROW, LWA_ALPHA,
    LoadCursorW, MB_ICONERROR, MB_OK, MONITORINFOF_PRIMARY, MSG, MessageBoxW,
    MsgWaitForMultipleObjects, PM_REMOVE, PeekMessageW, QS_ALLINPUT, RegisterClassExW, SW_HIDE,
    SW_SHOWNA, SWP_FRAMECHANGED, SWP_NOACTIVATE, SWP_NOMOVE, SWP_NOSIZE, SWP_NOZORDER,
    SWP_SHOWWINDOW, SetLayeredWindowAttributes, SetWindowLongPtrW, SetWindowPos, ShowWindow,
    TranslateMessage, WM_CLOSE, WM_DESTROY, WM_DISPLAYCHANGE, WM_DPICHANGED, WNDCLASSEXW,
    WS_EX_LAYERED, WS_EX_NOACTIVATE, WS_EX_TOOLWINDOW, WS_EX_TOPMOST, WS_EX_TRANSPARENT, WS_POPUP,
};
use windows_sys::core::BOOL;

use crate::app;
use crate::cli::OverlayArgs;
use crate::config::{self, Config};
use crate::overlay::{
    self,
    gpu::{Frame, Gpu},
    wgl::GlSurface,
};

pub const OVERLAY_CLASS: &str = "df-hud";
pub const DUMMY_CLASS: &str = "df-hud-wgl-dummy";
const WINDOW_TITLE: &str = "df-hud";
const WINDOW_INSET: i32 = 1;

/// The ex-style the overlay must keep. `WS_EX_TRANSPARENT` (with
/// `WS_EX_LAYERED`) is the click-through; the spike's gate B recorded that
/// losing it after `SwapBuffers` would be a kill and re-asserting it the
/// accepted fix, so every present checks these bits are still set.
const WANTED_EXSTYLE: u32 =
    WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE;

static CLOSED: AtomicBool = AtomicBool::new(false);
/// Set by `wndproc` on `WM_DISPLAYCHANGE` / `WM_DPICHANGED`; `follow` takes
/// it and re-enumerates the monitors, which otherwise stay cached.
static DISPLAY_CHANGED: AtomicBool = AtomicBool::new(false);

/// Whether the ex-style Windows reports is missing any wanted bit.
fn needs_reassert(current: u32, wanted: u32) -> bool {
    current & wanted != wanted
}

/// Whether `follow` should call `EnumDisplayMonitors` this tick: the display
/// set or DPI changed, the surface asked for one (config change, unmap), or
/// the monitor it wants is not the one it picked from last time.
fn rescan_due(display_changed: bool, surface_dirty: bool, want: &str, previous: &str) -> bool {
    display_changed || surface_dirty || want != previous
}

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

/// GUI subsystem: Explorer does not get a console. Attach the parent `cmd`
/// when there is one; otherwise send stderr to `%LOCALAPPDATA%\df-hud\df-hud.log`.
pub fn init_stdio() {
    // SAFETY: takes a process id constant; no pointers.
    if unsafe { AttachConsole(ATTACH_PARENT_PROCESS) } != 0 && bind_conout() {
        return;
    }
    bind_log_file();
}

fn bind_conout() -> bool {
    let name = wide("CONOUT$");
    // SAFETY: `name` is NUL-terminated and outlives the call; null security
    // attributes and template are the defaults.
    let h = unsafe {
        CreateFileW(
            name.as_ptr(),
            GENERIC_WRITE,
            FILE_SHARE_READ | FILE_SHARE_WRITE,
            ptr::null(),
            OPEN_EXISTING,
            0,
            ptr::null_mut(),
        )
    };
    if h.is_null() || h == INVALID_HANDLE_VALUE {
        return false;
    }
    // SAFETY: `h` is a valid handle we own and never close, so std's stdout/
    // stderr wrappers can use it for the rest of the process.
    unsafe {
        SetStdHandle(STD_OUTPUT_HANDLE, h);
        SetStdHandle(STD_ERROR_HANDLE, h);
    }
    true
}

fn bind_log_file() {
    let Some(path) = fatal_log_path() else {
        return;
    };
    if let Some(dir) = path.parent() {
        let _ = std::fs::create_dir_all(dir);
    }
    let Ok(file) = OpenOptions::new().create(true).append(true).open(&path) else {
        return;
    };
    let h = file.as_raw_handle();
    // SAFETY: `h` stays open for the rest of the process because `file` is
    // forgotten right after, so the std handles never point at a closed one.
    unsafe {
        SetStdHandle(STD_OUTPUT_HANDLE, h);
        SetStdHandle(STD_ERROR_HANDLE, h);
    }
    std::mem::forget(file);
    let _ = writeln!(
        std::io::stderr(),
        "\n--- df-hud {} ---",
        env!("CARGO_PKG_VERSION")
    );
}

/// No console window on Explorer / Run. Keep a dialog up so a bad config is
/// readable. `cmd` after AttachConsole already has stderr; skip the box there.
pub fn fatal_alert(err: &str, headline: &str) {
    let log = write_fatal_log(err);
    if shared_console() {
        return;
    }
    if let Some(path) = &log
        && show_fatal_task_dialog(err, path, headline)
    {
        return;
    }
    let text = wide(&format!(
        "{err}\n\n{headline}. Fix the problem and launch df-hud again."
    ));
    let title = wide("df-hud");
    // SAFETY: `text`/`title` are NUL-terminated u16 buffers that outlive the
    // (blocking) call; a null owner HWND is allowed.
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
    let mut f = OpenOptions::new()
        .create(true)
        .append(true)
        .open(&path)
        .ok()?;
    writeln!(f, "fatal: {err}").ok()?;
    Some(path)
}

fn fatal_log_path() -> Option<PathBuf> {
    Some(config::default_log_path())
}

fn show_fatal_task_dialog(err: &str, log: &Path, headline: &str) -> bool {
    let title = wide("df-hud");
    let instruction = wide(headline);
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
    let mut cfg = TASKDIALOGCONFIG {
        cbSize: size_of::<TASKDIALOGCONFIG>() as u32,
        pszWindowTitle: title.as_ptr(),
        pszMainInstruction: instruction.as_ptr(),
        pszContent: content.as_ptr(),
        cButtons: buttons.len() as u32,
        pButtons: buttons.as_ptr(),
        nDefaultButton: ID_CLOSE,
        dwFlags: TDF_ALLOW_DIALOG_CANCELLATION | TDF_SIZE_TO_CONTENT,
        ..TASKDIALOGCONFIG::default()
    };
    cfg.Anonymous1.pszMainIcon = TD_ERROR_ICON;
    let mut button = 0i32;
    // SAFETY: every pointer in `cfg` (`title`, `instruction`, `content`,
    // `buttons` and their labels) is a local that outlives this blocking
    // call; `button` is a live out-param; radio/verification outs are optional.
    let hr = unsafe { TaskDialogIndirect(&cfg, &mut button, ptr::null_mut(), ptr::null_mut()) };
    if hr < 0 {
        return false;
    }
    if button == ID_OPEN_LOG
        && let Err(open_err) = crate::app::autostart::open_file(log)
    {
        error!("could not open log: {open_err}");
    }
    true
}

fn shared_console() -> bool {
    let mut pids = [0u32; 8];
    // SAFETY: `pids` has room for the count passed (only the total matters here).
    let n = unsafe { GetConsoleProcessList(pids.as_mut_ptr(), pids.len() as u32) };
    n > 1
}

pub fn last_err(op: &str) -> Box<dyn Error> {
    // SAFETY: reads this thread's last-error slot; no arguments.
    let code = unsafe { GetLastError() };
    format!("{op}: Win32 error {code}").into()
}

fn enable_per_monitor_v2() {
    // SAFETY: takes a context constant; no pointers.
    let ok = unsafe { SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2) };
    if ok == 0 {
        // SAFETY: reads this thread's last-error slot; no arguments.
        let code = unsafe { GetLastError() };
        warn!("warning: SetProcessDpiAwarenessContext(PerMonitorV2) failed ({code})");
    } else {
        debug!("DPI: PerMonitorV2");
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

    fn same_surface(&self, other: &Self) -> bool {
        self.name.eq_ignore_ascii_case(&other.name)
            && self.left == other.left
            && self.top == other.top
            && self.width == other.width
            && self.height == other.height
    }
}

pub(crate) fn primary_panel() -> Option<(i32, i32)> {
    let monitors = list_monitors().ok()?;
    monitors
        .iter()
        .find(|m| m.primary)
        .or_else(|| monitors.first())
        .map(|m| (m.width, m.height))
}

fn list_monitors() -> Result<Vec<Monitor>, Box<dyn Error>> {
    let mut out: Vec<Monitor> = Vec::new();
    // SAFETY: `enum_monitor` runs synchronously on this thread before this
    // returns and is the only reader of `lparam`, which it treats as the
    // `&mut Vec<Monitor>` it is.
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
    // SAFETY: `lparam` is the `&mut Vec<Monitor>` `list_monitors` passed to
    // EnumDisplayMonitors, which calls back synchronously on that thread, so
    // the borrow is live and unaliased for the duration of this call.
    let out = unsafe { &mut *(lparam as *mut Vec<Monitor>) };
    let mut info = MONITORINFOEXW::default();
    info.monitorInfo.cbSize = size_of::<MONITORINFOEXW>() as u32;
    // SAFETY: `handle` is the monitor being enumerated; `cbSize` tells
    // GetMonitorInfoW that `info` is the extended struct, so the MONITORINFO*
    // cast is the documented way to receive `szDevice`.
    if unsafe { GetMonitorInfoW(handle, &mut info as *mut _ as *mut _) } == 0 {
        return 1;
    }
    let mut dpi_x = 0u32;
    let mut dpi_y = 0u32;
    // SAFETY: live out-params for the two DPI values.
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
            warn!(
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
    // SAFETY: a null module with IDC_ARROW loads the shared system cursor.
    let cursor = unsafe { LoadCursorW(ptr::null_mut(), IDC_ARROW) };
    let wc = WNDCLASSEXW {
        cbSize: size_of::<WNDCLASSEXW>() as u32,
        style: CS_OWNDC | CS_HREDRAW | CS_VREDRAW,
        lpfnWndProc: wndproc,
        cbClsExtra: 0,
        cbWndExtra: 0,
        hInstance: instance,
        hIcon: ptr::null_mut(),
        hCursor: cursor,
        hbrBackground: ptr::null_mut(),
        lpszMenuName: ptr::null(),
        lpszClassName: class.as_ptr(),
        hIconSm: ptr::null_mut(),
    };
    // SAFETY: `wc` is fully initialised and `class` (its name) outlives the
    // call, which copies it; `wndproc` has the WNDPROC signature.
    if unsafe { RegisterClassExW(&wc) } == 0 {
        // SAFETY: reads this thread's last-error slot; no arguments.
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
        WM_DISPLAYCHANGE | WM_DPICHANGED => {
            DISPLAY_CHANGED.store(true, Ordering::SeqCst);
            // SAFETY: forwarding, unchanged, the message the system just
            // delivered for `hwnd` on this thread.
            unsafe { DefWindowProcW(hwnd, msg, wparam, lparam) }
        }
        // SAFETY: forwarding, unchanged, the message the system just
        // delivered for `hwnd` on this thread.
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
        let ex = WANTED_EXSTYLE;
        let class = wide(OVERLAY_CLASS);
        let title = wide(WINDOW_TITLE);
        let w = monitor.width - 2 * inset;
        let h = monitor.height - 2 * inset;
        // SAFETY: `class`/`title` are NUL-terminated u16 buffers that outlive
        // the call; OVERLAY_CLASS was registered for `instance` by `register_classes`.
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
        // SAFETY: `self.hwnd` is this window's live handle (destroyed only in `Drop`).
        let ok = unsafe { SetWindowPos(self.hwnd, HWND_TOPMOST, x, y, w, h, SWP_NOACTIVATE) };
        if ok == 0 {
            return Err(last_err("SetWindowPos"));
        }
        debug!(
            "window {w}x{h} at {x},{y}  inset {inset}  (1px gap at the monitor edge is expected)"
        );
        Ok(())
    }

    fn extend_dwm_frame(&self) -> Result<(), Box<dyn Error>> {
        // WS_EX_LAYERED stays invisible until SetLayeredWindowAttributes,
        // UpdateLayeredWindow, or DWM starts compositing the GL swapchain.
        // Constant alpha 255 multiplies per-pixel alpha (does not flatten it).
        // SAFETY: `self.hwnd` is this window's live handle; the rest are flags.
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
        // SAFETY: `margins` is a live local read during the call only.
        let hr = unsafe { DwmExtendFrameIntoClientArea(self.hwnd, &margins) };
        if hr < 0 {
            return Err(format!("DwmExtendFrameIntoClientArea HRESULT 0x{hr:x}").into());
        }

        // Empty blur region is the DWM switch that composites WGL alpha.
        // DwmExtendFrame alone left this HWND fully invisible on Intel.
        // SAFETY: integer arguments only; the region is owned here and deleted
        // below.
        let region = unsafe { CreateRectRgn(0, 0, -1, -1) };
        let bb = DWM_BLURBEHIND {
            dwFlags: DWM_BB_ENABLE | DWM_BB_BLURREGION,
            fEnable: 1,
            hRgnBlur: region,
            fTransitionOnMaximized: 0,
        };
        // SAFETY: `bb` is a live local; DWM copies the region during the call,
        // so deleting `region` afterwards (once, when non-null) is correct.
        let hr = unsafe {
            let hr = DwmEnableBlurBehindWindow(self.hwnd, &bb);
            if !region.is_null() {
                DeleteObject(region);
            }
            hr
        };
        if hr < 0 {
            return Err(format!("DwmEnableBlurBehindWindow HRESULT 0x{hr:x}").into());
        }
        debug!("DWM: layered alpha 255 + extend-frame + blur-behind empty region");
        Ok(())
    }

    /// Puts [`WANTED_EXSTYLE`] back if anything cleared a bit. Runs every
    /// present, but an intact style costs one `GetWindowLongPtrW`; the
    /// `SetWindowLongPtrW` + `SWP_FRAMECHANGED` that make a style change
    /// take effect only run when a bit is actually missing.
    fn reassert_exstyle(&self) {
        // SAFETY: `self.hwnd` is this window's live handle and this runs on
        // the thread that created it; GWL_EXSTYLE is an index, not a pointer.
        let current = unsafe { GetWindowLongPtrW(self.hwnd, GWL_EXSTYLE) };
        if !needs_reassert(current as u32, WANTED_EXSTYLE) {
            return;
        }
        debug!(
            "hud: ex-style lost {:#x}; re-asserting",
            WANTED_EXSTYLE & !(current as u32)
        );
        // SAFETY: `self.hwnd` is this window's live handle and these run on
        // the thread that created it; the rest are style bits and flags.
        unsafe {
            SetWindowLongPtrW(self.hwnd, GWL_EXSTYLE, current | WANTED_EXSTYLE as isize);
            SetWindowPos(
                self.hwnd,
                ptr::null_mut(),
                0,
                0,
                0,
                0,
                SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_FRAMECHANGED | SWP_NOACTIVATE,
            );
        }
    }

    fn hide(&self) {
        // SAFETY: `self.hwnd` is this window's live handle.
        unsafe { ShowWindow(self.hwnd, SW_HIDE) };
        info!("unmapped (WGL context dropped)");
    }

    fn show(&self) -> Result<(), Box<dyn Error>> {
        // SAFETY: `self.hwnd` is this window's live handle.
        unsafe { ShowWindow(self.hwnd, SW_SHOWNA) };
        self.reassert_exstyle();
        self.extend_dwm_frame()?;
        // SAFETY: `self.hwnd` is this window's live handle; the rest are flags.
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
        info!("shown (click-through must still hold)");
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
        // SAFETY: `msg` is a live local this thread owns; PeekMessageW fills it
        // and the two calls read it, all on the window's own (this) thread.
        unsafe {
            while PeekMessageW(&mut msg, ptr::null_mut(), 0, 0, PM_REMOVE) != 0 {
                TranslateMessage(&msg);
                DispatchMessageW(&msg);
            }
        }
    }

    fn wait(timeout_ms: u32, extra: Option<HANDLE>) -> u32 {
        // SAFETY: `&h` points at one live HANDLE for a count of 1 (the wake
        // event, owned by `Handle` for longer than the loop); null with a count
        // of 0 waits on messages alone.
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
            // SAFETY: destroying the window `create` made, once, on its thread;
            // `Surface` drops the `Gpu` and `GlSurface` that used it first.
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
    // SAFETY: `create` left the new context current on this thread, so
    // `load_proc` resolves each GL entry point for it (or null, which glow
    // tolerates); the context outlives the `Gpu` (see `Surface` field order).
    let gl = unsafe { Glow::from_loader_function(|name| surface.load_proc(name)) };
    let gpu = Gpu::new(gl, buf_w, buf_h, font)?;
    debug!(
        "WGL context ready in {:.0}ms",
        started.elapsed().as_secs_f64() * 1000.0
    );
    Ok((surface, gpu))
}

fn monitor_want(name: &str) -> Option<&str> {
    (!name.is_empty()).then_some(name)
}

/// The HWND, its WGL context, and which monitor it is parked on. The
/// `overlay::Hooks` impl is what `overlay::step` drives once per tick.
///
/// Field order is drop order: `gpu` (GL handles) before `gl` (the context
/// they live in) before `win` (the HWND the context was created on).
struct Surface {
    instance: windows_sys::Win32::Foundation::HINSTANCE,
    handle: Arc<app::Handle>,
    cli_monitor: Option<String>,
    gpu: Option<Gpu>,
    gl: Option<GlSurface>,
    win: OverlayWindow,
    /// Cached `EnumDisplayMonitors` result; see [`rescan_due`] for when it
    /// is refreshed.
    monitors: Vec<Monitor>,
    /// Asks `follow` to re-enumerate on its next tick.
    rescan_monitors: bool,
    current: Monitor,
    /// Monitor `follow` picked this tick; `show` places the window on it.
    picked: Option<Monitor>,
    monitor_request: String,
    warned_monitors: HashSet<String>,
    buf_w: i32,
    buf_h: i32,
    mapped: bool,
    swaps: u32,
}

impl Surface {
    fn hide_window(&mut self) {
        if let Some(gl) = &self.gl {
            let _ = gl.make_current();
        }
        self.gpu.take();
        self.gl.take();
        self.win.hide();
        self.mapped = false;
        // The next map places the window again; do it from a fresh list.
        self.rescan_monitors = true;
    }
}

impl overlay::Hooks for Surface {
    fn config_changed(&mut self, cfg: &Config) {
        if let Some(gpu) = self.gpu.as_mut() {
            gpu.set_font(&cfg.hud.font);
        }
        self.rescan_monitors = true;
    }

    fn follow(&mut self, cfg: &Config) {
        let vis_mon = self.handle.vis.state().monitor;
        let next = config::overlay_monitor(self.cli_monitor.as_deref(), &cfg.hud.monitor, &vis_mon);
        if rescan_due(
            DISPLAY_CHANGED.swap(false, Ordering::SeqCst),
            self.rescan_monitors,
            &next,
            &self.monitor_request,
        ) {
            self.rescan_monitors = false;
            if let Ok(list) = list_monitors() {
                self.monitors = list;
            }
        }
        self.picked = pick_monitor(
            &self.monitors,
            monitor_want(&next),
            &mut self.warned_monitors,
        )
        .ok()
        .cloned();
        let moved = self
            .picked
            .as_ref()
            .is_some_and(|m| !self.current.same_surface(m));
        if self.mapped && (next != self.monitor_request || moved) {
            self.hide_window();
        }
        self.monitor_request = next;
    }

    fn show(&mut self, cfg: &Config) -> Result<(), Box<dyn Error>> {
        if self.mapped {
            return Ok(());
        }
        if let Some(m) = &self.picked
            && !self.current.same_surface(m)
        {
            self.win.place(m)?;
            self.buf_w = m.width - 2 * WINDOW_INSET;
            self.buf_h = m.height - 2 * WINDOW_INSET;
            self.current = m.clone();
            info!("hud: pinned to {}", self.current.name);
        }
        match create_gpu(
            self.instance,
            self.win.hwnd,
            self.buf_w,
            self.buf_h,
            &cfg.hud.font,
        ) {
            Ok((gl, gpu)) => {
                self.gl = Some(gl);
                self.gpu = Some(gpu);
                self.win.show()?;
                self.mapped = true;
            }
            Err(err) => error!("WGL: {err}"),
        }
        Ok(())
    }

    fn hide(&mut self) {
        if self.mapped {
            self.hide_window();
        }
    }

    fn present(&mut self, cfg: &Config) -> Result<(), Box<dyn Error>> {
        if !self.mapped {
            return Ok(());
        }
        let (Some(gl), Some(gpu)) = (self.gl.as_ref(), self.gpu.as_mut()) else {
            return Ok(());
        };
        self.win.reassert_exstyle();
        gl.make_current()?;
        let built = overlay::scene(&self.handle, cfg, self.buf_w as f32, self.buf_h as f32);
        if gpu.draw(Frame::square(self.buf_w, self.buf_h), built, false)? {
            gl.swap()?;
            self.swaps += 1;
        }
        Ok(())
    }
}

pub fn run(args: Args) -> Result<(), Box<dyn Error>> {
    CLOSED.store(false, Ordering::SeqCst);
    DISPLAY_CHANGED.store(false, Ordering::SeqCst);
    enable_per_monitor_v2();

    // SAFETY: a null name returns the handle of this executable's own module.
    let instance = unsafe { GetModuleHandleW(ptr::null()) };
    if instance.is_null() {
        if args.requested {
            return Err("GetModuleHandleW failed".into());
        }
        error!("no desktop (GetModuleHandleW failed); overlay skipped");
        return Ok(());
    }
    register_classes(instance)?;

    let monitors = match list_monitors() {
        Ok(monitors) => monitors,
        Err(err) => {
            if args.requested {
                return Err(err);
            }
            error!("no desktop ({err}); overlay skipped");
            return Ok(());
        }
    };
    for monitor in &monitors {
        debug!(
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

    let seed = monitors
        .iter()
        .find(|m| m.primary)
        .or_else(|| monitors.first())
        .map(|m| (m.width, m.height));
    let watch = config::Watch::open_with_reference(args.config.clone(), seed)?;
    let mut warned_monitors = HashSet::new();
    let want_name = config::overlay_monitor(args.monitor.as_deref(), &watch.cfg.hud.monitor, "");
    let monitor = pick_monitor(&monitors, monitor_want(&want_name), &mut warned_monitors)?.clone();
    let buf_w = monitor.width - 2 * WINDOW_INSET;
    let buf_h = monitor.height - 2 * WINDOW_INSET;

    let win = OverlayWindow::create(instance, &monitor)?;
    let (gl, gpu) = create_gpu(instance, win.hwnd, buf_w, buf_h, &watch.cfg.hud.font)?;
    win.extend_dwm_frame()?;
    win.reassert_exstyle();
    win.show()?;

    debug!(
        "hwnd layered+topmost+tool+noactivate+transparent  swap-interval=0  inset={WINDOW_INSET}"
    );
    debug!(
        "done-when: live HUD (block / XP / challenges / map) at 1 Hz over the game; clicks still pass through"
    );

    let handle = app::start_with(
        watch.cfg.clone(),
        app::PrintOpts {
            hud: args.print_hud,
        },
    )?;
    let _stop_on_exit = overlay::StopOnExit(&handle);
    let mut surface = Surface {
        instance,
        handle: handle.clone(),
        cli_monitor: args.monitor.clone(),
        gpu: Some(gpu),
        gl: Some(gl),
        win,
        monitors,
        rescan_monitors: false,
        current: monitor,
        picked: None,
        monitor_request: want_name,
        warned_monitors,
        buf_w,
        buf_h,
        mapped: true,
        swaps: 0,
    };
    let mut wait_failed = false;
    let mut rt = overlay::Runtime::new(watch, handle.config(), args.duration);

    loop {
        OverlayWindow::pump();
        if CLOSED.load(Ordering::SeqCst) {
            info!(
                "clean shutdown after {} swaps (window closed)",
                surface.swaps
            );
            return Ok(());
        }
        if handle.stopped() || rt.expired() {
            info!("clean shutdown after {} swaps", surface.swaps);
            return Ok(());
        }

        overlay::step(&mut rt, &handle, &mut surface)?;

        let result = OverlayWindow::wait(rt.wait_ms(), Some(handle.wake.event_handle()));
        match overlay::classify_win32_wait(result, 1) {
            overlay::Win32Wait::Wake => {
                handle.wake.take();
                wait_failed = false;
            }
            overlay::Win32Wait::Messages | overlay::Win32Wait::Timeout => {
                wait_failed = false;
            }
            overlay::Win32Wait::Failed(code) if !wait_failed => {
                error!(
                    "overlay wait failed: result {code:#x}, {}",
                    last_err("wait")
                );
                wait_failed = true;
            }
            overlay::Win32Wait::Failed(_) => {}
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn monitor(name: &str, left: i32, top: i32, width: i32, height: i32) -> Monitor {
        Monitor {
            name: name.into(),
            left,
            top,
            width,
            height,
            dpi: 96,
            primary: true,
        }
    }

    #[test]
    fn reassert_only_when_a_wanted_bit_is_missing() {
        assert!(!needs_reassert(WANTED_EXSTYLE, WANTED_EXSTYLE));
        assert!(
            !needs_reassert(WANTED_EXSTYLE | 0x0000_0001, WANTED_EXSTYLE),
            "extra bits are not a reason to touch the window"
        );
        assert!(needs_reassert(
            WANTED_EXSTYLE & !WS_EX_TRANSPARENT,
            WANTED_EXSTYLE
        ));
        assert!(needs_reassert(0, WANTED_EXSTYLE));
    }

    #[test]
    fn monitors_rescan_on_change_not_every_tick() {
        assert!(!rescan_due(false, false, r"\\.\DISPLAY1", r"\\.\DISPLAY1"));
        assert!(rescan_due(true, false, r"\\.\DISPLAY1", r"\\.\DISPLAY1"));
        assert!(rescan_due(false, true, r"\\.\DISPLAY1", r"\\.\DISPLAY1"));
        assert!(rescan_due(false, false, r"\\.\DISPLAY2", r"\\.\DISPLAY1"));
        assert!(!rescan_due(false, false, "", ""));
    }

    #[test]
    fn same_surface_ignores_name_case_not_size() {
        let a = monitor(r"\\.\DISPLAY1", 0, 0, 2560, 1440);
        assert!(a.same_surface(&monitor(r"\\.\display1", 0, 0, 2560, 1440)));
        assert!(!a.same_surface(&monitor(r"\\.\DISPLAY1", 0, 0, 1920, 1080)));
        assert!(!a.same_surface(&monitor(r"\\.\DISPLAY2", 0, 0, 2560, 1440)));
        assert!(!a.same_surface(&monitor(r"\\.\DISPLAY1", 1920, 0, 2560, 1440)));
    }
}
