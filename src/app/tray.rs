//! Tray icon and menu. Linux StatusNotifierItem via ksni; Windows Shell_NotifyIconW.
//! No GTK, no fyne, no tokio.
//!
//! Grey: game not running. Yellow: game up and IPC is fine (or unused).
//! Orange: game is in-world but no Discord IPC client connected. Red: bind failed.

#[cfg(any(test, windows))]
use std::io::Cursor;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
#[cfg(windows)]
use std::sync::Mutex;

use crate::app::Handle;
use crate::format;
use crate::model::{View, Visibility};

const ICON_SIZE: i32 = 64;
const ACTIVE: [u8; 4] = [0xe6, 0xcc, 0x4d, 0xff];
const IDLE: [u8; 4] = [0x8a, 0x90, 0x99, 0xff];
const WARN: [u8; 4] = [0xe6, 0x8a, 0x3d, 0xff];
const ERROR: [u8; 4] = [0xe6, 0x4d, 0x4d, 0xff];

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum IconKind {
    Idle,
    Active,
    Warn,
    Error,
}

pub fn version_label() -> String {
    format!("df-hud {}", env!("CARGO_PKG_VERSION"))
}

#[cfg(test)]
fn tooltip(view: Option<&View>, vis: Visibility) -> String {
    tooltip_with_version(view, vis, "")
}

pub fn tooltip_with_version(view: Option<&View>, vis: Visibility, version: &str) -> String {
    let primary = match view {
        None => "starting".to_string(),
        Some(v) if !v.game_running => "the game is not running".to_string(),
        Some(v) if v.has_session => format!("in the city {}", format::clock(v.session_time.std())),
        Some(v) => format!(
            "client up {}, not in the city",
            format::clock(v.client_uptime.std())
        ),
    };
    let mut lines = vec![primary.clone()];
    if let Some(v) = view {
        if v.have_data && v.xp_available {
            let rate = format::rate(v.xp_per_hour);
            if !rate.is_empty() {
                lines.push(format!("xp {rate}/hr"));
            }
        }
        if let Some(status) = tooltip_status_line(&primary, &v.status) {
            lines.push(status);
        }
        if v.game_running && !vis.visible && !vis.reason.is_empty() {
            lines.push(format!("overlay hidden: {}", vis.reason));
        }
    }
    if !version.is_empty() {
        lines.push(format!("version {version}"));
    }
    lines.join("\n")
}

pub fn tooltip_with_presence(
    view: Option<&View>,
    vis: Visibility,
    bind_failed: bool,
    ipc_missing: bool,
    config_err: Option<&str>,
) -> String {
    let mut tip = tooltip_with_version(view, vis, env!("CARGO_PKG_VERSION"));
    if let Some(line) = config_tray_line(config_err) {
        tip.push('\n');
        tip.push_str(&line);
    }
    if bind_failed {
        tip.push_str("\nDiscord IPC unavailable - close Discord and retry the bind");
    } else if ipc_missing {
        tip.push_str(
            "\nDiscord IPC is not connected - restart the game while the overlay is listening",
        );
    }
    tip
}

/// One unclickable tray-menu line, or none when nothing is wrong.
pub fn menu_alert(
    view: Option<&View>,
    config_err: Option<&str>,
    bind_failed: bool,
    ipc_missing: bool,
) -> Option<String> {
    if let Some(line) = config_tray_line(config_err) {
        return Some(line);
    }
    if bind_failed {
        return Some("Discord IPC unavailable".into());
    }
    if ipc_missing {
        return Some("Discord IPC is not connected".into());
    }
    let status = view.map(|v| v.status.as_str()).unwrap_or("").trim();
    if status.is_empty()
        || status.contains("only_when_game_running")
        || status == "waiting for the first poll"
    {
        return None;
    }
    Some(clip_menu_alert(status))
}

/// Short tray text. The journal already has the path and the full reason.
fn config_tray_line(err: Option<&str>) -> Option<String> {
    let err = err.map(str::trim).filter(|s| !s.is_empty())?;
    let rest = match err.split_once(": ") {
        Some((prefix, rest)) if prefix.contains('/') || prefix.contains('\\') => rest,
        _ => err,
    };
    let brief = rest.split(" (").next().unwrap_or(rest).trim();
    if !brief.is_empty() && brief.chars().count() <= 36 && !brief.contains('/') {
        Some(format!("config: {brief}"))
    } else {
        Some("config error (see the log)".into())
    }
}

fn clip_menu_alert(s: &str) -> String {
    let line = s.lines().next().unwrap_or(s).trim();
    let mut out = String::new();
    for ch in line.chars() {
        if out.chars().count() >= 72 {
            out.push_str("...");
            break;
        }
        out.push(ch);
    }
    out
}

fn menu_alert_from(handle: &Handle) -> Option<String> {
    let view = handle.store.derive(chrono::Utc::now());
    let bind_failed = handle.presence_bind_failed();
    let missing = ipc_unconnected(
        Some(&view),
        handle.has_presence(),
        bind_failed,
        handle.presence_client_connected(),
        handle.vis.placement().launcher_only,
    );
    menu_alert(
        Some(&view),
        handle.config_error().as_deref(),
        bind_failed,
        missing,
    )
}

/// Status that is not already the first line, and not a poller pause that
/// restates "the game is not running" with a config key in parentheses.
fn tooltip_status_line(primary: &str, status: &str) -> Option<String> {
    let status = status.trim();
    if status.is_empty() || status.contains("only_when_game_running") {
        return None;
    }
    let p = primary.to_ascii_lowercase();
    let s = status.to_ascii_lowercase();
    if s == p || s.starts_with(&p) {
        return None;
    }
    Some(status.to_string())
}

/// Game is in-world, we own the socket, but the client never connected
/// (started the overlay after the game, or the game gave up).
pub fn ipc_unconnected(
    view: Option<&View>,
    presence_enabled: bool,
    bind_failed: bool,
    client_connected: bool,
    launcher_only: bool,
) -> bool {
    let Some(v) = view else {
        return false;
    };
    presence_enabled
        && !bind_failed
        && !client_connected
        && v.game_running
        && v.have_data
        && !launcher_only
}

pub fn icon_kind(view: Option<&View>, bind_failed: bool, ipc_missing: bool) -> IconKind {
    if bind_failed {
        return IconKind::Error;
    }
    if ipc_missing {
        return IconKind::Warn;
    }
    if view.map(|v| v.game_running).unwrap_or(false) {
        IconKind::Active
    } else {
        IconKind::Idle
    }
}

#[cfg(any(test, windows))]
pub fn icon_png(kind: IconKind, size: i32) -> Vec<u8> {
    let size = size.max(8);
    let rgba = match kind {
        IconKind::Active => ACTIVE,
        IconKind::Idle => IDLE,
        IconKind::Warn => WARN,
        IconKind::Error => ERROR,
    };
    encode_png(&raster(rgba, size), size)
}

#[cfg(windows)]
pub fn icon_bytes(kind: IconKind, size: i32) -> Vec<u8> {
    wrap_ico(&icon_png(kind, size), size)
}

fn raster(rgba: [u8; 4], size: i32) -> Vec<u8> {
    const SAMPLES: i32 = 4;
    let mut out = vec![0u8; (size * size * 4) as usize];
    for y in 0..size {
        for x in 0..size {
            let mut hits = 0;
            for sy in 0..SAMPLES {
                for sx in 0..SAMPLES {
                    let fx = (x as f64 + (sx as f64 + 0.5) / SAMPLES as f64) / size as f64;
                    let fy = (y as f64 + (sy as f64 + 0.5) / SAMPLES as f64) / size as f64;
                    if covers(fx, fy) {
                        hits += 1;
                    }
                }
            }
            if hits == 0 {
                continue;
            }
            let a = (rgba[3] as f64 * hits as f64 / (SAMPLES * SAMPLES) as f64 + 0.5) as u8;
            let i = ((y * size + x) * 4) as usize;
            out[i] = rgba[0];
            out[i + 1] = rgba[1];
            out[i + 2] = rgba[2];
            out[i + 3] = a;
        }
    }
    out
}

fn covers(x: f64, y: f64) -> bool {
    let dx = x - 0.5;
    let dy = y - 0.5;
    let r = (dx * dx + dy * dy).sqrt();
    if r <= 0.11 {
        return true;
    }
    if r >= 0.29 && r <= 0.39 {
        return true;
    }
    if r > 0.39 && r <= 0.5 {
        return dx.abs() <= 0.045 || dy.abs() <= 0.045;
    }
    false
}

#[cfg(any(test, windows))]
fn encode_png(rgba: &[u8], size: i32) -> Vec<u8> {
    let mut out = Vec::new();
    {
        let mut enc = png::Encoder::new(Cursor::new(&mut out), size as u32, size as u32);
        enc.set_color(png::ColorType::Rgba);
        enc.set_depth(png::BitDepth::Eight);
        let mut writer = enc.write_header().expect("png header");
        writer.write_image_data(rgba).expect("png data");
    }
    out
}

#[cfg(windows)]
fn wrap_ico(png: &[u8], size: i32) -> Vec<u8> {
    let mut out = Vec::with_capacity(22 + png.len());
    out.extend_from_slice(&0u16.to_le_bytes());
    out.extend_from_slice(&1u16.to_le_bytes());
    out.extend_from_slice(&1u16.to_le_bytes());
    if size >= 256 {
        out.push(0);
        out.push(0);
    } else {
        out.push(size as u8);
        out.push(size as u8);
    }
    out.push(0);
    out.push(0);
    out.extend_from_slice(&1u16.to_le_bytes());
    out.extend_from_slice(&32u16.to_le_bytes());
    out.extend_from_slice(&(png.len() as u32).to_le_bytes());
    out.extend_from_slice(&22u32.to_le_bytes());
    out.extend_from_slice(png);
    out
}

pub fn spawn(handle: Arc<Handle>, stop: Arc<AtomicBool>) {
    if !handle.cfg.lock().unwrap().tray.enabled {
        return;
    }
    std::thread::Builder::new()
        .name("df-hud-tray".into())
        .spawn(move || run(handle, stop))
        .expect("spawn tray");
}

fn run(handle: Arc<Handle>, stop: Arc<AtomicBool>) {
    #[cfg(target_os = "linux")]
    linux::run(handle, stop);
    #[cfg(windows)]
    windows::run(handle, stop);
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        let _ = (handle, stop);
    }
}

#[cfg(target_os = "linux")]
mod linux {
    use super::*;
    use std::time::Duration;

    use ksni::blocking::TrayMethods;
    use ksni::menu::{CheckmarkItem, StandardItem};
    use ksni::{Icon, MenuItem, ToolTip, Tray};

    struct HudTray {
        handle: Arc<Handle>,
        kind: IconKind,
        tip: String,
    }

    impl Tray for HudTray {
        fn id(&self) -> String {
            "df-hud".into()
        }

        fn title(&self) -> String {
            "df-hud".into()
        }

        fn icon_name(&self) -> String {
            // Empty so hosts use IconPixmap (the reticle), not a theme icon.
            String::new()
        }

        fn icon_pixmap(&self) -> Vec<Icon> {
            vec![icon_argb(self.kind)]
        }

        fn tool_tip(&self) -> ToolTip {
            ToolTip {
                icon_name: String::new(),
                icon_pixmap: vec![icon_argb(self.kind)],
                title: "df-hud".into(),
                description: self.tip.clone(),
            }
        }

        fn activate(&mut self, _x: i32, _y: i32) {
            let _ = self.handle.toggle_overlay();
        }

        fn menu_about_to_show(&mut self) {
            // Override so ksni rebuilds the menu on right-click. The default
            // impl leaves the layout frozen from the last svc.update().
        }

        fn menu(&self) -> Vec<MenuItem<Self>> {
            let mut items = Vec::new();
            if let Some(alert) = menu_alert_from(&self.handle) {
                items.push(
                    StandardItem {
                        label: alert,
                        enabled: false,
                        ..StandardItem::default()
                    }
                    .into(),
                );
                items.push(MenuItem::Separator);
            }
            items.extend([
                CheckmarkItem {
                    label: "Show overlay".into(),
                    checked: self.handle.overlay_on.load(Ordering::SeqCst),
                    activate: Box::new(|this: &mut HudTray| {
                        let _ = this.handle.toggle_overlay();
                    }),
                    ..CheckmarkItem::default()
                }
                .into(),
                CheckmarkItem {
                    label: "Show challenges".into(),
                    checked: self.handle.groups.shown("challenges"),
                    activate: Box::new(|this: &mut HudTray| {
                        let _ = this.handle.toggle_group("challenges");
                    }),
                    ..CheckmarkItem::default()
                }
                .into(),
                CheckmarkItem {
                    label: "FPS display on launch".into(),
                    checked: self.handle.gamekeys.fps_display(),
                    activate: Box::new(|this: &mut HudTray| {
                        this.handle
                            .set_fps_display(!this.handle.gamekeys.fps_display());
                    }),
                    ..CheckmarkItem::default()
                }
                .into(),
                CheckmarkItem {
                    label: "Skip the launcher".into(),
                    checked: self.handle.gamekeys.dismiss_launcher(),
                    activate: Box::new(|this: &mut HudTray| {
                        this.handle
                            .set_dismiss_launcher(!this.handle.gamekeys.dismiss_launcher());
                    }),
                    ..CheckmarkItem::default()
                }
                .into(),
                MenuItem::Separator,
                StandardItem {
                    label: "Reset xp/hr".into(),
                    activate: Box::new(|this: &mut HudTray| this.handle.reset_xp()),
                    ..StandardItem::default()
                }
                .into(),
                StandardItem {
                    label: "Restart run clock".into(),
                    activate: Box::new(|this: &mut HudTray| this.handle.restart_run()),
                    ..StandardItem::default()
                }
                .into(),
            ]);
            if self.handle.has_presence() {
                items.push(
                    StandardItem {
                        label: "Retry Discord IPC bind".into(),
                        activate: Box::new(|this: &mut HudTray| {
                            let _ = this.handle.retry_presence();
                        }),
                        ..StandardItem::default()
                    }
                    .into(),
                );
            }
            if crate::app::autostart::available() {
                items.push(
                    CheckmarkItem {
                        label: "Start df-hud with Windows".into(),
                        checked: self.handle.start_on_login(),
                        activate: Box::new(|this: &mut HudTray| {
                            this.handle
                                .set_start_on_login(!this.handle.start_on_login());
                        }),
                        ..CheckmarkItem::default()
                    }
                    .into(),
                );
            }
            if cfg!(windows) {
                items.push(
                    StandardItem {
                        label: "Open config file".into(),
                        activate: Box::new(|this: &mut HudTray| this.handle.open_config()),
                        ..StandardItem::default()
                    }
                    .into(),
                );
            }
            items.push(
                StandardItem {
                    label: "Reload config".into(),
                    activate: Box::new(|this: &mut HudTray| this.handle.reload_config()),
                    ..StandardItem::default()
                }
                .into(),
            );
            items.push(MenuItem::Separator);
            items.push(
                StandardItem {
                    label: version_label(),
                    enabled: false,
                    ..StandardItem::default()
                }
                .into(),
            );
            items.push(
                StandardItem {
                    label: "Quit df-hud".into(),
                    activate: Box::new(|this: &mut HudTray| this.handle.request_stop()),
                    ..StandardItem::default()
                }
                .into(),
            );
            items
        }

        fn watcher_offline(&self, reason: ksni::OfflineReason) -> bool {
            eprintln!(
                "tray: StatusNotifierWatcher offline ({reason:?}); HTTP remains the control hatch"
            );
            true
        }
    }

    fn icon_argb(kind: IconKind) -> Icon {
        let rgba = match kind {
            IconKind::Active => raster(ACTIVE, ICON_SIZE),
            IconKind::Idle => raster(IDLE, ICON_SIZE),
            IconKind::Warn => raster(WARN, ICON_SIZE),
            IconKind::Error => raster(ERROR, ICON_SIZE),
        };
        let mut data = rgba;
        for px in data.chunks_exact_mut(4) {
            px.rotate_right(1);
        }
        Icon {
            width: ICON_SIZE,
            height: ICON_SIZE,
            data,
        }
    }

    fn snapshot(handle: &Handle) -> (IconKind, String, bool, bool, bool, bool) {
        let vis = handle.store.visibility();
        let view = handle.store.derive(chrono::Utc::now());
        let bind_failed = handle.presence_bind_failed();
        let missing = ipc_unconnected(
            Some(&view),
            handle.has_presence(),
            bind_failed,
            handle.presence_client_connected(),
            handle.vis.placement().launcher_only,
        );
        let config_err = handle.config_error();
        (
            icon_kind(Some(&view), bind_failed, missing),
            tooltip_with_presence(
                Some(&view),
                vis,
                bind_failed,
                missing,
                config_err.as_deref(),
            ),
            handle.overlay_on.load(Ordering::SeqCst),
            handle.groups.shown("challenges"),
            handle.gamekeys.fps_display(),
            handle.gamekeys.dismiss_launcher(),
        )
    }

    pub fn run(handle: Arc<Handle>, stop: Arc<AtomicBool>) {
        let (kind, tip, ..) = snapshot(&handle);
        let tray = HudTray {
            kind,
            tip,
            handle: handle.clone(),
        };
        match tray.assume_sni_available(true).spawn() {
            Ok(svc) => {
                eprintln!("tray: StatusNotifierItem via ksni");
                let mut last = snapshot(&handle);
                while !stop.load(Ordering::SeqCst) && !handle.stopped() {
                    let next = snapshot(&handle);
                    if next != last {
                        last = next.clone();
                        let (kind, tip, ..) = next;
                        if svc
                            .update(|t| {
                                t.kind = kind;
                                t.tip = tip;
                            })
                            .is_none()
                        {
                            eprintln!(
                                "tray: StatusNotifierItem stopped; HTTP remains the control hatch"
                            );
                            break;
                        }
                    }
                    std::thread::sleep(Duration::from_millis(200));
                }
                svc.shutdown().wait();
            }
            Err(err) => {
                eprintln!(
                    "tray: StatusNotifierItem failed ({err}); HTTP remains the control hatch"
                );
                while !stop.load(Ordering::SeqCst) && !handle.stopped() {
                    std::thread::sleep(Duration::from_secs(1));
                }
            }
        }
    }
}

#[cfg(windows)]
mod windows {
    use super::*;
    use crate::overlay::win32::wide;
    use std::mem::{size_of, zeroed};
    use std::ptr;
    use std::time::Duration;
    use windows_sys::Win32::Foundation::{HWND, LPARAM, LRESULT, WPARAM};
    use windows_sys::Win32::UI::Shell::{
        Shell_NotifyIconW, NIF_ICON, NIF_MESSAGE, NIF_TIP, NIM_ADD, NIM_DELETE, NIM_MODIFY,
        NOTIFYICONDATAW,
    };
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        CreatePopupMenu, CreateWindowExW, DefWindowProcW, DestroyIcon, DestroyWindow,
        DispatchMessageW, GetCursorPos, InsertMenuW, LoadCursorW, PeekMessageW, PostQuitMessage,
        RegisterClassExW, SetForegroundWindow, TrackPopupMenu, TranslateMessage, CS_HREDRAW,
        CS_VREDRAW, CW_USEDEFAULT, IDC_ARROW, MF_CHECKED, MF_DISABLED, MF_GRAYED, MF_SEPARATOR,
        MF_STRING, MF_UNCHECKED, TPM_RIGHTBUTTON, WM_APP, WM_COMMAND, WM_DESTROY, WM_LBUTTONUP,
        WM_RBUTTONUP, WNDCLASSEXW, WS_OVERLAPPED,
    };

    const WM_TRAY: u32 = WM_APP + 1;
    const ID_OVERLAY: u16 = 1;
    const ID_CHALLENGES: u16 = 2;
    const ID_FPS: u16 = 3;
    const ID_LAUNCHER: u16 = 4;
    const ID_XP: u16 = 20;
    const ID_RUN: u16 = 21;
    const ID_RETRY: u16 = 23;
    const ID_RELOAD: u16 = 24;
    const ID_STARTUP: u16 = 25;
    const ID_OPEN_CONFIG: u16 = 26;
    const ID_QUIT: u16 = 22;

    struct Ctx {
        handle: Arc<Handle>,
        hwnd: HWND,
        icon: windows_sys::Win32::UI::WindowsAndMessaging::HICON,
        kind: IconKind,
        tip: String,
    }

    unsafe impl Send for Ctx {}
    unsafe impl Sync for Ctx {}

    static CTX: Mutex<Option<Box<Ctx>>> = Mutex::new(None);

    pub fn run(handle: Arc<Handle>, stop: Arc<AtomicBool>) {
        unsafe { run_inner(handle, stop) }
    }

    unsafe fn run_inner(handle: Arc<Handle>, stop: Arc<AtomicBool>) {
        let class = wide("df-hud-tray");
        let wc = WNDCLASSEXW {
            cbSize: size_of::<WNDCLASSEXW>() as u32,
            style: CS_HREDRAW | CS_VREDRAW,
            lpfnWndProc: Some(wndproc),
            hInstance: windows_sys::Win32::System::LibraryLoader::GetModuleHandleW(ptr::null()),
            hCursor: LoadCursorW(ptr::null_mut(), IDC_ARROW),
            lpszClassName: class.as_ptr(),
            ..zeroed()
        };
        RegisterClassExW(&wc);
        let hwnd = CreateWindowExW(
            0,
            class.as_ptr(),
            class.as_ptr(),
            WS_OVERLAPPED,
            CW_USEDEFAULT,
            CW_USEDEFAULT,
            CW_USEDEFAULT,
            CW_USEDEFAULT,
            ptr::null_mut(),
            ptr::null_mut(),
            wc.hInstance,
            ptr::null(),
        );
        if hwnd.is_null() {
            eprintln!("tray: CreateWindowExW failed");
            return;
        }
        let png = icon_bytes(IconKind::Idle, ICON_SIZE);
        let icon = create_icon(&png);
        let mut nid: NOTIFYICONDATAW = zeroed();
        nid.cbSize = size_of::<NOTIFYICONDATAW>() as u32;
        nid.hWnd = hwnd;
        nid.uID = 1;
        nid.uFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP;
        nid.uCallbackMessage = WM_TRAY;
        nid.hIcon = icon;
        write_tip(&mut nid, "df-hud: starting");
        Shell_NotifyIconW(NIM_ADD, &nid);
        *CTX.lock().unwrap() = Some(Box::new(Ctx {
            handle: handle.clone(),
            hwnd,
            icon,
            kind: IconKind::Idle,
            tip: "df-hud: starting".into(),
        }));
        let mut msg = zeroed();
        while !stop.load(Ordering::SeqCst) {
            refresh();
            if PeekMessageW(&mut msg, hwnd, 0, 0, 1) != 0 {
                if msg.message == WM_DESTROY {
                    break;
                }
                TranslateMessage(&msg);
                DispatchMessageW(&msg);
            } else {
                std::thread::sleep(Duration::from_millis(200));
            }
        }
        Shell_NotifyIconW(NIM_DELETE, &nid);
        if !icon.is_null() {
            DestroyIcon(icon);
        }
        DestroyWindow(hwnd);
        *CTX.lock().unwrap() = None;
    }

    unsafe fn refresh() {
        let mut g = CTX.lock().unwrap();
        let Some(ctx) = g.as_mut() else {
            return;
        };
        let view = ctx.handle.store.derive(chrono::Utc::now());
        let vis = ctx.handle.store.visibility();
        let bind_failed = ctx.handle.presence_bind_failed();
        let missing = ipc_unconnected(
            Some(&view),
            ctx.handle.has_presence(),
            bind_failed,
            ctx.handle.presence_client_connected(),
            ctx.handle.vis.placement().launcher_only,
        );
        let kind = icon_kind(Some(&view), bind_failed, missing);
        let tip = tooltip_with_presence(
            Some(&view),
            vis,
            bind_failed,
            missing,
            ctx.handle.config_error().as_deref(),
        );
        if kind == ctx.kind && tip == ctx.tip {
            return;
        }
        ctx.kind = kind;
        ctx.tip = tip.clone();
        if !ctx.icon.is_null() {
            DestroyIcon(ctx.icon);
        }
        let bytes = icon_bytes(kind, ICON_SIZE);
        ctx.icon = create_icon(&bytes);
        let mut nid: NOTIFYICONDATAW = zeroed();
        nid.cbSize = size_of::<NOTIFYICONDATAW>() as u32;
        nid.hWnd = ctx.hwnd;
        nid.uID = 1;
        nid.uFlags = NIF_ICON | NIF_TIP;
        nid.hIcon = ctx.icon;
        write_tip(&mut nid, &tip);
        Shell_NotifyIconW(NIM_MODIFY, &nid);
    }

    unsafe extern "system" fn wndproc(hwnd: HWND, msg: u32, wp: WPARAM, lp: LPARAM) -> LRESULT {
        match msg {
            WM_TRAY => {
                if lp as u32 == WM_RBUTTONUP || lp as u32 == WM_LBUTTONUP {
                    show_menu(hwnd);
                }
                0
            }
            WM_COMMAND => {
                on_command(wp as u16);
                0
            }
            WM_DESTROY => {
                PostQuitMessage(0);
                0
            }
            _ => DefWindowProcW(hwnd, msg, wp, lp),
        }
    }

    unsafe fn show_menu(hwnd: HWND) {
        let g = CTX.lock().unwrap();
        let Some(ctx) = g.as_ref() else {
            return;
        };
        let menu = CreatePopupMenu();
        if let Some(alert) = menu_alert_from(&ctx.handle) {
            InsertMenuW(
                menu,
                u32::MAX,
                MF_STRING | MF_GRAYED | MF_DISABLED,
                0,
                wide(&alert).as_ptr(),
            );
            InsertMenuW(menu, u32::MAX, MF_SEPARATOR, 0, ptr::null());
        }
        let overlay_on = ctx.handle.overlay_on.load(Ordering::SeqCst);
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING | if overlay_on { MF_CHECKED } else { MF_UNCHECKED },
            ID_OVERLAY as usize,
            wide("Show overlay").as_ptr(),
        );
        let board = ctx.handle.groups.shown("challenges");
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING | if board { MF_CHECKED } else { MF_UNCHECKED },
            ID_CHALLENGES as usize,
            wide("Show challenges").as_ptr(),
        );
        let fps = ctx.handle.gamekeys.fps_display();
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING | if fps { MF_CHECKED } else { MF_UNCHECKED },
            ID_FPS as usize,
            wide("FPS display on launch").as_ptr(),
        );
        let skip = ctx.handle.gamekeys.dismiss_launcher();
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING | if skip { MF_CHECKED } else { MF_UNCHECKED },
            ID_LAUNCHER as usize,
            wide("Skip the launcher").as_ptr(),
        );
        InsertMenuW(menu, u32::MAX, MF_SEPARATOR, 0, ptr::null());
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING,
            ID_XP as usize,
            wide("Reset xp/hr").as_ptr(),
        );
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING,
            ID_RUN as usize,
            wide("Restart run clock").as_ptr(),
        );
        if ctx.handle.has_presence() {
            InsertMenuW(
                menu,
                u32::MAX,
                MF_STRING,
                ID_RETRY as usize,
                wide("Retry Discord IPC bind").as_ptr(),
            );
        }
        if crate::app::autostart::available() {
            let startup = ctx.handle.start_on_login();
            InsertMenuW(
                menu,
                u32::MAX,
                MF_STRING | if startup { MF_CHECKED } else { MF_UNCHECKED },
                ID_STARTUP as usize,
                wide("Start df-hud with Windows").as_ptr(),
            );
        }
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING,
            ID_OPEN_CONFIG as usize,
            wide("Open config file").as_ptr(),
        );
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING,
            ID_RELOAD as usize,
            wide("Reload config").as_ptr(),
        );
        InsertMenuW(menu, u32::MAX, MF_SEPARATOR, 0, ptr::null());
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING | MF_GRAYED | MF_DISABLED,
            0,
            wide(&version_label()).as_ptr(),
        );
        InsertMenuW(
            menu,
            u32::MAX,
            MF_STRING,
            ID_QUIT as usize,
            wide("Quit df-hud").as_ptr(),
        );
        drop(g);
        let mut pt = zeroed();
        GetCursorPos(&mut pt);
        SetForegroundWindow(hwnd);
        TrackPopupMenu(menu, TPM_RIGHTBUTTON, pt.x, pt.y, 0, hwnd, ptr::null());
    }

    fn on_command(id: u16) {
        let g = CTX.lock().unwrap();
        let Some(ctx) = g.as_ref() else {
            return;
        };
        let h = ctx.handle.clone();
        drop(g);
        match id {
            ID_OVERLAY => {
                let _ = h.toggle_overlay();
            }
            ID_CHALLENGES => {
                let _ = h.toggle_group("challenges");
            }
            ID_FPS => h.set_fps_display(!h.gamekeys.fps_display()),
            ID_LAUNCHER => h.set_dismiss_launcher(!h.gamekeys.dismiss_launcher()),
            ID_XP => h.reset_xp(),
            ID_RUN => h.restart_run(),
            ID_RETRY => {
                let _ = h.retry_presence();
            }
            ID_STARTUP => h.set_start_on_login(!h.start_on_login()),
            ID_OPEN_CONFIG => h.open_config(),
            ID_RELOAD => h.reload_config(),
            ID_QUIT => h.request_stop(),
            _ => {}
        }
    }

    fn write_tip(nid: &mut NOTIFYICONDATAW, tip: &str) {
        let v: Vec<u16> = tip.encode_utf16().take(127).collect();
        for (i, c) in v.iter().enumerate() {
            nid.szTip[i] = *c;
        }
        if v.len() < nid.szTip.len() {
            nid.szTip[v.len()] = 0;
        }
    }

    fn create_icon(ico: &[u8]) -> windows_sys::Win32::UI::WindowsAndMessaging::HICON {
        use windows_sys::Win32::UI::WindowsAndMessaging::{
            CreateIconFromResourceEx, LR_DEFAULTCOLOR,
        };
        unsafe {
            CreateIconFromResourceEx(
                ico.as_ptr() as *mut u8,
                ico.len() as u32,
                1,
                0x00030000,
                0,
                0,
                LR_DEFAULTCOLOR,
            )
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{Ns, View, Visibility};
    use std::time::Duration;

    #[test]
    fn tooltip_states() {
        let vis = Visibility {
            visible: true,
            ..Visibility::default()
        };
        assert!(tooltip(None, vis.clone()).contains("starting"));
        let closed = View::default();
        assert!(tooltip(Some(&closed), vis.clone()).contains("not running"));
        let playing = View {
            game_running: true,
            has_session: true,
            session_time: Ns::from_std(Duration::from_secs(12 * 60 + 34)),
            have_data: true,
            xp_available: true,
            xp_per_hour: 19_500_000.0,
            ..View::default()
        };
        let got = tooltip(Some(&playing), vis.clone());
        assert!(
            got.contains("in the city 0:12:34") && got.contains("xp 19,500,000/hr"),
            "{got}"
        );
        let lines: Vec<_> = got.split('\n').collect();
        assert_eq!(lines.len(), 2, "{lines:?}");
        assert!(lines[0].starts_with("in the city"), "{}", lines[0]);
        assert!(
            tooltip_with_version(Some(&playing), vis.clone(), "1.2.3").contains("version 1.2.3")
        );
        assert_eq!(
            version_label(),
            format!("df-hud {}", env!("CARGO_PKG_VERSION"))
        );
        let live = tooltip_with_presence(Some(&playing), vis.clone(), false, false, None);
        assert!(
            live.contains(&format!("version {}", env!("CARGO_PKG_VERSION"))),
            "{live}"
        );
        let stale = View {
            game_running: true,
            xp_available: true,
            xp_per_hour: 1234.0,
            ..View::default()
        };
        assert!(!tooltip(Some(&stale), vis.clone()).contains("xp "));
        let loading = View {
            game_running: true,
            client_uptime: Ns::from_std(Duration::from_secs(180)),
            ..View::default()
        };
        let got = tooltip(Some(&loading), vis);
        assert!(
            got.contains("client up 0:03:00") && got.contains("not in the city"),
            "{got}"
        );
    }

    #[test]
    fn tooltip_explains_a_hidden_overlay() {
        let hidden = Visibility {
            visible: false,
            reason: "the game is on workspace 7, which is not the one being shown".into(),
            ..Visibility::default()
        };
        let playing = View {
            game_running: true,
            has_session: true,
            session_time: Ns::from_std(Duration::from_secs(60)),
            ..View::default()
        };
        assert!(tooltip(Some(&playing), hidden.clone()).contains("workspace 7"));
        let closed = tooltip(
            Some(&View::default()),
            Visibility {
                visible: false,
                reason: "the game is not running".into(),
                ..Visibility::default()
            },
        );
        assert_eq!(closed.matches("not running").count(), 1, "{closed}");
        let paused = View {
            status: "the game is not running (poll.only_when_game_running)".into(),
            ..View::default()
        };
        let paused_tip = tooltip(Some(&paused), vis_closed());
        assert_eq!(paused_tip.matches("not running").count(), 1, "{paused_tip}");
        assert!(
            !paused_tip.contains("only_when_game_running"),
            "{paused_tip}"
        );
    }

    fn vis_closed() -> Visibility {
        Visibility {
            visible: false,
            reason: "the game is not running".into(),
            ..Visibility::default()
        }
    }

    #[test]
    fn menu_alert_picks_the_urgent_line() {
        let idle = View::default();
        assert_eq!(menu_alert(Some(&idle), None, false, false), None);
        assert_eq!(
            menu_alert(
                Some(&View {
                    status: "the game is not running (poll.only_when_game_running)".into(),
                    ..View::default()
                }),
                None,
                false,
                false,
            ),
            None
        );
        assert_eq!(
            menu_alert(Some(&idle), Some("unknown key wibble"), false, false).as_deref(),
            Some("config: unknown key wibble")
        );
        assert_eq!(
            menu_alert(
                Some(&idle),
                Some("/home/me/.config/df-hud/config.toml: unknown key widget.bamamap (a typo here would otherwise be silently ignored; see df-hud.example.toml)"),
                false,
                false,
            )
            .as_deref(),
            Some("config: unknown key widget.bamamap")
        );
        assert_eq!(
            menu_alert(Some(&idle), None, true, true).as_deref(),
            Some("Discord IPC unavailable")
        );
        assert_eq!(
            menu_alert(Some(&idle), None, false, true).as_deref(),
            Some("Discord IPC is not connected")
        );
        let expired = View {
            status: "session expired - open any Dead Frontier page to refresh".into(),
            ..View::default()
        };
        assert!(menu_alert(Some(&expired), None, false, false)
            .unwrap()
            .contains("session expired"));
        let long = "x".repeat(80);
        assert_eq!(
            menu_alert(Some(&idle), Some(&long), false, false).as_deref(),
            Some("config error (see the log)")
        );
    }

    #[test]
    fn tooltip_carries_status() {
        let v = View {
            game_running: true,
            status: "session expired - open any Dead Frontier page to refresh".into(),
            ..View::default()
        };
        assert!(tooltip(
            Some(&v),
            Visibility {
                visible: true,
                ..Visibility::default()
            }
        )
        .contains("session expired"));
    }

    #[test]
    fn icon_kind_follows_the_game() {
        assert_eq!(icon_kind(None, false, false), IconKind::Idle);
        assert_eq!(
            icon_kind(Some(&View::default()), false, false),
            IconKind::Idle
        );
        assert_eq!(
            icon_kind(
                Some(&View {
                    game_running: true,
                    ..View::default()
                }),
                false,
                false
            ),
            IconKind::Active
        );
        assert_eq!(
            icon_kind(
                Some(&View {
                    game_running: true,
                    have_data: true,
                    ..View::default()
                }),
                false,
                true
            ),
            IconKind::Warn
        );
        assert_eq!(
            icon_kind(
                Some(&View {
                    game_running: true,
                    ..View::default()
                }),
                true,
                true
            ),
            IconKind::Error
        );
    }

    #[test]
    fn tooltip_names_a_missing_ipc_client() {
        let vis = Visibility {
            visible: true,
            ..Visibility::default()
        };
        let playing = View {
            game_running: true,
            have_data: true,
            ..View::default()
        };
        assert!(ipc_unconnected(Some(&playing), true, false, false, false));
        assert!(!ipc_unconnected(Some(&playing), true, false, true, false));
        assert!(!ipc_unconnected(Some(&playing), true, false, false, true));
        let tip = tooltip_with_presence(Some(&playing), vis.clone(), false, true, None);
        assert!(tip.contains("Discord IPC is not connected"), "{tip}");
        let bad = tooltip_with_presence(
            Some(&playing),
            vis,
            false,
            false,
            Some("/home/me/.config/df-hud/config.toml: unknown key widgext (a typo here would otherwise be silently ignored; see df-hud.example.toml)"),
        );
        assert!(bad.contains("config: unknown key widgext"), "{bad}");
        assert!(!bad.contains("/home/me/"), "{bad}");
        assert!(!bad.contains("silently ignored"), "{bad}");
    }

    #[test]
    fn icon_png_is_a_reticle() {
        for kind in [
            IconKind::Active,
            IconKind::Idle,
            IconKind::Warn,
            IconKind::Error,
        ] {
            for size in [16, 64] {
                let data = icon_png(kind, size);
                let dec = png::Decoder::new(Cursor::new(&data));
                let mut reader = dec.read_info().unwrap();
                assert_eq!(reader.info().width, size as u32);
                assert_eq!(reader.info().height, size as u32);
                let mut buf = vec![0; reader.output_buffer_size()];
                reader.next_frame(&mut buf).unwrap();
                let _ = buf;
            }
        }
        let data = icon_png(IconKind::Active, 64);
        let mut reader = png::Decoder::new(Cursor::new(&data)).read_info().unwrap();
        let mut buf = vec![0; reader.output_buffer_size()];
        let info = reader.next_frame(&mut buf).unwrap();
        let (w, h) = (info.width as usize, info.height as usize);
        let at = |x, y| {
            let i = (y * w + x) * 4;
            buf[i + 3]
        };
        assert_ne!(at(32, 32), 0, "centre dot");
        assert_eq!(at(32 + 13, 32), 0, "gap");
        assert_ne!(at(32 + 22, 32), 0, "ring");
        assert_eq!(at(1, 1), 0, "corner");
        let _ = h;
    }

    #[test]
    fn icon_size_floor() {
        let data = icon_png(IconKind::Idle, 0);
        let reader = png::Decoder::new(Cursor::new(&data)).read_info().unwrap();
        assert!(reader.info().width >= 8);
    }
}
