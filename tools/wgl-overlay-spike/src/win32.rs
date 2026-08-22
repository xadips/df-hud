use std::error::Error;
use std::sync::atomic::{AtomicBool, Ordering};

use windows_sys::Win32::Foundation::{
    GetLastError, BOOL, HWND, LPARAM, LRESULT, POINT, RECT, WPARAM,
};
use windows_sys::Win32::Graphics::Dwm::DwmExtendFrameIntoClientArea;
use windows_sys::Win32::UI::Controls::MARGINS;
use windows_sys::Win32::Graphics::Gdi::{
    EnumDisplayMonitors, GetMonitorInfoW, HDC, HMONITOR, MONITORINFOEXW,
};
use windows_sys::Win32::UI::HiDpi::{
    GetDpiForMonitor, SetProcessDpiAwarenessContext, DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2,
    MDT_EFFECTIVE_DPI,
};
use windows_sys::Win32::UI::WindowsAndMessaging::{
    CreateWindowExW, DefWindowProcW, DestroyWindow, DispatchMessageW, GetWindowLongPtrW,
    LoadCursorW, PeekMessageW, RegisterClassExW, SetWindowLongPtrW, SetWindowPos, ShowWindow,
    TranslateMessage, CS_HREDRAW, CS_OWNDC, CS_VREDRAW, GWL_EXSTYLE, HWND_TOPMOST, IDC_ARROW,
    MONITORINFOF_PRIMARY, MSG, PM_REMOVE, SWP_NOACTIVATE, SWP_SHOWWINDOW, SW_HIDE, SW_SHOWNA,
    WM_CLOSE, WM_DESTROY, WNDCLASSEXW, WS_EX_LAYERED, WS_EX_NOACTIVATE, WS_EX_TOOLWINDOW,
    WS_EX_TOPMOST, WS_EX_TRANSPARENT, WS_POPUP,
};

pub const CLASS_NAME: &str = "df-hud-wgl-spike";
pub const WINDOW_TITLE: &str = "df-hud WGL overlay spike";

static CLOSED: AtomicBool = AtomicBool::new(false);

pub fn closed() -> bool {
    CLOSED.load(Ordering::SeqCst)
}

pub fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain(std::iter::once(0)).collect()
}

pub fn last_err(op: &str) -> Box<dyn Error> {
    let code = unsafe { GetLastError() };
    format!("{op}: Win32 error {code}").into()
}

pub fn enable_per_monitor_v2() {
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
    #[allow(dead_code)]
    pub handle: HMONITOR,
}

impl Monitor {
    pub fn matches(&self, want: &str) -> bool {
        let want = want.trim();
        self.name.eq_ignore_ascii_case(want)
            || self
                .name
                .rsplit('\\')
                .next()
                .is_some_and(|tail| tail.eq_ignore_ascii_case(want))
    }
}

pub fn list_monitors() -> Result<Vec<Monitor>, Box<dyn Error>> {
    let mut out: Vec<Monitor> = Vec::new();
    let ok = unsafe {
        EnumDisplayMonitors(
            std::ptr::null_mut(),
            std::ptr::null(),
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
        handle,
    });
    1
}

fn utf16_z(buf: &[u16]) -> String {
    let end = buf.iter().position(|&c| c == 0).unwrap_or(buf.len());
    String::from_utf16_lossy(&buf[..end])
}

pub fn pick_monitor<'a>(monitors: &'a [Monitor], want: Option<&str>) -> Result<&'a Monitor, Box<dyn Error>> {
    if let Some(want) = want {
        return monitors
            .iter()
            .find(|m| m.matches(want))
            .ok_or_else(|| format!("monitor {want:?} was not found (see --list-monitors)").into());
    }
    monitors
        .iter()
        .find(|m| m.primary)
        .or_else(|| monitors.first())
        .ok_or_else(|| "no monitors".into())
}

pub fn register_class(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
) -> Result<(), Box<dyn Error>> {
    let class = wide(CLASS_NAME);
    let wc = WNDCLASSEXW {
        cbSize: size_of::<WNDCLASSEXW>() as u32,
        style: CS_OWNDC | CS_HREDRAW | CS_VREDRAW,
        lpfnWndProc: Some(wndproc),
        cbClsExtra: 0,
        cbWndExtra: 0,
        hInstance: instance,
        hIcon: std::ptr::null_mut(),
        hCursor: unsafe { LoadCursorW(std::ptr::null_mut(), IDC_ARROW) },
        hbrBackground: std::ptr::null_mut(),
        lpszMenuName: std::ptr::null(),
        lpszClassName: class.as_ptr(),
        hIconSm: std::ptr::null_mut(),
    };
    if unsafe { RegisterClassExW(&wc) } == 0 {
        let err = unsafe { GetLastError() };
        // already registered in this process is fine
        if err != 1410 {
            return Err(last_err("RegisterClassExW"));
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

pub struct OverlayWindow {
    pub hwnd: HWND,
    click_through: bool,
}

impl OverlayWindow {
    pub fn create(
        instance: windows_sys::Win32::Foundation::HINSTANCE,
        monitor: &Monitor,
        inset: i32,
        click_through: bool,
    ) -> Result<Self, Box<dyn Error>> {
        if inset < 0 || 2 * inset >= monitor.width || 2 * inset >= monitor.height {
            return Err(format!(
                "inset {inset} is invalid for monitor {}x{}",
                monitor.width, monitor.height
            )
            .into());
        }
        let mut ex = WS_EX_LAYERED | WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE;
        if click_through {
            ex |= WS_EX_TRANSPARENT;
        }
        let class = wide(CLASS_NAME);
        let title = wide(WINDOW_TITLE);
        let x = monitor.left + inset;
        let y = monitor.top + inset;
        let w = monitor.width - 2 * inset;
        let h = monitor.height - 2 * inset;
        let hwnd = unsafe {
            CreateWindowExW(
                ex,
                class.as_ptr(),
                title.as_ptr(),
                WS_POPUP,
                x,
                y,
                w,
                h,
                std::ptr::null_mut(),
                std::ptr::null_mut(),
                instance,
                std::ptr::null(),
            )
        };
        if hwnd.is_null() {
            return Err(last_err("CreateWindowExW overlay"));
        }
        let win = Self {
            hwnd,
            click_through,
        };
        win.place(monitor, inset)?;
        Ok(win)
    }

    pub fn place(&self, monitor: &Monitor, inset: i32) -> Result<(), Box<dyn Error>> {
        let x = monitor.left + inset;
        let y = monitor.top + inset;
        let w = monitor.width - 2 * inset;
        let h = monitor.height - 2 * inset;
        let ok = unsafe {
            SetWindowPos(
                self.hwnd,
                HWND_TOPMOST,
                x,
                y,
                w,
                h,
                windows_sys::Win32::UI::WindowsAndMessaging::SWP_NOACTIVATE,
            )
        };
        if ok == 0 {
            return Err(last_err("SetWindowPos"));
        }
        eprintln!(
            "window {}x{} at {},{}  inset {inset}  (1px gap at the monitor edge is expected)",
            w, h, x, y
        );
        Ok(())
    }

    pub fn extend_dwm_frame(&self) -> Result<(), Box<dyn Error>> {
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
        Ok(())
    }

    pub fn reassert_exstyle(&self) {
        let mut ex = unsafe { GetWindowLongPtrW(self.hwnd, GWL_EXSTYLE) };
        ex |= (WS_EX_LAYERED | WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE) as isize;
        if self.click_through {
            ex |= WS_EX_TRANSPARENT as isize;
        }
        unsafe { SetWindowLongPtrW(self.hwnd, GWL_EXSTYLE, ex) };
        unsafe {
            SetWindowPos(
                self.hwnd,
                std::ptr::null_mut(),
                0,
                0,
                0,
                0,
                windows_sys::Win32::UI::WindowsAndMessaging::SWP_NOMOVE
                    | windows_sys::Win32::UI::WindowsAndMessaging::SWP_NOSIZE
                    | windows_sys::Win32::UI::WindowsAndMessaging::SWP_NOZORDER
                    | windows_sys::Win32::UI::WindowsAndMessaging::SWP_FRAMECHANGED
                    | SWP_NOACTIVATE,
            )
        };
    }

    pub fn hide(&self) {
        unsafe { ShowWindow(self.hwnd, SW_HIDE) };
        eprintln!("hidden (WGL context kept)");
    }

    pub fn show(&self) -> Result<(), Box<dyn Error>> {
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
                SWP_SHOWWINDOW | SWP_NOACTIVATE | windows_sys::Win32::UI::WindowsAndMessaging::SWP_NOMOVE
                    | windows_sys::Win32::UI::WindowsAndMessaging::SWP_NOSIZE,
            )
        };
        if ok == 0 {
            return Err(last_err("SetWindowPos show"));
        }
        eprintln!("shown (click-through must still hold)");
        Ok(())
    }

    pub fn pump() {
        let mut msg = MSG {
            hwnd: std::ptr::null_mut(),
            message: 0,
            wParam: 0,
            lParam: 0,
            time: 0,
            pt: POINT { x: 0, y: 0 },
        };
        while unsafe { PeekMessageW(&mut msg, std::ptr::null_mut(), 0, 0, PM_REMOVE) } != 0 {
            unsafe {
                TranslateMessage(&msg);
                DispatchMessageW(&msg);
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
