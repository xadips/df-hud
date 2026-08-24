//! Compositor / Win32 placement.
//! Hyprland talks to the Unix socket, not the `hyprctl` binary.

#[cfg(any(test, target_os = "linux"))]
use serde::Deserialize;
#[cfg(unix)]
use std::path::Path;
#[cfg(any(test, target_os = "linux"))]
use std::path::PathBuf;
#[cfg(any(test, target_os = "linux"))]
use std::sync::Mutex;

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Placement {
    pub known: bool,
    pub class: String,
    pub title: String,
    pub address: String,
    pub workspace: i32,
    pub workspace_name: String,
    pub monitor: String,
    pub on_active_workspace: bool,
    pub foreground: bool,
    pub minimized: bool,
    pub foreground_rule: bool,
    pub matched_by: String,
    pub launcher_only: bool,
    pub launcher_address: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Match {
    pub class: String,
    pub ignore_titles: Vec<String>,
    pub launcher_title: String,
}

impl Match {
    pub fn is_launcher_dialog(&self, title: &str) -> bool {
        let want = self.launcher_title.trim().to_ascii_lowercase();
        !want.is_empty() && title.to_ascii_lowercase().contains(&want)
    }

    pub fn ignored(&self, title: &str) -> bool {
        let low = title.to_ascii_lowercase();
        self.ignore_titles.iter().any(|skip| {
            let skip = skip.trim().to_ascii_lowercase();
            !skip.is_empty() && low.contains(&skip)
        })
    }
}

pub fn normalize_class(s: &str) -> String {
    let s = s.trim().to_ascii_lowercase();
    s.strip_suffix(".exe").unwrap_or(&s).to_string()
}

pub trait Client: Send + Sync {
    fn game_window(&self, pid: i32, m: &Match) -> Result<Placement, String>;
    fn send_key(&self, key: &str, address: &str) -> Result<(), String>;
    fn active_address(&self) -> Option<String>;
}

#[cfg(test)]
pub fn can_start_run(place: &Placement) -> bool {
    #[cfg(windows)]
    {
        place.known && place.foreground
    }
    #[cfg(not(windows))]
    {
        let _ = place;
        false
    }
}

pub fn new_client() -> Box<dyn Client> {
    #[cfg(target_os = "linux")]
    {
        Box::new(HyprlandClient)
    }
    #[cfg(windows)]
    {
        Box::new(Win32Client)
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        Box::new(NullClient)
    }
}

#[cfg(not(any(target_os = "linux", windows)))]
struct NullClient;
#[cfg(not(any(target_os = "linux", windows)))]
impl Client for NullClient {
    fn game_window(&self, _: i32, _: &Match) -> Result<Placement, String> {
        Ok(Placement::default())
    }
    fn send_key(&self, _: &str, _: &str) -> Result<(), String> {
        Err("desktop send_key is not available on this OS".into())
    }
    fn active_address(&self) -> Option<String> {
        None
    }
}

#[cfg(any(test, target_os = "linux"))]
#[derive(Clone, Debug, Default, Deserialize)]
pub struct HyprWindow {
    #[serde(default)]
    pub class: String,
    #[serde(default, rename = "initialClass")]
    pub initial_class: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub address: String,
    #[serde(default)]
    pub pid: i32,
    #[serde(default)]
    pub monitor: i32,
    #[serde(default)]
    pub workspace: HyprWorkspace,
}

#[cfg(any(test, target_os = "linux"))]
#[derive(Clone, Debug, Default, Deserialize)]
pub struct HyprWorkspace {
    #[serde(default)]
    pub id: i32,
    #[serde(default)]
    pub name: String,
}

#[cfg(any(test, target_os = "linux"))]
#[derive(Clone, Debug, Default, Deserialize)]
pub struct HyprMonitor {
    #[serde(default)]
    pub id: i32,
    #[serde(default)]
    pub name: String,
    #[serde(default, rename = "activeWorkspace")]
    pub active_workspace: HyprWorkspace,
    #[serde(default, rename = "specialWorkspace")]
    pub special_workspace: HyprWorkspace,
}

#[cfg(any(test, target_os = "linux"))]
pub fn find_game_window(
    windows: &[HyprWindow],
    monitors: &[HyprMonitor],
    pid: i32,
    m: &Match,
) -> Placement {
    let by_id: std::collections::HashMap<i32, &HyprMonitor> =
        monitors.iter().map(|mon| (mon.id, mon)).collect();

    let place = |w: &HyprWindow, how: &str| {
        let mut p = Placement {
            known: true,
            class: w.class.clone(),
            title: w.title.clone(),
            address: w.address.clone(),
            workspace: w.workspace.id,
            workspace_name: w.workspace.name.clone(),
            matched_by: how.to_string(),
            ..Placement::default()
        };
        if let Some(mon) = by_id.get(&w.monitor) {
            p.monitor = mon.name.clone();
            p.on_active_workspace = if w.workspace.id < 0 {
                mon.special_workspace.id == w.workspace.id
            } else {
                mon.active_workspace.id == w.workspace.id
            };
        }
        p
    };

    let mut launcher = false;
    let mut launcher_addr = String::new();
    let mut note_launcher = |w: &HyprWindow| {
        launcher = true;
        if launcher_addr.is_empty() && m.is_launcher_dialog(&w.title) {
            launcher_addr = w.address.clone();
        }
    };
    let want = normalize_class(&m.class);

    if pid > 0 {
        for w in windows {
            if w.pid != pid {
                continue;
            }
            if m.ignored(&w.title) {
                note_launcher(w);
                continue;
            }
            return place(w, "process id");
        }
    }
    if !want.is_empty() {
        for w in windows {
            if normalize_class(&w.class) != want && normalize_class(&w.initial_class) != want {
                continue;
            }
            if m.ignored(&w.title) {
                note_launcher(w);
                continue;
            }
            return place(w, "window class");
        }
    }
    Placement {
        launcher_only: launcher,
        launcher_address: launcher_addr,
        ..Placement::default()
    }
}

#[cfg(any(test, target_os = "linux"))]
pub fn send_key_validate(key: &str, address: &str) -> Result<(), String> {
    if !hypr_key_ok(key) {
        return Err(format!("hyprland: {key:?} is not a plain key name"));
    }
    if !hypr_address_ok(address) {
        return Err(format!("hyprland: {address:?} is not a window address"));
    }
    Ok(())
}

#[cfg(any(test, target_os = "linux"))]
fn hypr_key_ok(key: &str) -> bool {
    !key.is_empty() && key.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
}

#[cfg(any(test, target_os = "linux"))]
fn hypr_address_ok(address: &str) -> bool {
    let rest = match address.strip_prefix("0x") {
        Some(r) => r,
        None => return false,
    };
    !rest.is_empty() && rest.chars().all(|c| c.is_ascii_hexdigit())
}

#[cfg(any(test, target_os = "linux"))]
static HYPR_DIRS_OVERRIDE: Mutex<Option<Vec<PathBuf>>> = Mutex::new(None);

#[cfg(test)]
pub fn set_hypr_dirs_for_testing(dirs: Option<Vec<PathBuf>>) {
    *HYPR_DIRS_OVERRIDE.lock().unwrap() = dirs;
}

#[cfg(any(test, target_os = "linux"))]
fn hypr_dirs() -> Vec<PathBuf> {
    if let Some(dirs) = HYPR_DIRS_OVERRIDE.lock().unwrap().clone() {
        return dirs;
    }
    let mut dirs = Vec::new();
    if let Ok(runtime) = std::env::var("XDG_RUNTIME_DIR") {
        if !runtime.is_empty() {
            dirs.push(PathBuf::from(runtime).join("hypr"));
        }
    }
    dirs.push(PathBuf::from("/tmp/hypr"));
    dirs
}

#[cfg(any(test, target_os = "linux"))]
pub fn hypr_socket_path(name: &str) -> Result<PathBuf, String> {
    let dirs = hypr_dirs();
    let mut candidates = Vec::new();
    if let Ok(sig) = std::env::var("HYPRLAND_INSTANCE_SIGNATURE") {
        if !sig.is_empty() {
            for dir in &dirs {
                candidates.push(dir.join(&sig).join(name));
            }
        }
    }
    for path in &candidates {
        if path.exists() {
            return Ok(path.clone());
        }
    }
    match lone_hypr_socket(&dirs, name) {
        Ok(Some(p)) => return Ok(p),
        Ok(None) => {}
        Err(e) => return Err(e),
    }
    if candidates.is_empty() {
        return Err(
            "HYPRLAND_INSTANCE_SIGNATURE is unset and no single running Hyprland instance was found"
                .into(),
        );
    }
    Err(format!(
        "no Hyprland socket {name} (looked in {})",
        candidates
            .iter()
            .map(|p| p.display().to_string())
            .collect::<Vec<_>>()
            .join(", ")
    ))
}

#[cfg(any(test, target_os = "linux"))]
fn lone_hypr_socket(dirs: &[PathBuf], name: &str) -> Result<Option<PathBuf>, String> {
    for dir in dirs {
        let Ok(entries) = std::fs::read_dir(dir) else {
            continue;
        };
        let mut hits = Vec::new();
        for e in entries.flatten() {
            if !e.file_type().map(|t| t.is_dir()).unwrap_or(false) {
                continue;
            }
            let candidate = e.path().join(name);
            if candidate.exists() {
                hits.push(candidate);
            }
        }
        match hits.len() {
            0 => continue,
            1 => return Ok(Some(hits.remove(0))),
            n => {
                return Err(format!(
                    "{n} Hyprland instances are running, so which one to ask is ambiguous"
                ))
            }
        }
    }
    Ok(None)
}

#[cfg(any(test, target_os = "linux"))]
pub fn hypr_command(cmd: &str) -> Result<Vec<u8>, String> {
    #[cfg(unix)]
    {
        hypr_command_at(&hypr_socket_path(".socket.sock")?, cmd)
    }
    #[cfg(not(unix))]
    {
        let _ = cmd;
        Err("hyprland socket is only available on Unix".into())
    }
}

#[cfg(unix)]
fn hypr_command_at(path: &Path, cmd: &str) -> Result<Vec<u8>, String> {
    use std::io::{Read, Write};
    use std::os::unix::net::UnixStream;
    use std::time::Duration;
    let mut stream = UnixStream::connect(path).map_err(|e| e.to_string())?;
    let _ = stream.set_read_timeout(Some(Duration::from_secs(2)));
    let _ = stream.set_write_timeout(Some(Duration::from_secs(2)));
    stream
        .write_all(cmd.as_bytes())
        .map_err(|e| e.to_string())?;
    let mut buf = Vec::new();
    stream.read_to_end(&mut buf).map_err(|e| e.to_string())?;
    Ok(buf)
}

#[cfg(unix)]
pub fn watch_events(
    stop: std::sync::Arc<std::sync::atomic::AtomicBool>,
    on_game: impl Fn(),
    on_place: impl Fn(),
) {
    let path = match hypr_socket_path(".socket2.sock") {
        Ok(p) => p,
        Err(e) => {
            eprintln!("game: no Hyprland event stream ({e}); falling back to periodic scans");
            return;
        }
    };
    let mut backoff = std::time::Duration::from_secs(1);
    while !stop.load(std::sync::atomic::Ordering::SeqCst) {
        match stream_hypr_events(&path, &stop, &on_game, &on_place) {
            Ok(()) => {}
            Err(e) => {
                if stop.load(std::sync::atomic::Ordering::SeqCst) {
                    return;
                }
                eprintln!("game: Hyprland event stream ended ({e}); retrying in {backoff:?}");
            }
        }
        if stop.load(std::sync::atomic::Ordering::SeqCst) {
            return;
        }
        std::thread::sleep(backoff);
        if backoff < std::time::Duration::from_secs(30) {
            backoff *= 2;
        }
    }
}

#[cfg(unix)]
fn stream_hypr_events(
    path: &Path,
    stop: &std::sync::atomic::AtomicBool,
    on_game: &impl Fn(),
    on_place: &impl Fn(),
) -> Result<(), String> {
    use std::io::{BufRead, BufReader};
    use std::os::unix::net::UnixStream;
    let stream = UnixStream::connect(path).map_err(|e| e.to_string())?;
    let _ = stream.set_read_timeout(Some(std::time::Duration::from_millis(500)));
    let reader = BufReader::new(stream);
    for line in reader.lines() {
        if stop.load(std::sync::atomic::Ordering::SeqCst) {
            return Ok(());
        }
        let Ok(line) = line else {
            continue;
        };
        let Some((name, _)) = line.split_once(">>") else {
            continue;
        };
        match name {
            "openwindow" | "closewindow" | "fullscreen" => {
                on_game();
                on_place();
            }
            "workspace" | "workspacev2" | "focusedmon" | "focusedmonv2" | "movewindow"
            | "movewindowv2" | "moveworkspace" | "moveworkspacev2" | "activespecial"
            | "monitoradded" | "monitorremoved" => on_place(),
            _ => {}
        }
    }
    Err("socket closed".into())
}

#[cfg(windows)]
pub fn watch_events(
    stop: std::sync::Arc<std::sync::atomic::AtomicBool>,
    on_game: impl Fn(),
    on_place: impl Fn(),
) {
    use windows_sys::Win32::UI::WindowsAndMessaging::GetForegroundWindow;
    let mut last = unsafe { GetForegroundWindow() };
    while !stop.load(std::sync::atomic::Ordering::SeqCst) {
        std::thread::sleep(std::time::Duration::from_millis(200));
        let next = unsafe { GetForegroundWindow() };
        if next != last {
            last = next;
            on_game();
            on_place();
        }
    }
}

#[cfg(not(any(unix, windows)))]
pub fn watch_events(_: std::sync::Arc<std::sync::atomic::AtomicBool>, _: impl Fn(), _: impl Fn()) {}

#[derive(Clone, Debug)]
pub struct ListedWindow {
    pub class: String,
    pub title: String,
    pub pid: i32,
}

pub fn listed_windows() -> Result<Vec<ListedWindow>, String> {
    #[cfg(target_os = "linux")]
    {
        let clients = hypr_command("j/clients")?;
        let windows: Vec<HyprWindow> = serde_json::from_slice(&clients)
            .map_err(|e| format!("hyprland: could not read the window list: {e}"))?;
        Ok(windows
            .into_iter()
            .map(|w| ListedWindow {
                class: if w.class.is_empty() {
                    w.initial_class
                } else {
                    w.class
                },
                title: w.title,
                pid: w.pid,
            })
            .collect())
    }
    #[cfg(windows)]
    {
        Ok(enumerate_windows()?
            .into_iter()
            .map(|w| ListedWindow {
                class: w.class,
                title: w.title,
                pid: w.pid as i32,
            })
            .collect())
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        Err("desktop window list is not available on this OS".into())
    }
}

#[cfg(target_os = "linux")]
pub struct HyprlandClient;

#[cfg(target_os = "linux")]
impl Client for HyprlandClient {
    fn game_window(&self, pid: i32, m: &Match) -> Result<Placement, String> {
        let clients = hypr_command("j/clients")?;
        let monitors = hypr_command("j/monitors")?;
        let windows: Vec<HyprWindow> = serde_json::from_slice(&clients)
            .map_err(|e| format!("hyprland: could not read the window list: {e}"))?;
        let monitors: Vec<HyprMonitor> = serde_json::from_slice(&monitors)
            .map_err(|e| format!("hyprland: could not read the monitor list: {e}"))?;
        Ok(find_game_window(&windows, &monitors, pid, m))
    }

    fn send_key(&self, key: &str, address: &str) -> Result<(), String> {
        send_key_validate(key, address)?;
        let cmd = format!(
            "dispatch hl.dsp.send_shortcut{{mods=\"\",key={key:?},window={:?}}}",
            format!("address:{address}")
        );
        let reply = hypr_command(&cmd)?;
        let text = String::from_utf8_lossy(&reply);
        let text = text.trim();
        if !text.starts_with("ok") {
            let line = text.split('\n').next().unwrap_or(text).trim();
            return Err(format!("hyprland: {line}"));
        }
        Ok(())
    }

    fn active_address(&self) -> Option<String> {
        let raw = hypr_command("j/activewindow").ok()?;
        #[derive(Deserialize)]
        struct W {
            #[serde(default)]
            address: String,
        }
        let w: W = serde_json::from_slice(&raw).ok()?;
        if w.address.is_empty() {
            None
        } else {
            Some(w.address)
        }
    }
}

#[cfg(any(test, windows))]
#[derive(Clone, Debug)]
pub struct WinWindow {
    pub handle: usize,
    pub class: String,
    pub title: String,
    pub pid: u32,
    pub minimized: bool,
    pub width: i32,
    pub height: i32,
}

#[cfg(any(test, windows))]
pub fn find_windows_game_window(
    windows: &[WinWindow],
    foreground: usize,
    pid: i32,
    m: &Match,
) -> Placement {
    let mut launcher = false;
    let mut launcher_addr = String::new();
    let mut note_launcher = |w: &WinWindow| {
        launcher = true;
        if launcher_addr.is_empty() && m.is_launcher_dialog(&w.title) {
            launcher_addr = format!("0x{:x}", w.handle);
        }
    };
    let place = |w: &WinWindow, how: &str| {
        let fg = w.handle != 0 && w.handle == foreground;
        Placement {
            known: true,
            class: w.class.clone(),
            title: w.title.clone(),
            address: format!("0x{:x}", w.handle),
            on_active_workspace: !w.minimized,
            foreground: fg,
            minimized: w.minimized,
            foreground_rule: true,
            matched_by: how.to_string(),
            ..Placement::default()
        }
    };
    let mut best = |pred: &dyn Fn(&WinWindow) -> bool| {
        let mut best_i: Option<usize> = None;
        let mut best_area = 0i32;
        for (i, w) in windows.iter().enumerate() {
            if !pred(w) {
                continue;
            }
            if m.ignored(&w.title) {
                note_launcher(w);
                continue;
            }
            if !windows_game_candidate(w) {
                continue;
            }
            let area = w.width.saturating_mul(w.height);
            if best_i.is_none() || area > best_area {
                best_i = Some(i);
                best_area = area;
            }
        }
        best_i.map(|i| &windows[i])
    };
    if pid > 0 {
        if let Some(w) = best(&|w| w.pid == pid as u32) {
            return place(w, "process id");
        }
    }
    let want = normalize_class(&m.class);
    if !want.is_empty() {
        if let Some(w) = best(&|w| normalize_class(&w.class) == want) {
            return place(w, "window class");
        }
    }
    Placement {
        launcher_only: launcher,
        launcher_address: launcher_addr,
        ..Placement::default()
    }
}

#[cfg(any(test, windows))]
fn windows_game_candidate(w: &WinWindow) -> bool {
    if w.class.eq_ignore_ascii_case("ComboLBox") {
        return false;
    }
    !w.title.trim().is_empty() && w.width >= 320 && w.height >= 200
}

#[cfg(any(test, windows))]
pub fn windows_virtual_key(key: &str) -> Option<u16> {
    let upper = key.trim().to_ascii_uppercase();
    if upper.len() == 1 {
        let c = upper.as_bytes()[0];
        if c.is_ascii_alphanumeric() {
            return Some(c as u16);
        }
    }
    if let Some(rest) = upper.strip_prefix('F') {
        if let Ok(n) = rest.parse::<u16>() {
            if (1..=24).contains(&n) {
                return Some(0x70 + n - 1);
            }
        }
    }
    Some(match upper.as_str() {
        "BACKSPACE" => 0x08,
        "TAB" => 0x09,
        "RETURN" | "ENTER" => 0x0D,
        "SHIFT" => 0x10,
        "CONTROL" | "CTRL" => 0x11,
        "ALT" => 0x12,
        "ESCAPE" | "ESC" => 0x1B,
        "SPACE" => 0x20,
        "PAGEUP" => 0x21,
        "PAGEDOWN" => 0x22,
        "END" => 0x23,
        "HOME" => 0x24,
        "LEFT" => 0x25,
        "UP" => 0x26,
        "RIGHT" => 0x27,
        "DOWN" => 0x28,
        "INSERT" => 0x2D,
        "DELETE" => 0x2E,
        _ => return None,
    })
}

#[cfg(windows)]
pub struct Win32Client;

#[cfg(windows)]
impl Client for Win32Client {
    fn game_window(&self, pid: i32, m: &Match) -> Result<Placement, String> {
        let list = enumerate_windows()?;
        let fg = unsafe { windows_sys::Win32::UI::WindowsAndMessaging::GetForegroundWindow() };
        Ok(find_windows_game_window(&list, fg as usize, pid, m))
    }

    fn send_key(&self, key: &str, address: &str) -> Result<(), String> {
        let hwnd = parse_windows_address(address)?;
        unsafe {
            use windows_sys::Win32::UI::WindowsAndMessaging::{GetForegroundWindow, IsWindow};
            if IsWindow(hwnd) == 0 {
                return Err(format!(
                    "windows: target window {address:?} no longer exists"
                ));
            }
            if GetForegroundWindow() != hwnd {
                return Err(format!(
                    "windows: target window {address:?} is not in the foreground"
                ));
            }
        }
        let vk = windows_virtual_key(key)
            .ok_or_else(|| format!("windows: unsupported key name {key:?}"))?;
        unsafe {
            windows_sys::Win32::UI::Input::KeyboardAndMouse::keybd_event(vk as u8, 0, 0, 0);
            windows_sys::Win32::UI::Input::KeyboardAndMouse::keybd_event(
                vk as u8,
                0,
                windows_sys::Win32::UI::Input::KeyboardAndMouse::KEYEVENTF_KEYUP,
                0,
            );
        }
        Ok(())
    }

    fn active_address(&self) -> Option<String> {
        let hwnd = unsafe { windows_sys::Win32::UI::WindowsAndMessaging::GetForegroundWindow() };
        if hwnd.is_null() {
            None
        } else {
            Some(format!("0x{:x}", hwnd as usize))
        }
    }
}

#[cfg(windows)]
fn parse_windows_address(address: &str) -> Result<windows_sys::Win32::Foundation::HWND, String> {
    let rest = address
        .strip_prefix("0x")
        .ok_or_else(|| format!("windows: {address:?} is not a window handle"))?;
    let value = usize::from_str_radix(rest, 16)
        .map_err(|_| format!("windows: {address:?} is not a window handle"))?;
    if value == 0 {
        return Err(format!("windows: {address:?} is not a window handle"));
    }
    Ok(value as windows_sys::Win32::Foundation::HWND)
}

#[cfg(windows)]
fn enumerate_windows() -> Result<Vec<WinWindow>, String> {
    use std::mem::size_of;
    use windows_sys::core::BOOL;
    use windows_sys::Win32::Foundation::{HWND, LPARAM, RECT};
    use windows_sys::Win32::Graphics::Gdi::{GetMonitorInfoW, MonitorFromWindow, MONITORINFOEXW};
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        EnumWindows, GetClassNameW, GetWindowRect, GetWindowTextLengthW, GetWindowTextW,
        GetWindowThreadProcessId, IsIconic, IsWindowVisible,
    };

    struct State {
        windows: Vec<WinWindow>,
    }
    unsafe extern "system" fn cb(hwnd: HWND, lp: LPARAM) -> BOOL {
        let state = &mut *(lp as *mut State);
        if IsWindowVisible(hwnd) == 0 {
            return 1;
        }
        let mut pid = 0u32;
        GetWindowThreadProcessId(hwnd, &mut pid);
        if pid == 0 {
            return 1;
        }
        let mut rect = RECT {
            left: 0,
            top: 0,
            right: 0,
            bottom: 0,
        };
        GetWindowRect(hwnd, &mut rect);
        let mut class = [0u16; 256];
        let n = GetClassNameW(hwnd, class.as_mut_ptr(), class.len() as i32);
        let class = String::from_utf16_lossy(&class[..n.max(0) as usize]);
        let len = GetWindowTextLengthW(hwnd);
        let title = if len <= 0 {
            String::new()
        } else {
            let mut buf = vec![0u16; len as usize + 1];
            let n = GetWindowTextW(hwnd, buf.as_mut_ptr(), buf.len() as i32);
            String::from_utf16_lossy(&buf[..n.max(0) as usize])
        };
        state.windows.push(WinWindow {
            handle: hwnd as usize,
            class,
            title,
            pid,
            minimized: IsIconic(hwnd) != 0,
            width: rect.right - rect.left,
            height: rect.bottom - rect.top,
        });
        let _ = (
            GetMonitorInfoW,
            MonitorFromWindow,
            size_of::<MONITORINFOEXW>(),
        );
        1
    }
    let mut state = State {
        windows: Vec::new(),
    };
    unsafe {
        if EnumWindows(Some(cb), &mut state as *mut State as LPARAM) == 0 {
            return Err("EnumWindows failed".into());
        }
    }
    Ok(state.windows)
}

#[cfg(test)]
mod tests {
    use super::*;

    const HYPR_CLIENTS: &str = r#"[
  {"class":"kitty","initialClass":"kitty","title":"~","pid":2695,"monitor":0,
   "mapped":true,"workspace":{"id":2,"name":"2"},"visible":true},
  {"class":"deadfrontier.exe","initialClass":"deadfrontier.exe","title":"Dead Frontier",
   "pid":77863,"monitor":1,"mapped":true,"workspace":{"id":13,"name":"13"},"visible":true},
  {"class":"firefox","initialClass":"firefox","title":"Dead Frontier - Inner City",
   "pid":1607,"monitor":0,"mapped":true,"workspace":{"id":1,"name":"1"},"visible":true}
]"#;

    const HYPR_MONITORS: &str = r#"[
  {"id":0,"name":"DP-1","focused":false,
   "activeWorkspace":{"id":1,"name":"1"},"specialWorkspace":{"id":0,"name":""}},
  {"id":1,"name":"DP-2","focused":true,
   "activeWorkspace":{"id":13,"name":"13"},"specialWorkspace":{"id":0,"name":""}}
]"#;

    fn fixtures() -> (Vec<HyprWindow>, Vec<HyprMonitor>) {
        (
            serde_json::from_str(HYPR_CLIENTS).unwrap(),
            serde_json::from_str(HYPR_MONITORS).unwrap(),
        )
    }

    #[test]
    fn find_game_window_by_pid() {
        let (windows, monitors) = fixtures();
        let got = find_game_window(
            &windows,
            &monitors,
            77863,
            &Match {
                class: "DeadFrontier.exe".into(),
                ..Match::default()
            },
        );
        assert!(got.known);
        assert_eq!(got.matched_by, "process id");
        assert_eq!(got.monitor, "DP-2");
        assert_eq!(got.workspace_name, "13");
        assert!(got.on_active_workspace);
    }

    #[test]
    fn find_game_window_by_class_when_pid_is_wrong() {
        let (windows, monitors) = fixtures();
        let got = find_game_window(
            &windows,
            &monitors,
            999999,
            &Match {
                class: "DeadFrontier.exe".into(),
                ..Match::default()
            },
        );
        assert!(got.known);
        assert_eq!(got.matched_by, "window class");
        assert_eq!(got.class, "deadfrontier.exe");
    }

    #[test]
    fn find_game_window_on_an_inactive_workspace() {
        let (windows, monitors) = fixtures();
        let got = find_game_window(&windows, &monitors, 2695, &Match::default());
        assert!(got.known);
        assert!(!got.on_active_workspace);
    }

    #[test]
    fn find_game_window_never_matches_on_title() {
        let (windows, monitors) = fixtures();
        let got = find_game_window(
            &windows,
            &monitors,
            0,
            &Match {
                class: "SomethingElse.exe".into(),
                ..Match::default()
            },
        );
        assert!(!got.known);
    }

    fn ws(id: i32) -> HyprWorkspace {
        HyprWorkspace {
            id,
            name: id.to_string(),
        }
    }

    #[test]
    fn find_game_window_skips_the_launcher() {
        let launcher = vec![
            HyprWindow {
                class: "deadfrontier.exe".into(),
                title: "Dead Frontier Configuration".into(),
                pid: 4242,
                workspace: ws(3),
                ..HyprWindow::default()
            },
            HyprWindow {
                class: "deadfrontier.exe".into(),
                title: "Input Configuration".into(),
                pid: 4242,
                workspace: ws(3),
                ..HyprWindow::default()
            },
        ];
        let monitors = vec![HyprMonitor {
            id: 0,
            name: "DP-1".into(),
            ..HyprMonitor::default()
        }];
        let m = Match {
            class: "DeadFrontier.exe".into(),
            ignore_titles: vec!["configuration".into()],
            ..Match::default()
        };
        assert!(!find_game_window(&launcher, &monitors, 4242, &m).known);
        assert!(!find_game_window(&launcher, &monitors, 0, &m).known);
        let mut game = launcher.clone();
        game.push(HyprWindow {
            class: "deadfrontier.exe".into(),
            title: "Dead Frontier".into(),
            pid: 4242,
            workspace: ws(3),
            ..HyprWindow::default()
        });
        let got = find_game_window(&game, &monitors, 4242, &m);
        assert!(got.known);
        assert_eq!(got.title, "Dead Frontier");
        assert!(
            find_game_window(
                &launcher,
                &monitors,
                4242,
                &Match {
                    class: "DeadFrontier.exe".into(),
                    ..Match::default()
                }
            )
            .known
        );
    }

    #[test]
    fn find_game_window_reports_launcher_only() {
        let monitors = vec![HyprMonitor {
            id: 0,
            name: "DP-1".into(),
            ..HyprMonitor::default()
        }];
        let m = Match {
            class: "DeadFrontier.exe".into(),
            ignore_titles: vec!["configuration".into()],
            ..Match::default()
        };
        let got = find_game_window(
            &[HyprWindow {
                class: "deadfrontier.exe".into(),
                title: "Dead Frontier Configuration".into(),
                pid: 7,
                workspace: ws(3),
                ..HyprWindow::default()
            }],
            &monitors,
            7,
            &m,
        );
        assert!(!got.known && got.launcher_only);
        let got = find_game_window(
            &[HyprWindow {
                class: "deadfrontier.exe".into(),
                title: "Input Configuration".into(),
                pid: 999,
                workspace: ws(3),
                ..HyprWindow::default()
            }],
            &monitors,
            7,
            &m,
        );
        assert!(!got.known && got.launcher_only);
        let got = find_game_window(
            &[HyprWindow {
                class: "kitty".into(),
                title: "~".into(),
                pid: 12,
                workspace: ws(3),
                ..HyprWindow::default()
            }],
            &monitors,
            7,
            &m,
        );
        assert!(!got.known && !got.launcher_only);
        let got = find_game_window(
            &[
                HyprWindow {
                    class: "deadfrontier.exe".into(),
                    title: "Dead Frontier Configuration".into(),
                    pid: 7,
                    workspace: ws(3),
                    ..HyprWindow::default()
                },
                HyprWindow {
                    class: "deadfrontier.exe".into(),
                    title: "Dead Frontier".into(),
                    pid: 7,
                    workspace: ws(3),
                    ..HyprWindow::default()
                },
            ],
            &monitors,
            7,
            &m,
        );
        assert!(got.known && !got.launcher_only);
    }

    #[test]
    fn find_game_window_handles_special_workspaces() {
        let windows = vec![HyprWindow {
            class: "deadfrontier.exe".into(),
            pid: 5,
            monitor: 0,
            workspace: HyprWorkspace {
                id: -98,
                name: "special:magic".into(),
            },
            ..HyprWindow::default()
        }];
        let mut monitors = vec![HyprMonitor {
            id: 0,
            name: "DP-1".into(),
            active_workspace: HyprWorkspace {
                id: 1,
                name: "1".into(),
            },
            special_workspace: HyprWorkspace {
                id: -98,
                name: "special:magic".into(),
            },
            ..HyprMonitor::default()
        }];
        assert!(find_game_window(&windows, &monitors, 5, &Match::default()).on_active_workspace);
        monitors[0].special_workspace.id = 0;
        assert!(!find_game_window(&windows, &monitors, 5, &Match::default()).on_active_workspace);
    }

    #[test]
    fn find_game_window_unknown_monitor() {
        let (windows, _) = fixtures();
        let got = find_game_window(&windows, &[], 77863, &Match::default());
        assert!(got.known);
        assert!(got.monitor.is_empty());
    }

    #[test]
    fn normalize_class_strips_exe() {
        for (input, want) in [
            ("DeadFrontier.exe", "deadfrontier"),
            ("deadfrontier.exe", "deadfrontier"),
            (" DeadFrontier.EXE ", "deadfrontier"),
            ("", ""),
        ] {
            assert_eq!(normalize_class(input), want);
        }
    }

    #[test]
    fn send_key_refuses_values_it_would_have_to_escape() {
        for (key, address) in [
            (r#"y","x"] --"#, "0xabc"),
            ("y y", "0xabc"),
            ("", "0xabc"),
            ("y", "abc"),
            ("y", r#"0xabc""#),
            ("y", ""),
        ] {
            assert!(
                send_key_validate(key, address).is_err(),
                "accepted key={key:?} address={address:?}"
            );
        }
    }

    #[test]
    fn hypr_socket_path_signature_and_ambiguity() {
        let root = std::env::temp_dir().join(format!(
            "df-hud-hypr-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&root).unwrap();
        set_hypr_dirs_for_testing(Some(vec![root.clone()]));
        let instance = |sig: &str| {
            let dir = root.join(sig);
            std::fs::create_dir_all(&dir).unwrap();
            let path = dir.join(".socket2.sock");
            std::fs::write(&path, []).unwrap();
            path
        };
        std::env::set_var("HYPRLAND_INSTANCE_SIGNATURE", "");
        assert!(hypr_socket_path(".socket2.sock").is_err());
        let want = instance("real-signature");
        std::env::set_var("HYPRLAND_INSTANCE_SIGNATURE", "real-signature");
        assert_eq!(hypr_socket_path(".socket2.sock").unwrap(), want);
        std::env::set_var("HYPRLAND_INSTANCE_SIGNATURE", "last-time-i-was-started");
        assert_eq!(hypr_socket_path(".socket2.sock").unwrap(), want);
        instance("a-second-compositor");
        assert!(hypr_socket_path(".socket2.sock").is_err());
        std::env::set_var("HYPRLAND_INSTANCE_SIGNATURE", "a-second-compositor");
        assert!(hypr_socket_path(".socket2.sock").is_ok());
        set_hypr_dirs_for_testing(None);
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn find_windows_game_window_tracks_foreground() {
        let mut windows = vec![
            WinWindow {
                handle: 0x10,
                class: "Other".into(),
                title: "Other".into(),
                pid: 10,
                minimized: false,
                width: 800,
                height: 600,
            },
            WinWindow {
                handle: 0x20,
                class: "UnityWndClass".into(),
                title: "Dead Frontier".into(),
                pid: 42,
                minimized: false,
                width: 2560,
                height: 1440,
            },
        ];
        let got = find_windows_game_window(&windows, 0x20, 42, &Match::default());
        assert!(got.known && got.foreground_rule && got.on_active_workspace && got.foreground);
        assert_eq!(got.address, "0x20");
        assert_eq!(got.matched_by, "process id");
        let got = find_windows_game_window(&windows, 0x10, 42, &Match::default());
        assert!(got.on_active_workspace);
        assert!(!can_start_run(&got) || cfg!(windows) && !got.foreground);
        windows[1].minimized = true;
        let got = find_windows_game_window(&windows, 0x10, 42, &Match::default());
        assert!(!got.on_active_workspace && got.minimized);
    }

    #[test]
    fn find_windows_game_window_reports_launcher() {
        let windows = vec![
            WinWindow {
                handle: 0x30,
                class: "DeadFrontier.exe".into(),
                title: "Dead Frontier Configuration".into(),
                pid: 42,
                minimized: false,
                width: 640,
                height: 480,
            },
            WinWindow {
                handle: 0x31,
                class: "ComboLBox".into(),
                title: "Resolution".into(),
                pid: 42,
                minimized: false,
                width: 180,
                height: 120,
            },
        ];
        let m = Match {
            class: "DeadFrontier.exe".into(),
            ignore_titles: vec!["configuration".into()],
            launcher_title: "Dead Frontier Configuration".into(),
        };
        let got = find_windows_game_window(&windows, 0x30, 42, &m);
        assert!(!got.known && got.launcher_only);
        assert_eq!(got.launcher_address, "0x30");
    }

    #[test]
    fn find_windows_game_window_ignores_launcher_popup_controls() {
        let mut windows = vec![
            WinWindow {
                handle: 0x30,
                class: "DeadFrontier.exe".into(),
                title: "Dead Frontier Configuration".into(),
                pid: 42,
                minimized: false,
                width: 640,
                height: 480,
            },
            WinWindow {
                handle: 0x31,
                class: "ComboLBox".into(),
                title: "Resolution".into(),
                pid: 42,
                minimized: false,
                width: 180,
                height: 120,
            },
        ];
        let m = Match {
            ignore_titles: vec!["configuration".into()],
            ..Match::default()
        };
        let got = find_windows_game_window(&windows, 0x31, 42, &m);
        assert!(!got.known && got.launcher_only);
        windows.push(WinWindow {
            handle: 0x32,
            class: "UnityWndClass".into(),
            title: "Dead Frontier".into(),
            pid: 42,
            minimized: false,
            width: 2560,
            height: 1440,
        });
        let got = find_windows_game_window(&windows, 0x32, 42, &m);
        assert!(got.known);
        assert_eq!(got.class, "UnityWndClass");
    }

    #[test]
    fn windows_virtual_key_names() {
        assert_eq!(windows_virtual_key("y"), Some(0x59));
        assert_eq!(windows_virtual_key("Return"), Some(0x0D));
        assert_eq!(windows_virtual_key("F12"), Some(0x7B));
        assert_eq!(windows_virtual_key("Space"), Some(0x20));
        assert!(windows_virtual_key("not_a_real_key").is_none());
    }
}
