//! Tray icon and menu. Linux `StatusNotifierItem` via ksni; Windows `Shell_NotifyIconW`.
//! No GTK, no fyne, no tokio.
//!
//! Grey: game not running. Yellow: game up and IPC is fine (or unused).
//! Orange: game is in-world but no Discord IPC client connected. Red: bind failed.

#[cfg(test)]
use std::io::Cursor;
use std::sync::Arc;
#[cfg(windows)]
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, Ordering};

use crate::app::{Handle, store::TrayHint};
use crate::format;
use crate::model::{Ns, View, Visibility};

#[cfg(target_os = "linux")]
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

pub trait TrayState {
    fn game_running(&self) -> bool;
    fn has_session(&self) -> bool;
    fn session_time(&self) -> Ns;
    fn client_uptime(&self) -> Ns;
    fn have_data(&self) -> bool;
    fn xp_available(&self) -> bool;
    fn xp_per_hour(&self) -> f64;
    fn status(&self) -> &str;
}

macro_rules! impl_tray_state {
    ($type:ty) => {
        impl TrayState for $type {
            fn game_running(&self) -> bool {
                self.game_running
            }
            fn has_session(&self) -> bool {
                self.has_session
            }
            fn session_time(&self) -> Ns {
                self.session_time
            }
            fn client_uptime(&self) -> Ns {
                self.client_uptime
            }
            fn have_data(&self) -> bool {
                self.have_data
            }
            fn xp_available(&self) -> bool {
                self.xp_available
            }
            fn xp_per_hour(&self) -> f64 {
                self.xp_per_hour
            }
            fn status(&self) -> &str {
                &self.status
            }
        }
    };
}

impl_tray_state!(View);
impl_tray_state!(TrayHint);

pub fn version_label() -> String {
    format!("df-hud {}", env!("CARGO_PKG_VERSION"))
}

/// The update item's label on both trays; clicking it (re-)checks.
pub fn update_label(status: &crate::app::updates::Status) -> String {
    use crate::app::updates::Status;
    match status {
        Status::Unchecked => "Check for updates".into(),
        Status::Checking => "Checking for updates…".into(),
        Status::UpToDate => format!("{} is the latest", version_label()),
        Status::Newer(version) => format!("Update to {version}…"),
        Status::Failed => "Update check failed - see log".into(),
    }
}

#[cfg(test)]
fn tooltip(view: Option<&View>, vis: Visibility) -> String {
    tooltip_with_version(view.map(|v| v as &dyn TrayState), vis, "")
}

pub fn tooltip_with_version(
    view: Option<&dyn TrayState>,
    vis: Visibility,
    version: &str,
) -> String {
    let primary = match view {
        None => "starting".to_string(),
        Some(v) if !v.game_running() => "the game is not running".to_string(),
        Some(v) if v.has_session() => {
            format!("in the city {}", format::clock(v.session_time().std()))
        }
        Some(v) => format!(
            "client up {}, not in the city",
            format::clock(v.client_uptime().std())
        ),
    };
    let mut lines = vec![primary.clone()];
    if let Some(v) = view {
        if v.have_data() && v.xp_available() {
            let rate = format::rate(v.xp_per_hour());
            if !rate.is_empty() {
                lines.push(format!("xp {rate}/hr"));
            }
        }
        if let Some(status) = tooltip_status_line(&primary, v.status()) {
            lines.push(status);
        }
        if v.game_running() && !vis.visible && !vis.reason.is_empty() {
            lines.push(format!("overlay hidden: {}", vis.reason));
        }
    }
    if !version.is_empty() {
        lines.push(format!("version {version}"));
    }
    lines.join("\n")
}

pub fn tooltip_with_presence(
    view: Option<&dyn TrayState>,
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
    view: Option<&dyn TrayState>,
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
    let status = view.map_or("", TrayState::status).trim();
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
    let view = handle.store.tray_hint(chrono::Utc::now());
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
    view: Option<&dyn TrayState>,
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
        && v.game_running()
        && v.have_data()
        && !launcher_only
}

pub fn icon_kind(view: Option<&dyn TrayState>, bind_failed: bool, ipc_missing: bool) -> IconKind {
    if bind_failed {
        return IconKind::Error;
    }
    if ipc_missing {
        return IconKind::Warn;
    }
    if view.is_some_and(TrayState::game_running) {
        IconKind::Active
    } else {
        IconKind::Idle
    }
}

#[cfg(test)]
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

fn raster(rgba: [u8; 4], size: i32) -> Vec<u8> {
    const SAMPLES: i32 = 4;
    let mut out = vec![0u8; (size * size * 4) as usize];
    for y in 0..size {
        for x in 0..size {
            let mut hits = 0;
            for sy in 0..SAMPLES {
                for sx in 0..SAMPLES {
                    let fx = (f64::from(x) + (f64::from(sx) + 0.5) / f64::from(SAMPLES))
                        / f64::from(size);
                    let fy = (f64::from(y) + (f64::from(sy) + 0.5) / f64::from(SAMPLES))
                        / f64::from(size);
                    if covers(fx, fy) {
                        hits += 1;
                    }
                }
            }
            if hits == 0 {
                continue;
            }
            let a =
                (f64::from(rgba[3]) * f64::from(hits) / f64::from(SAMPLES * SAMPLES) + 0.5) as u8;
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
    if (0.29..=0.39).contains(&r) {
        return true;
    }
    if r > 0.39 && r <= 0.5 {
        return dx.abs() <= 0.045 || dy.abs() <= 0.045;
    }
    false
}

#[cfg(test)]
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

/// Shell icons are BGRA with premultiplied alpha. A full `.ico` wrapper is
/// the wrong payload for `CreateIconFromResourceEx` and yields a null HICON.
#[cfg(any(test, windows))]
fn premul_bgra(rgba: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(rgba.len());
    for px in rgba.as_chunks::<4>().0 {
        let a = u16::from(px[3]);
        out.push((u16::from(px[2]) * a / 255) as u8);
        out.push((u16::from(px[1]) * a / 255) as u8);
        out.push((u16::from(px[0]) * a / 255) as u8);
        out.push(px[3]);
    }
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
    use super::{
        ACTIVE, Arc, AtomicBool, ERROR, Handle, ICON_SIZE, IDLE, IconKind, Ordering, WARN,
        icon_kind, ipc_unconnected, menu_alert_from, raster, tooltip_with_presence, update_label,
        version_label,
    };
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
                    label: "Show keybinds".into(),
                    checked: self.handle.groups.shown("keybinds"),
                    activate: Box::new(|this: &mut HudTray| {
                        let _ = this.handle.toggle_group("keybinds");
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
                items.push(
                    StandardItem {
                        label: "Open log file".into(),
                        activate: Box::new(|this: &mut HudTray| this.handle.open_log()),
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
            items.push(
                StandardItem {
                    label: update_label(&self.handle.update_status()),
                    activate: Box::new(|this: &mut HudTray| this.handle.check_updates()),
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
        for px in data.as_chunks_mut::<4>().0 {
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
        let view = handle.store.tray_hint(chrono::Utc::now());
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
                    handle.ui.wait_timeout(Duration::from_secs(1));
                }
                svc.shutdown().wait();
            }
            Err(err) => {
                eprintln!(
                    "tray: StatusNotifierItem failed ({err}); HTTP remains the control hatch"
                );
                while !stop.load(Ordering::SeqCst) && !handle.stopped() {
                    handle.ui.wait_timeout(Duration::from_secs(1));
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
        NIF_ICON, NIF_MESSAGE, NIF_TIP, NIM_ADD, NIM_DELETE, NIM_MODIFY, NOTIFYICONDATAW,
        Shell_NotifyIconW,
    };
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        CS_HREDRAW, CS_VREDRAW, CW_USEDEFAULT, CreatePopupMenu, CreateWindowExW, DefWindowProcW,
        DestroyIcon, DestroyWindow, DispatchMessageW, GetCursorPos, IDC_ARROW, InsertMenuW,
        LoadCursorW, MF_CHECKED, MF_DISABLED, MF_GRAYED, MF_SEPARATOR, MF_STRING, MF_UNCHECKED,
        PeekMessageW, PostQuitMessage, RegisterClassExW, SetForegroundWindow, TPM_RIGHTBUTTON,
        TrackPopupMenu, TranslateMessage, WM_APP, WM_COMMAND, WM_DESTROY, WM_LBUTTONUP,
        WM_RBUTTONUP, WNDCLASSEXW, WS_OVERLAPPED,
    };

    const WM_TRAY: u32 = WM_APP + 1;
    const ID_OVERLAY: u16 = 1;
    const ID_CHALLENGES: u16 = 2;
    const ID_KEYBINDS: u16 = 5;
    const ID_FPS: u16 = 3;
    const ID_LAUNCHER: u16 = 4;
    const ID_XP: u16 = 20;
    const ID_RUN: u16 = 21;
    const ID_RETRY: u16 = 23;
    const ID_RELOAD: u16 = 24;
    const ID_STARTUP: u16 = 25;
    const ID_OPEN_CONFIG: u16 = 26;
    const ID_OPEN_LOG: u16 = 27;
    const ID_UPDATES: u16 = 28;
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
        unsafe {
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
            let icon = hicon(IconKind::Idle);
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
                    handle.ui.wait_timeout(Duration::from_millis(200));
                }
            }
            Shell_NotifyIconW(NIM_DELETE, &nid);
            if !icon.is_null() {
                DestroyIcon(icon);
            }
            DestroyWindow(hwnd);
            *CTX.lock().unwrap() = None;
        }
    }

    unsafe fn refresh() {
        unsafe {
            let mut g = CTX.lock().unwrap();
            let Some(ctx) = g.as_mut() else {
                return;
            };
            let view = ctx.handle.store.tray_hint(chrono::Utc::now());
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
            ctx.icon = hicon(kind);
            let mut nid: NOTIFYICONDATAW = zeroed();
            nid.cbSize = size_of::<NOTIFYICONDATAW>() as u32;
            nid.hWnd = ctx.hwnd;
            nid.uID = 1;
            nid.uFlags = NIF_ICON | NIF_TIP;
            nid.hIcon = ctx.icon;
            write_tip(&mut nid, &tip);
            Shell_NotifyIconW(NIM_MODIFY, &nid);
        }
    }

    unsafe extern "system" fn wndproc(hwnd: HWND, msg: u32, wp: WPARAM, lp: LPARAM) -> LRESULT {
        unsafe {
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
    }

    unsafe fn show_menu(hwnd: HWND) {
        unsafe {
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
            let keybinds = ctx.handle.groups.shown("keybinds");
            InsertMenuW(
                menu,
                u32::MAX,
                MF_STRING | if keybinds { MF_CHECKED } else { MF_UNCHECKED },
                ID_KEYBINDS as usize,
                wide("Show keybinds").as_ptr(),
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
                ID_OPEN_LOG as usize,
                wide("Open log file").as_ptr(),
            );
            InsertMenuW(
                menu,
                u32::MAX,
                MF_STRING,
                ID_RELOAD as usize,
                wide("Reload config").as_ptr(),
            );
            InsertMenuW(
                menu,
                u32::MAX,
                MF_STRING,
                ID_UPDATES as usize,
                wide(&update_label(&ctx.handle.update_status())).as_ptr(),
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
            ID_KEYBINDS => {
                let _ = h.toggle_group("keybinds");
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
            ID_OPEN_LOG => h.open_log(),
            ID_RELOAD => h.reload_config(),
            ID_UPDATES => h.check_updates(),
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

    fn hicon(kind: IconKind) -> windows_sys::Win32::UI::WindowsAndMessaging::HICON {
        use super::{ACTIVE, ERROR, IDLE, WARN, premul_bgra, raster};
        use windows_sys::Win32::Graphics::Gdi::{
            BI_RGB, BITMAPINFO, BITMAPINFOHEADER, CreateBitmap, CreateDIBSection, DIB_RGB_COLORS,
            DeleteObject,
        };
        use windows_sys::Win32::UI::WindowsAndMessaging::{
            CreateIconIndirect, GetSystemMetrics, ICONINFO, SM_CXSMICON,
        };

        let size = unsafe { GetSystemMetrics(SM_CXSMICON) }.max(16);
        let rgba = raster(
            match kind {
                IconKind::Active => ACTIVE,
                IconKind::Idle => IDLE,
                IconKind::Warn => WARN,
                IconKind::Error => ERROR,
            },
            size,
        );
        let bgra = premul_bgra(&rgba);
        let info = BITMAPINFO {
            bmiHeader: BITMAPINFOHEADER {
                biSize: size_of::<BITMAPINFOHEADER>() as u32,
                biWidth: size,
                biHeight: -size,
                biPlanes: 1,
                biBitCount: 32,
                biCompression: BI_RGB,
                ..unsafe { zeroed() }
            },
            bmiColors: [unsafe { zeroed() }],
        };
        let mut bits = ptr::null_mut();
        let color = unsafe {
            CreateDIBSection(
                ptr::null_mut(),
                &info,
                DIB_RGB_COLORS,
                &mut bits,
                ptr::null_mut(),
                0,
            )
        };
        if color.is_null() || bits.is_null() {
            eprintln!("tray: CreateDIBSection failed");
            return ptr::null_mut();
        }
        unsafe {
            ptr::copy_nonoverlapping(bgra.as_ptr(), bits.cast::<u8>(), bgra.len());
        }
        let mask = unsafe { CreateBitmap(size, size, 1, 1, ptr::null()) };
        let icon = unsafe {
            CreateIconIndirect(&ICONINFO {
                fIcon: 1,
                xHotspot: 0,
                yHotspot: 0,
                hbmMask: mask,
                hbmColor: color,
            })
        };
        unsafe {
            if !color.is_null() {
                DeleteObject(color);
            }
            if !mask.is_null() {
                DeleteObject(mask);
            }
        }
        if icon.is_null() {
            eprintln!("tray: CreateIconIndirect failed");
        }
        icon
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{Ns, View, Visibility};
    use std::time::Duration;

    #[test]
    fn update_labels_cover_every_state() {
        use crate::app::updates::Status;
        assert_eq!(update_label(&Status::Unchecked), "Check for updates");
        assert!(update_label(&Status::Checking).contains("Checking"));
        assert!(update_label(&Status::UpToDate).contains(env!("CARGO_PKG_VERSION")));
        assert_eq!(
            update_label(&Status::Newer("9.9.9".into())),
            "Update to 9.9.9…"
        );
        assert!(update_label(&Status::Failed).contains("log"));
    }

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
        assert!(
            menu_alert(Some(&expired), None, false, false)
                .unwrap()
                .contains("session expired")
        );
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
        assert!(
            tooltip(
                Some(&v),
                Visibility {
                    visible: true,
                    ..Visibility::default()
                }
            )
            .contains("session expired")
        );
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
            Some(
                "/home/me/.config/df-hud/config.toml: unknown key widgext (a typo here would otherwise be silently ignored; see df-hud.example.toml)",
            ),
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
                let mut buf = vec![0; reader.output_buffer_size().unwrap()];
                reader.next_frame(&mut buf).unwrap();
                let _ = buf;
            }
        }
        let data = icon_png(IconKind::Active, 64);
        let mut reader = png::Decoder::new(Cursor::new(&data)).read_info().unwrap();
        let mut buf = vec![0; reader.output_buffer_size().unwrap()];
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

    #[test]
    fn premul_bgra_keeps_opaque_ink_and_clears_holes() {
        let out = premul_bgra(&[0xe6, 0xcc, 0x4d, 0xff, 0xe6, 0xcc, 0x4d, 0x00]);
        assert_eq!(out, [0x4d, 0xcc, 0xe6, 0xff, 0, 0, 0, 0]);
        let half = premul_bgra(&[0x10, 0x20, 0x40, 0x80]);
        assert_eq!(half, [0x20, 0x10, 0x08, 0x80]);
    }
}
