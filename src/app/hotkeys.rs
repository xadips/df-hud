//! Hotkey chords for config validation. Names match Win32 virtual keys.

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use super::groups::Group;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Binding {
    pub modifiers: u32,
    pub virtual_key: u32,
    pub key: String,
}

pub const MOD_ALT: u32 = 0x0001;
pub const MOD_CONTROL: u32 = 0x0002;
pub const MOD_SHIFT: u32 = 0x0004;
pub const MOD_WIN: u32 = 0x0008;

pub fn parse_binding(raw: &str) -> Result<Binding, String> {
    let raw = raw.trim();
    if raw.is_empty() {
        return Err("binding is empty".into());
    }
    let parts: Vec<&str> = raw.split('+').collect();
    let mut binding = Binding {
        modifiers: 0,
        virtual_key: 0,
        key: String::new(),
    };
    for (i, part) in parts.iter().enumerate() {
        let part = part.trim();
        if part.is_empty() {
            return Err(format!("{raw:?} contains an empty key name"));
        }
        let last = i + 1 == parts.len();
        if let Some(modifier) = parse_modifier(part) {
            if last {
                return Err(format!("{raw:?} ends with a modifier instead of a key"));
            }
            if binding.modifiers & modifier != 0 {
                return Err(format!("{raw:?} repeats modifier {part:?}"));
            }
            binding.modifiers |= modifier;
            continue;
        }
        if !last {
            return Err(format!(
                "{raw:?} has non-modifier {part:?} before the final key"
            ));
        }
        let (key, vk) = parse_virtual_key(part)
            .ok_or_else(|| format!("{raw:?} has unsupported key {part:?}"))?;
        binding.key = key;
        binding.virtual_key = vk;
    }
    if binding.key.is_empty() {
        return Err(format!("{raw:?} has no key"));
    }
    Ok(binding)
}

impl Binding {
    /// Canonical chord as written in config.toml.
    pub fn canonical(&self) -> String {
        let mut parts = Vec::new();
        if self.modifiers & MOD_CONTROL != 0 {
            parts.push("Ctrl");
        }
        if self.modifiers & MOD_ALT != 0 {
            parts.push("Alt");
        }
        if self.modifiers & MOD_SHIFT != 0 {
            parts.push("Shift");
        }
        if self.modifiers & MOD_WIN != 0 {
            parts.push("Win");
        }
        parts.push(self.key.as_str());
        parts.join("+")
    }

    pub fn is_wasd(&self) -> bool {
        matches!(self.key.as_str(), "W" | "A" | "S" | "D")
    }
}

pub fn spawn(handle: Arc<crate::app::Handle>, stop: Arc<AtomicBool>) {
    if let Err(err) = std::thread::Builder::new()
        .name("df-hud-hotkeys".into())
        .spawn(move || run(handle, stop))
    {
        warn!("hotkeys: {err}; HTTP remains the control hatch");
    }
}

fn run(handle: Arc<crate::app::Handle>, stop: Arc<AtomicBool>) {
    #[cfg(windows)]
    windows::run(handle, stop);
    #[cfg(target_os = "linux")]
    linux::run(handle, stop);
    #[cfg(not(any(windows, target_os = "linux")))]
    {
        let _ = (handle, stop);
    }
}

/// What a chord does. The name is the `[hotkeys]` key it is configured
/// under, and on Linux it travels through the Hyprland bind script and back
/// up the fifo as text, so [`Action::parse`] is the inverse of
/// [`Action::as_str`].
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Action {
    Toggle(Group),
    RestartRun,
    ResetXp,
    ToggleOverlay,
}

impl Action {
    fn as_str(self) -> &'static str {
        match self {
            Action::Toggle(g) => g.as_str(),
            Action::RestartRun => "run",
            Action::ResetXp => "xp",
            Action::ToggleOverlay => "overlay",
        }
    }

    /// Only the fifo line on Linux is text; Win32 hands back the slot id.
    #[cfg(any(target_os = "linux", test))]
    fn parse(name: &str) -> Option<Self> {
        Some(match name {
            "run" => Action::RestartRun,
            "xp" => Action::ResetXp,
            "overlay" => Action::ToggleOverlay,
            group => match group.parse::<Group>().ok()? {
                g @ (Group::Map | Group::Challenges | Group::Masteries) => Action::Toggle(g),
                _ => return None,
            },
        })
    }
}

struct Slot {
    #[cfg(windows)]
    id: i32,
    action: Action,
    binding: Binding,
}

/// `masteries_widget` is `widget.masteries.enabled`: with the widget off the
/// key stays with the game instead of being eaten for a no-op.
fn slots_from_cfg(cfg: &crate::config::Hotkeys, masteries_widget: bool) -> Vec<Slot> {
    let specs = [
        (1, Action::Toggle(Group::Map), cfg.map.as_str()),
        (
            2,
            Action::Toggle(Group::Challenges),
            cfg.challenges.as_str(),
        ),
        (3, Action::RestartRun, cfg.run_start.as_str()),
        (4, Action::ResetXp, cfg.xp_reset.as_str()),
        (5, Action::ToggleOverlay, cfg.overlay.as_str()),
        (6, Action::Toggle(Group::Masteries), cfg.masteries.as_str()),
    ];
    let mut out = Vec::new();
    for (_id, action, raw) in specs {
        if raw.trim().is_empty()
            || (action == Action::Toggle(Group::Masteries) && !masteries_widget)
        {
            continue;
        }
        match parse_binding(raw) {
            Ok(binding) if binding.is_wasd() => {
                warn!("hotkeys: refusing to bind WASD for {}", action.as_str());
            }
            Ok(binding) => out.push(Slot {
                #[cfg(windows)]
                id: _id,
                action,
                binding,
            }),
            Err(err) => warn!("hotkeys: skipping {}: {err}", action.as_str()),
        }
    }
    out
}

fn fire(handle: &crate::app::Handle, action: Action) {
    match action {
        Action::Toggle(g) => {
            handle.toggle_group(g);
        }
        Action::RestartRun => handle.restart_run(),
        Action::ResetXp => handle.reset_xp(),
        Action::ToggleOverlay => {
            handle.toggle_overlay();
        }
    }
}

fn game_focused(handle: &crate::app::Handle) -> bool {
    focus_matches(
        handle.game_running.load(Ordering::SeqCst),
        &handle.vis.placement(),
        handle.active_address().as_deref(),
    )
}

fn focus_matches(
    game_running: bool,
    place: &crate::game::desktop::Placement,
    active: Option<&str>,
) -> bool {
    if !game_running {
        return false;
    }
    if !place.known || place.address.is_empty() {
        return false;
    }
    active == Some(place.address.as_str())
}

#[cfg(target_os = "linux")]
mod linux {
    use super::{
        Action, Arc, AtomicBool, Binding, MOD_ALT, MOD_CONTROL, MOD_SHIFT, MOD_WIN, Ordering, Slot,
        fire, game_focused, slots_from_cfg,
    };
    use std::fs::{self, File, OpenOptions};
    use std::io::Read;
    use std::os::fd::AsRawFd;
    use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
    use std::path::PathBuf;
    use std::time::Duration;

    pub fn run(handle: Arc<crate::app::Handle>, stop: Arc<AtomicBool>) {
        let runtime = std::env::var("XDG_RUNTIME_DIR").unwrap_or_else(|_| "/tmp".into());
        let fifo = PathBuf::from(&runtime).join(format!("df-hud-hk.{}", std::process::id()));
        let script = PathBuf::from(&runtime).join(format!("df-hud-hk-fire.{}", std::process::id()));
        let _ = fs::remove_file(&fifo);
        let path_c = std::ffi::CString::new(fifo.to_string_lossy().as_bytes()).ok();
        if let Some(c) = path_c {
            // SAFETY: `c` is a NUL-terminated path that outlives the call.
            unsafe { libc::mkfifo(c.as_ptr(), 0o600) };
        }
        let fifo_s = fifo.display().to_string();
        let body = format!("#!/bin/sh\nprintf '%s\\n' \"$1\" >> '{fifo_s}'\n");
        if fs::write(&script, body).is_ok() {
            let _ = fs::set_permissions(&script, fs::Permissions::from_mode(0o755));
        }
        let mut fifo_file = OpenOptions::new()
            .read(true)
            .write(true)
            .custom_flags(libc::O_NONBLOCK)
            .open(&fifo)
            .ok();
        let mut bound: Vec<String> = Vec::new();
        let mut last_key = String::new();
        let mut last_focus = false;
        let mut bind_failed = false;
        let mut leftover = String::new();
        let hypr = hyprland_socket_present();
        if !hypr {
            warn!("hotkeys: no Hyprland socket; HTTP remains the control hatch");
        }
        while !stop.load(Ordering::SeqCst) && !handle.stopped() {
            let running = handle.config();
            let cfg = &running.hotkeys;
            let slots = if cfg.enabled {
                slots_from_cfg(cfg, running.widget.masteries.enabled)
            } else {
                Vec::new()
            };
            let key = slots
                .iter()
                .map(|s| format!("{}:{}", s.action.as_str(), s.binding.canonical()))
                .collect::<Vec<_>>()
                .join("|");
            let focused = cfg.enabled && game_focused(&handle);
            if key != last_key || focused != last_focus {
                unbind_all(&mut bound);
                if focused && hypr {
                    if let Err(err) = bind_all(&slots, &script, &mut bound) {
                        if !bind_failed {
                            warn!(
                                "hotkeys: Hyprland bind failed ({err}); HTTP remains the control hatch"
                            );
                            bind_failed = true;
                        }
                    } else {
                        bind_failed = false;
                        if !bound.is_empty() {
                            info!("hotkeys: Hyprland armed {}", bound.join(" "));
                        }
                    }
                }
                last_key = key;
                last_focus = focused;
            }
            if let Some(f) = fifo_file.as_mut() {
                let mut buf = [0u8; 256];
                match f.read(&mut buf) {
                    Ok(n) if n > 0 => {
                        leftover.push_str(&String::from_utf8_lossy(&buf[..n]));
                        while let Some(i) = leftover.find('\n') {
                            let line = leftover[..i].trim().to_string();
                            leftover = leftover[i + 1..].to_string();
                            if focused && let Some(action) = Action::parse(&line) {
                                fire(&handle, action);
                            }
                        }
                    }
                    _ => {}
                }
            }
            wait_fifo(fifo_file.as_ref());
        }
        unbind_all(&mut bound);
        let _ = fs::remove_file(&fifo);
        let _ = fs::remove_file(&script);
        let _ = leftover;
    }

    fn wait_fifo(fifo: Option<&File>) {
        match fifo {
            Some(f) => {
                let mut pfd = libc::pollfd {
                    fd: f.as_raw_fd(),
                    events: libc::POLLIN,
                    revents: 0,
                };
                // SAFETY: `pfd` is one live pollfd and nfds is 1; `f` keeps the fd open.
                unsafe {
                    libc::poll(&mut pfd, 1, 1000);
                }
            }
            None => std::thread::sleep(Duration::from_secs(1)),
        }
    }

    fn hypr_key(b: &Binding) -> String {
        match b.key.as_str() {
            "Grave" => "grave".into(),
            "Escape" => "escape".into(),
            "Space" => "space".into(),
            "Tab" => "tab".into(),
            "Enter" => "return".into(),
            "Delete" => "delete".into(),
            other if other.len() == 1 => other.to_ascii_lowercase(),
            other => other.to_ascii_lowercase(),
        }
    }

    /// Chord as `hl.bind` expects it. Lua-config Hyprland rejects `keyword bind`.
    fn hypr_lua_keys(b: &Binding) -> String {
        let mut parts = Vec::new();
        if b.modifiers & MOD_CONTROL != 0 {
            parts.push("CTRL".to_string());
        }
        if b.modifiers & MOD_ALT != 0 {
            parts.push("ALT".to_string());
        }
        if b.modifiers & MOD_SHIFT != 0 {
            parts.push("SHIFT".to_string());
        }
        if b.modifiers & MOD_WIN != 0 {
            parts.push("SUPER".to_string());
        }
        parts.push(hypr_key(b));
        parts.join("+")
    }

    fn lua_quote(s: &str) -> String {
        format!("\"{}\"", s.replace('\\', "\\\\").replace('"', "\\\""))
    }

    fn hyprland_socket_present() -> bool {
        crate::game::desktop::hypr_socket_path(".socket.sock").is_ok()
    }

    fn hypr_eval(lua: &str) -> Result<(), String> {
        let reply = crate::game::desktop::hypr_command(&format!("eval {lua}"))?;
        let text = String::from_utf8_lossy(&reply);
        let line = text.split('\n').next().unwrap_or("").trim();
        if line == "ok" || line.is_empty() {
            return Ok(());
        }
        Err(line.to_string())
    }

    fn bind_all(
        slots: &[Slot],
        script: &std::path::Path,
        bound: &mut Vec<String>,
    ) -> Result<(), String> {
        let script = script.display().to_string();
        for slot in slots {
            let keys = hypr_lua_keys(&slot.binding);
            let fire = format!("{} {}", script, slot.action.as_str());
            let lua = format!(
                "hl.bind({}, hl.dsp.exec_cmd({}), {{ description = {} }})",
                lua_quote(&keys),
                lua_quote(&fire),
                lua_quote(&format!("df-hud: {}", slot.action.as_str())),
            );
            hypr_eval(&lua).map_err(|e| format!("{} ({e})", slot.binding.canonical()))?;
            bound.push(keys);
        }
        Ok(())
    }

    fn unbind_all(bound: &mut Vec<String>) {
        for keys in bound.drain(..) {
            let _ = hypr_eval(&format!("hl.unbind({})", lua_quote(&keys)));
        }
    }

    #[cfg(test)]
    mod tests {
        use super::hypr_lua_keys;
        use crate::app::hotkeys::parse_binding;

        #[test]
        fn lua_keys_match_hl_bind() {
            assert_eq!(hypr_lua_keys(&parse_binding("Alt+M").unwrap()), "ALT+m");
            assert_eq!(hypr_lua_keys(&parse_binding("V").unwrap()), "v");
            assert_eq!(hypr_lua_keys(&parse_binding("Grave").unwrap()), "grave");
            assert_eq!(
                hypr_lua_keys(&parse_binding("Ctrl+Shift+M").unwrap()),
                "CTRL+SHIFT+m"
            );
            assert_eq!(hypr_lua_keys(&parse_binding("Win+K").unwrap()), "SUPER+k");
        }
    }
}

#[cfg(windows)]
mod windows {
    use super::*;
    use std::time::Duration;
    use windows_sys::Win32::Foundation::HWND;
    use windows_sys::Win32::UI::Input::KeyboardAndMouse::{RegisterHotKey, UnregisterHotKey};
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        HWND_MESSAGE, MSG, PM_REMOVE, PeekMessageW, WM_HOTKEY,
    };

    const MOD_NOREPEAT: u32 = 0x4000;

    pub fn run(handle: Arc<crate::app::Handle>, stop: Arc<AtomicBool>) {
        let mut bound: Vec<i32> = Vec::new();
        let mut last_key = String::new();
        let mut last_focus = false;
        let mut msg = MSG::default();
        while !stop.load(Ordering::SeqCst) && !handle.stopped() {
            if !handle.game_running.load(Ordering::SeqCst) {
                if !bound.is_empty() {
                    unbind_all(&mut bound);
                    last_focus = false;
                }
                handle.ui.wait();
                continue;
            }
            let running = handle.config();
            let cfg = &running.hotkeys;
            let slots = if cfg.enabled {
                slots_from_cfg(cfg, running.widget.masteries.enabled)
            } else {
                Vec::new()
            };
            let key = slots
                .iter()
                .map(|s| format!("{}:{}", s.action.as_str(), s.binding.canonical()))
                .collect::<Vec<_>>()
                .join("|");
            let focused = cfg.enabled && game_focused(&handle) && hwnd_matches(&handle);
            if key != last_key || focused != last_focus {
                unbind_all(&mut bound);
                if focused {
                    for slot in &slots {
                        // SAFETY: a null HWND posts WM_HOTKEY to this thread's
                        // queue, which the PeekMessageW loop below drains on the
                        // same thread; the other arguments are plain integers.
                        let ok = unsafe {
                            RegisterHotKey(
                                std::ptr::null_mut(),
                                slot.id,
                                slot.binding.modifiers | MOD_NOREPEAT,
                                slot.binding.virtual_key,
                            )
                        };
                        if ok == 0 {
                            warn!(
                                "hotkeys: RegisterHotKey failed for {} ({})",
                                slot.binding.canonical(),
                                slot.action.as_str()
                            );
                            continue;
                        }
                        bound.push(slot.id);
                    }
                }
                last_key = key;
                last_focus = focused;
            }
            // SAFETY: `msg` is this thread's own live MSG, written only by
            // PeekMessageW and read after it returns; both peeks are on this
            // thread's queue.
            while unsafe {
                PeekMessageW(&mut msg, HWND_MESSAGE, WM_HOTKEY, WM_HOTKEY, PM_REMOVE) != 0
                    || PeekMessageW(
                        &mut msg,
                        std::ptr::null_mut(),
                        WM_HOTKEY,
                        WM_HOTKEY,
                        PM_REMOVE,
                    ) != 0
            } {
                if msg.message == WM_HOTKEY && focused {
                    let id = msg.wParam as i32;
                    if let Some(slot) = slots.iter().find(|s| s.id == id) {
                        fire(&handle, slot.action);
                    }
                }
            }
            std::thread::sleep(Duration::from_millis(25));
        }
        unbind_all(&mut bound);
    }

    fn hwnd_matches(handle: &crate::app::Handle) -> bool {
        let place = handle.vis.placement();
        let want = parse_hwnd(&place.address);
        let fg = crate::game::desktop::foreground_window();
        !want.is_null() && fg == want
    }

    fn parse_hwnd(address: &str) -> HWND {
        let s = address
            .trim()
            .trim_start_matches("0x")
            .trim_start_matches("0X");
        usize::from_str_radix(s, 16).unwrap_or(0) as HWND
    }

    fn unbind_all(bound: &mut Vec<i32>) {
        for id in bound.drain(..) {
            // SAFETY: `id` was registered by this thread with a null HWND, the
            // pair UnregisterHotKey needs to find it.
            unsafe { UnregisterHotKey(std::ptr::null_mut(), id) };
        }
    }
}

fn parse_modifier(part: &str) -> Option<u32> {
    match part.to_ascii_lowercase().as_str() {
        "alt" => Some(MOD_ALT),
        "ctrl" | "control" => Some(MOD_CONTROL),
        "shift" => Some(MOD_SHIFT),
        "win" | "windows" | "super" => Some(MOD_WIN),
        _ => None,
    }
}

fn parse_virtual_key(part: &str) -> Option<(String, u32)> {
    let upper = part.to_ascii_uppercase();
    if upper.len() == 1 {
        let key = upper.as_bytes()[0];
        if key.is_ascii_alphanumeric() {
            return Some((upper, u32::from(key)));
        }
    }
    if let Some(rest) = upper.strip_prefix('F')
        && let Ok(n) = rest.parse::<u32>()
        && (1..=24).contains(&n)
    {
        return Some((format!("F{n}"), 0x70 + n - 1));
    }
    let named: &[(&str, &str, u32)] = &[
        ("BACKTICK", "Grave", 0xc0),
        ("DELETE", "Delete", 0x2e),
        ("DOWN", "Down", 0x28),
        ("END", "End", 0x23),
        ("ENTER", "Enter", 0x0d),
        ("ESC", "Escape", 0x1b),
        ("ESCAPE", "Escape", 0x1b),
        ("GRAVE", "Grave", 0xc0),
        ("HOME", "Home", 0x24),
        ("INSERT", "Insert", 0x2d),
        ("LEFT", "Left", 0x25),
        ("PAGEDOWN", "PageDown", 0x22),
        ("PAGEUP", "PageUp", 0x21),
        ("PGDN", "PageDown", 0x22),
        ("PGUP", "PageUp", 0x21),
        ("RETURN", "Enter", 0x0d),
        ("RIGHT", "Right", 0x27),
        ("SPACE", "Space", 0x20),
        ("TAB", "Tab", 0x09),
        ("UP", "Up", 0x26),
    ];
    named
        .iter()
        .find(|(k, _, _)| *k == upper)
        .map(|(_, name, vk)| ((*name).to_string(), *vk))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_plain_and_chords() {
        let b = parse_binding("V").unwrap();
        assert_eq!(b.key, "V");
        assert_eq!(b.modifiers, 0);
        let b = parse_binding("Ctrl+Shift+M").unwrap();
        assert_eq!(b.key, "M");
        assert_eq!(b.modifiers, MOD_CONTROL | MOD_SHIFT);
        let b = parse_binding("Grave").unwrap();
        assert_eq!(b.key, "Grave");
        assert_eq!(b.virtual_key, 0xc0);
        let b = parse_binding("control + shift + m").unwrap();
        assert_eq!(b.canonical(), "Ctrl+Shift+M");
        assert!(parse_binding("Ctrl+").is_err());
        assert!(parse_binding("Nope").is_err());
        assert!(parse_binding("W").unwrap().is_wasd());
        assert!(!parse_binding("V").unwrap().is_wasd());
    }

    #[test]
    fn action_names_round_trip() {
        let cfg = crate::config::Config::default().hotkeys;
        let slots = slots_from_cfg(&cfg, true);
        assert_eq!(slots.len(), 6);
        for slot in &slots {
            assert_eq!(Action::parse(slot.action.as_str()), Some(slot.action));
        }
        assert_eq!(
            Action::parse("xp"),
            Some(Action::ResetXp),
            "the reset, not the widget"
        );
        assert_eq!(Action::parse("session"), None, "not bindable");
        assert_eq!(Action::parse("nope"), None);
        assert!(
            slots_from_cfg(&cfg, false)
                .iter()
                .all(|s| s.action != Action::Toggle(Group::Masteries)),
            "widget off: the key stays with the game"
        );
    }

    #[test]
    fn focus_match_uses_shared_active_address() {
        let place = crate::game::desktop::Placement {
            known: true,
            address: "0xabc".into(),
            ..crate::game::desktop::Placement::default()
        };
        assert!(focus_matches(true, &place, Some("0xabc")));
        assert!(!focus_matches(true, &place, Some("0xdef")));
        assert!(!focus_matches(false, &place, Some("0xabc")));
        assert!(!focus_matches(
            true,
            &crate::game::desktop::Placement::default(),
            Some("0xabc")
        ));
    }
}
