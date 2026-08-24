//! Process watch. Linux `/proc` argv0 basename; Windows Toolhelp.
//! Existence and uptime only — not a window list.

use chrono::{DateTime, Utc};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use crate::model::GameState;
use crate::poller::Notify;

pub const DEFAULT_PROCESS: &str = "DeadFrontier.exe";

pub fn base_name(p: &str) -> &str {
    match p.rfind(['/', '\\']) {
        Some(i) => &p[i + 1..],
        None => p,
    }
}

pub trait Scanner: Send {
    fn scan(&self) -> Result<GameState, String>;
}

pub struct Watcher {
    scanner: Mutex<Box<dyn Scanner>>,
    interval: Duration,
    state: Mutex<GameState>,
    on_change: Mutex<Option<Arc<dyn Fn(GameState) + Send + Sync>>>,
    poke: Notify,
}

impl Watcher {
    pub fn new(exe_name: &str, interval: Duration) -> Arc<Self> {
        let interval = if interval.is_zero() {
            Duration::from_secs(2)
        } else {
            interval
        };
        Arc::new(Self {
            scanner: Mutex::new(platform_scanner(exe_name)),
            interval,
            state: Mutex::new(GameState::default()),
            on_change: Mutex::new(None),
            poke: Notify::new(),
        })
    }

    #[cfg(test)]
    pub fn with_scanner(scanner: Box<dyn Scanner>, interval: Duration) -> Arc<Self> {
        let w = Self::new(DEFAULT_PROCESS, interval);
        *w.scanner.lock().unwrap() = scanner;
        w
    }

    pub fn state(&self) -> GameState {
        *self.state.lock().unwrap()
    }

    pub fn set_on_change(&self, f: impl Fn(GameState) + Send + Sync + 'static) {
        *self.on_change.lock().unwrap() = Some(Arc::new(f));
    }

    #[cfg(test)]
    pub fn set_state_for_testing(&self, state: GameState) {
        *self.state.lock().unwrap() = state;
    }

    pub fn poke(&self) {
        self.poke.ping();
    }

    pub fn run(&self, stop: Arc<AtomicBool>) {
        self.scan_once();
        while !stop.load(Ordering::SeqCst) {
            self.poke.wait_timeout(self.interval);
            if stop.load(Ordering::SeqCst) {
                break;
            }
            self.scan_once();
        }
    }

    fn scan_once(&self) {
        let next = match self.scanner.lock().unwrap().scan() {
            Ok(s) => s,
            Err(_) => return,
        };
        let (changed, prev) = {
            let mut g = self.state.lock().unwrap();
            let prev = *g;
            let changed =
                next.running != prev.running || (next.running && !next.same_session(prev));
            if changed {
                *g = next;
            }
            (changed, prev)
        };
        if !changed {
            return;
        }
        match (next.running, prev.running) {
            (true, false) => eprintln!(
                "game: {} running (pid {}, started {})",
                DEFAULT_PROCESS,
                next.pid,
                next.started_at
                    .map(|t| t.format("%H:%M:%S").to_string())
                    .unwrap_or_default()
            ),
            (false, true) => {
                eprintln!("game: closed after {:?}", prev.elapsed(Utc::now()))
            }
            (true, true) => eprintln!("game: relaunched (pid {})", next.pid),
            _ => {}
        }
        if let Some(f) = self.on_change.lock().unwrap().clone() {
            f(next);
        }
    }
}

pub fn scan(exe_name: &str) -> Result<GameState, String> {
    platform_scanner(exe_name).scan()
}

pub fn scan_description(exe_name: &str) -> String {
    let name = if exe_name.trim().is_empty() {
        DEFAULT_PROCESS
    } else {
        exe_name
    };
    #[cfg(target_os = "linux")]
    {
        format!("looking for a process whose argv[0] basename is {name:?}")
    }
    #[cfg(windows)]
    {
        format!("looking for a process whose executable name is {name:?}")
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        format!("looking for a process named {name:?}")
    }
}

pub fn scan_error(err: &str) -> String {
    #[cfg(target_os = "linux")]
    {
        format!("could not scan /proc: {err}")
    }
    #[cfg(windows)]
    {
        format!("could not enumerate Windows processes: {err}")
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        format!("could not scan processes: {err}")
    }
}

pub fn similar_processes(needle: &str) -> Vec<String> {
    #[cfg(target_os = "linux")]
    {
        linux::similar_processes(needle)
    }
    #[cfg(windows)]
    {
        windows::similar_processes(needle)
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        let _ = needle;
        Vec::new()
    }
}

pub fn spawn(watcher: Arc<Watcher>, stop: Arc<AtomicBool>) {
    thread::Builder::new()
        .name("df-hud-game".into())
        .spawn(move || watcher.run(stop))
        .expect("spawn game watcher");
}

fn platform_scanner(exe_name: &str) -> Box<dyn Scanner> {
    #[cfg(target_os = "linux")]
    {
        Box::new(linux::ProcScanner::new(exe_name))
    }
    #[cfg(windows)]
    {
        Box::new(windows::ToolhelpScanner::new(exe_name))
    }
    #[cfg(not(any(target_os = "linux", windows)))]
    {
        Box::new(NullScanner)
    }
}

#[cfg(not(any(target_os = "linux", windows)))]
struct NullScanner;
#[cfg(not(any(target_os = "linux", windows)))]
impl Scanner for NullScanner {
    fn scan(&self) -> Result<GameState, String> {
        Ok(GameState::default())
    }
}

/// Oldest matching process. Shared by Windows Toolhelp and its tests so the
/// rule can run on Linux CI.
#[cfg(any(test, windows))]
#[derive(Clone, Debug)]
pub struct Proc {
    pub pid: u32,
    pub exe: String,
    pub started_at: Option<DateTime<Utc>>,
}

#[cfg(any(test, windows))]
pub fn oldest_matching<'a>(procs: &'a [Proc], exe: &str, self_pid: u32) -> Option<&'a Proc> {
    let mut best: Option<&Proc> = None;
    for p in procs {
        if p.pid == self_pid || p.started_at.is_none() {
            continue;
        }
        if !base_name(&p.exe).eq_ignore_ascii_case(exe) {
            continue;
        }
        match best {
            None => best = Some(p),
            Some(b) => {
                if p.started_at < b.started_at {
                    best = Some(p);
                }
            }
        }
    }
    best
}

#[cfg(target_os = "linux")]
pub mod linux {
    use super::*;
    use std::fs;
    use std::path::{Path, PathBuf};

    const USER_HZ: i64 = 100;

    pub struct ProcScanner {
        pub proc_root: PathBuf,
        pub exe_name: String,
        pub self_pid: i32,
    }

    impl ProcScanner {
        pub fn new(exe_name: &str) -> Self {
            let exe_name = if exe_name.is_empty() {
                DEFAULT_PROCESS
            } else {
                exe_name
            };
            Self {
                proc_root: PathBuf::from("/proc"),
                exe_name: exe_name.to_string(),
                self_pid: std::process::id() as i32,
            }
        }
    }

    impl Scanner for ProcScanner {
        fn scan(&self) -> Result<GameState, String> {
            scan_proc(&self.proc_root, &self.exe_name, self.self_pid)
        }
    }

    pub fn scan_proc(root: &Path, exe_name: &str, self_pid: i32) -> Result<GameState, String> {
        let entries = fs::read_dir(root).map_err(|e| e.to_string())?;
        let boot = boot_time(root)?;
        let mut best = GameState::default();
        for entry in entries.flatten() {
            let name = entry.file_name();
            let Some(pid) = name.to_str().and_then(|s| s.parse::<i32>().ok()) else {
                continue;
            };
            if pid == self_pid {
                continue;
            }
            let Ok(argv0) = process_argv0(root, pid) else {
                continue;
            };
            if argv0.is_empty() {
                continue;
            }
            if !base_name(&argv0).eq_ignore_ascii_case(exe_name) {
                continue;
            }
            let Ok(started) = process_start_time(root, pid, boot) else {
                continue;
            };
            if !best.running || started < best.started_at.unwrap_or(started) {
                best = GameState {
                    running: true,
                    pid,
                    started_at: Some(started),
                };
            }
        }
        Ok(best)
    }

    pub fn process_argv0(root: &Path, pid: i32) -> Result<String, String> {
        let data =
            fs::read(root.join(pid.to_string()).join("cmdline")).map_err(|e| e.to_string())?;
        let argv0 = data.split(|b| *b == 0).next().unwrap_or(&[]);
        Ok(String::from_utf8_lossy(argv0).into_owned())
    }

    pub fn process_start_time(
        root: &Path,
        pid: i32,
        boot: DateTime<Utc>,
    ) -> Result<DateTime<Utc>, String> {
        let data = fs::read(root.join(pid.to_string()).join("stat")).map_err(|e| e.to_string())?;
        let line = String::from_utf8_lossy(&data);
        let close = line
            .rfind(')')
            .ok_or_else(|| format!("proc: {pid}: malformed stat line"))?;
        if close + 2 >= line.len() {
            return Err(format!("proc: {pid}: malformed stat line"));
        }
        let fields: Vec<&str> = line[close + 2..].split_whitespace().collect();
        const START_TIME_INDEX: usize = 19;
        if fields.len() <= START_TIME_INDEX {
            return Err(format!(
                "proc: {pid}: stat has {} fields after comm, want more than {START_TIME_INDEX}",
                fields.len()
            ));
        }
        let ticks: i64 = fields[START_TIME_INDEX]
            .parse()
            .map_err(|_| format!("proc: {pid}: start time {:?}", fields[START_TIME_INDEX]))?;
        Ok(boot + chrono::Duration::milliseconds(ticks * 1000 / USER_HZ))
    }

    pub fn boot_time(root: &Path) -> Result<DateTime<Utc>, String> {
        let data = fs::read_to_string(root.join("stat")).map_err(|e| e.to_string())?;
        for line in data.lines() {
            if let Some(rest) = line.strip_prefix("btime ") {
                let secs: i64 = rest
                    .trim()
                    .parse()
                    .map_err(|_| format!("proc: btime {line:?}"))?;
                return DateTime::from_timestamp(secs, 0)
                    .ok_or_else(|| "proc: btime out of range".into());
            }
        }
        Err("proc: /proc/stat has no btime line".into())
    }

    pub fn similar_processes(needle: &str) -> Vec<String> {
        let needle = needle.to_ascii_lowercase();
        let Ok(entries) = fs::read_dir("/proc") else {
            return Vec::new();
        };
        let self_pid = std::process::id();
        let parent = std::os::unix::process::parent_id();
        let mut out = Vec::new();
        for entry in entries.flatten() {
            let Some(pid) = entry.file_name().to_str().and_then(|s| s.parse::<u32>().ok()) else {
                continue;
            };
            if pid == self_pid || pid == parent {
                continue;
            }
            let Ok(raw) = fs::read(format!("/proc/{pid}/cmdline")) else {
                continue;
            };
            let line = String::from_utf8_lossy(&raw).replace('\0', " ");
            let lower = line.to_ascii_lowercase();
            if !lower.contains(&needle) || lower.contains("df-hud") {
                continue;
            }
            let mut line = line.trim().to_string();
            if line.len() > 120 {
                line.truncate(120);
                line.push_str("...");
            }
            out.push(format!("pid {pid}: {line}"));
            if out.len() >= 8 {
                break;
            }
        }
        out
    }
}

#[cfg(windows)]
pub mod windows {
    use super::*;
    use std::mem::{size_of, zeroed};
    use windows_sys::Win32::Foundation::{CloseHandle, FILETIME, HANDLE};
    use windows_sys::Win32::System::Diagnostics::ToolHelp::{
        CreateToolhelp32Snapshot, Process32FirstW, Process32NextW, PROCESSENTRY32W,
        TH32CS_SNAPPROCESS,
    };
    use windows_sys::Win32::System::Threading::{
        GetProcessTimes, OpenProcess, PROCESS_QUERY_LIMITED_INFORMATION,
    };

    pub struct ToolhelpScanner {
        exe_name: String,
        self_pid: u32,
    }

    impl ToolhelpScanner {
        pub fn new(exe_name: &str) -> Self {
            let exe_name = if exe_name.is_empty() {
                DEFAULT_PROCESS
            } else {
                exe_name
            };
            Self {
                exe_name: exe_name.to_string(),
                self_pid: std::process::id(),
            }
        }
    }

    impl Scanner for ToolhelpScanner {
        fn scan(&self) -> Result<GameState, String> {
            let procs = enumerate(true)?;
            match oldest_matching(&procs, &self.exe_name, self.self_pid) {
                Some(p) => Ok(GameState {
                    running: true,
                    pid: p.pid as i32,
                    started_at: p.started_at,
                }),
                None => Ok(GameState::default()),
            }
        }
    }

    fn filetime_to_datetime(ft: FILETIME) -> DateTime<Utc> {
        let ticks = ((ft.dwHighDateTime as u64) << 32) | ft.dwLowDateTime as u64;
        let unix_100ns = ticks.saturating_sub(116_444_736_000_000_000);
        let secs = (unix_100ns / 10_000_000) as i64;
        let nsec = ((unix_100ns % 10_000_000) * 100) as u32;
        DateTime::from_timestamp(secs, nsec).unwrap_or(DateTime::<Utc>::UNIX_EPOCH)
    }

    fn process_start_time(pid: u32) -> Option<DateTime<Utc>> {
        unsafe {
            let handle: HANDLE = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid);
            if handle.is_null() || handle == -1isize as HANDLE {
                return None;
            }
            let mut creation = FILETIME {
                dwLowDateTime: 0,
                dwHighDateTime: 0,
            };
            let mut exit = creation;
            let mut kernel = creation;
            let mut user = creation;
            let ok = GetProcessTimes(handle, &mut creation, &mut exit, &mut kernel, &mut user);
            CloseHandle(handle);
            if ok == 0 {
                return None;
            }
            Some(filetime_to_datetime(creation))
        }
    }

    fn utf16_to_string(buf: &[u16]) -> String {
        let len = buf.iter().position(|&c| c == 0).unwrap_or(buf.len());
        String::from_utf16_lossy(&buf[..len])
    }

    fn enumerate(with_start: bool) -> Result<Vec<Proc>, String> {
        unsafe {
            let snap = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
            if snap.is_null() || snap == -1isize as HANDLE {
                return Err("could not enumerate Windows processes".into());
            }
            let mut entry: PROCESSENTRY32W = zeroed();
            entry.dwSize = size_of::<PROCESSENTRY32W>() as u32;
            let mut out = Vec::new();
            if Process32FirstW(snap, &mut entry) != 0 {
                loop {
                    let exe = utf16_to_string(&entry.szExeFile);
                    let pid = entry.th32ProcessID;
                    if !with_start {
                        out.push(Proc {
                            pid,
                            exe,
                            started_at: None,
                        });
                    } else if let Some(started) = process_start_time(pid) {
                        out.push(Proc {
                            pid,
                            exe,
                            started_at: Some(started),
                        });
                    }
                    entry.dwSize = size_of::<PROCESSENTRY32W>() as u32;
                    if Process32NextW(snap, &mut entry) == 0 {
                        break;
                    }
                }
            }
            CloseHandle(snap);
            Ok(out)
        }
    }

    pub fn similar_processes(needle: &str) -> Vec<String> {
        let needle = needle.to_ascii_lowercase();
        let Ok(processes) = enumerate(false) else {
            return Vec::new();
        };
        let self_pid = std::process::id();
        let mut out = Vec::new();
        for process in processes {
            if process.pid == self_pid
                || !process.exe.to_ascii_lowercase().contains(&needle)
            {
                continue;
            }
            out.push(format!("pid {}: {}", process.pid, process.exe));
            if out.len() >= 8 {
                break;
            }
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn base_name_splits_both_separators() {
        assert_eq!(
            base_name(r"C:\Program Files\DeadFrontier.exe"),
            "DeadFrontier.exe"
        );
        assert_eq!(
            base_name("/home/me/.steam/.../DeadFrontier.exe"),
            "DeadFrontier.exe"
        );
        assert_eq!(base_name("DeadFrontier.exe"), "DeadFrontier.exe");
    }

    #[test]
    fn oldest_matching_skips_self_and_inaccessible() {
        let base = Utc.timestamp_opt(1_700_000_000, 0).unwrap();
        let procs = vec![
            Proc {
                pid: 10,
                exe: "explorer.exe".into(),
                started_at: Some(base),
            },
            Proc {
                pid: 20,
                exe: "DeadFrontier.exe".into(),
                started_at: Some(base + chrono::Duration::minutes(2)),
            },
            Proc {
                pid: 21,
                exe: "deadfrontier.EXE".into(),
                started_at: Some(base + chrono::Duration::minutes(1)),
            },
            Proc {
                pid: 22,
                exe: "DeadFrontier.exe".into(),
                started_at: None,
            },
        ];
        let got = oldest_matching(&procs, "DeadFrontier.exe", 999).unwrap();
        assert_eq!(got.pid, 21);
        let self_only = vec![Proc {
            pid: 42,
            exe: "df-hud.exe".into(),
            started_at: Some(Utc::now()),
        }];
        assert!(oldest_matching(&self_only, "df-hud.exe", 42).is_none());
    }

    #[test]
    fn game_state_elapsed_and_session() {
        let now = Utc::now();
        let running = GameState {
            running: true,
            pid: 42,
            started_at: Some(now - chrono::Duration::minutes(90)),
        };
        assert_eq!(running.elapsed(now), Duration::from_secs(90 * 60));
        let mut stopped = running;
        stopped.running = false;
        assert_eq!(stopped.elapsed(now), Duration::ZERO);
        assert_eq!(
            running.elapsed(running.started_at.unwrap() - chrono::Duration::hours(1)),
            Duration::ZERO
        );
        assert!(running.same_session(running));
        let recycled = GameState {
            running: true,
            pid: 42,
            started_at: Some(now - chrono::Duration::minutes(1)),
        };
        assert!(!running.same_session(recycled));
        assert!(!running.same_session(stopped));
    }
}

#[cfg(all(test, target_os = "linux"))]
mod linux_tests {
    use super::linux::*;
    use super::*;
    use chrono::{DateTime, Utc};
    use std::fs;
    use std::path::{Path, PathBuf};
    use std::sync::atomic::{AtomicBool, AtomicI32, AtomicU64, Ordering};
    use std::sync::mpsc;
    use std::sync::Arc;
    use std::thread;
    use std::time::Duration;

    static N: AtomicU64 = AtomicU64::new(1);

    struct FakeProc {
        root: PathBuf,
        btime: i64,
    }

    impl Drop for FakeProc {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.root);
        }
    }

    impl FakeProc {
        fn new() -> Self {
            let n = N.fetch_add(1, Ordering::Relaxed);
            let root =
                std::env::temp_dir().join(format!("df-hud-proc-{}-{}", std::process::id(), n));
            fs::create_dir_all(&root).unwrap();
            let btime = 1_786_429_751i64;
            fs::write(
                root.join("stat"),
                format!("cpu  1 2 3\nbtime {btime}\nprocesses 4242\n"),
            )
            .unwrap();
            Self { root, btime }
        }

        fn add_process(&self, pid: i32, comm: &str, argv0: &str, start_ticks: i64) {
            let dir = self.root.join(pid.to_string());
            fs::create_dir_all(&dir).unwrap();
            let mut cmdline = argv0.as_bytes().to_vec();
            cmdline.push(0);
            cmdline.extend_from_slice(b"--some-flag");
            cmdline.push(0);
            fs::write(dir.join("cmdline"), cmdline).unwrap();
            let mut fields = vec![pid.to_string(), format!("({comm})")];
            for _ in 3..=21 {
                fields.push("0".into());
            }
            fields.push(start_ticks.to_string());
            fields.push("trailing".into());
            fields.push("fields".into());
            fs::write(dir.join("stat"), fields.join(" ") + "\n").unwrap();
        }

        fn scanner(&self, exe: &str) -> ProcScanner {
            ProcScanner {
                proc_root: self.root.clone(),
                exe_name: exe.into(),
                self_pid: 999999,
            }
        }

        fn started_at(&self, ticks: i64) -> DateTime<Utc> {
            DateTime::from_timestamp(self.btime, 0).unwrap()
                + chrono::Duration::milliseconds(ticks * 10)
        }
    }

    #[test]
    fn proc_scan_finds_the_game() {
        let p = FakeProc::new();
        p.add_process(100, "systemd", "/usr/lib/systemd/systemd", 50);
        p.add_process(
            200,
            "DeadFrontier.e",
            r"Z:\home\player\Program Files\DeadFrontier.exe",
            360_000,
        );
        let got = p.scanner("DeadFrontier.exe").scan().unwrap();
        assert!(got.running && got.pid == 200, "{got:?}");
        assert_eq!(got.started_at, Some(p.started_at(360_000)));
    }

    #[test]
    fn proc_scan_handles_windows_paths() {
        for argv0 in [
            r"C:\Program Files (x86)\Dead Frontier\DeadFrontier.exe",
            r"Z:\home\player\games\DeadFrontier.exe",
            "/home/player/.steam/.../Dead Frontier/DeadFrontier.exe",
            "DeadFrontier.exe",
            "deadfrontier.exe",
        ] {
            let p = FakeProc::new();
            p.add_process(300, "DeadFrontier.e", argv0, 1000);
            let got = p.scanner("DeadFrontier.exe").scan().unwrap();
            assert!(got.running, "argv0 {argv0:?} was not detected");
        }
    }

    #[test]
    fn proc_scan_ignores_mentions_in_other_command_lines() {
        let p = FakeProc::new();
        p.add_process(400, "grep", "/usr/bin/grep", 100);
        p.add_process(401, "nvim", "/usr/bin/nvim", 200);
        p.add_process(402, "df-hud", "/home/player/Programming/df-hud/df-hud", 300);
        let dir = p.root.join("403");
        fs::create_dir_all(&dir).unwrap();
        fs::write(
            dir.join("cmdline"),
            b"/usr/bin/grep\x00DeadFrontier.exe\x00",
        )
        .unwrap();
        fs::write(
            dir.join("stat"),
            format!("403 (grep) {}5000 x y\n", "0 ".repeat(19)),
        )
        .unwrap();
        let got = p.scanner("DeadFrontier.exe").scan().unwrap();
        assert!(!got.running, "{got:?}");
    }

    #[test]
    fn proc_scan_prefers_the_oldest_match() {
        let p = FakeProc::new();
        p.add_process(500, "DeadFrontier.e", "/games/DeadFrontier.exe", 900_000);
        p.add_process(501, "DeadFrontier.e", "/games/DeadFrontier.exe", 360_000);
        p.add_process(502, "DeadFrontier.e", "/games/DeadFrontier.exe", 500_000);
        let got = p.scanner("DeadFrontier.exe").scan().unwrap();
        assert_eq!(got.pid, 501);
    }

    #[test]
    fn proc_scan_skips_self() {
        let p = FakeProc::new();
        p.add_process(600, "DeadFrontier.e", "/games/DeadFrontier.exe", 1000);
        let mut s = p.scanner("DeadFrontier.exe");
        s.self_pid = 600;
        let got = s.scan().unwrap();
        assert!(!got.running);
    }

    #[test]
    fn process_start_time_survives_a_weird_process_name() {
        let p = FakeProc::new();
        p.add_process(
            700,
            "evil) (name with spaces",
            "/games/DeadFrontier.exe",
            12_345,
        );
        let got = p.scanner("DeadFrontier.exe").scan().unwrap();
        assert!(got.running);
        assert_eq!(got.started_at, Some(p.started_at(12_345)));
    }

    #[test]
    fn proc_scan_no_game_running() {
        let p = FakeProc::new();
        p.add_process(800, "firefox", "/usr/lib/firefox/firefox", 100);
        let got = p.scanner("DeadFrontier.exe").scan().unwrap();
        assert!(!got.running && got.pid == 0 && got.started_at.is_none());
    }

    #[test]
    fn boot_time_errors() {
        let dir = std::env::temp_dir().join(format!(
            "df-hud-btime-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir_all(&dir).unwrap();
        assert!(boot_time(&dir).is_err());
        fs::write(dir.join("stat"), "cpu 1 2 3\n").unwrap();
        assert!(boot_time(&dir).is_err());
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn boot_time_against_the_real_proc() {
        let boot = match boot_time(Path::new("/proc")) {
            Ok(b) => b,
            Err(e) => {
                eprintln!("skip: {e}");
                return;
            }
        };
        let now = Utc::now();
        assert!(boot < now);
        assert!(now - boot < chrono::Duration::days(365));
        let started =
            process_start_time(Path::new("/proc"), std::process::id() as i32, boot).unwrap();
        assert!(started >= boot);
        assert!(started <= now + chrono::Duration::seconds(1));
    }

    #[test]
    fn watcher_reports_changes_once() {
        let p = FakeProc::new();
        p.add_process(900, "DeadFrontier.e", "/games/DeadFrontier.exe", 360_000);
        let w = Watcher::with_scanner(
            Box::new(p.scanner("DeadFrontier.exe")),
            Duration::from_millis(10),
        );
        let (tx, rx) = mpsc::channel();
        w.set_on_change(move |s| {
            let _ = tx.send(s);
        });
        let stop = Arc::new(AtomicBool::new(false));
        let w2 = w.clone();
        let stop2 = stop.clone();
        thread::spawn(move || w2.run(stop2));
        let got = rx.recv_timeout(Duration::from_secs(2)).expect("detected");
        assert!(got.running && got.pid == 900);
        assert!(
            rx.recv_timeout(Duration::from_millis(150)).is_err(),
            "unchanged scan fired a change"
        );
        fs::remove_dir_all(p.root.join("900")).unwrap();
        let got = rx.recv_timeout(Duration::from_secs(2)).expect("closed");
        assert!(!got.running);
        assert!(!w.state().running);
        stop.store(true, Ordering::SeqCst);
        w.poke();
    }

    #[test]
    fn watcher_detects_relaunch() {
        let p = FakeProc::new();
        p.add_process(910, "DeadFrontier.e", "/games/DeadFrontier.exe", 100_000);
        let w = Watcher::with_scanner(
            Box::new(p.scanner("DeadFrontier.exe")),
            Duration::from_secs(3600),
        );
        let (tx, rx) = mpsc::channel();
        w.set_on_change(move |s| {
            let _ = tx.send(s);
        });
        let stop = Arc::new(AtomicBool::new(false));
        let w2 = w.clone();
        let stop2 = stop.clone();
        thread::spawn(move || w2.run(stop2));
        let first = rx.recv_timeout(Duration::from_secs(2)).unwrap();
        assert_eq!(first.pid, 910);
        fs::remove_dir_all(p.root.join("910")).unwrap();
        p.add_process(911, "DeadFrontier.e", "/games/DeadFrontier.exe", 500_000);
        w.poke();
        let got = rx.recv_timeout(Duration::from_secs(2)).expect("relaunch");
        assert!(got.running && got.pid == 911);
        assert_ne!(got.started_at, first.started_at);
        stop.store(true, Ordering::SeqCst);
        w.poke();
    }

    #[test]
    fn watcher_is_quiet_while_the_game_is_closed() {
        let p = FakeProc::new();
        p.add_process(100, "firefox", "/usr/lib/firefox/firefox", 5000);
        let w = Watcher::with_scanner(
            Box::new(p.scanner("DeadFrontier.exe")),
            Duration::from_millis(10),
        );
        let n = Arc::new(AtomicI32::new(0));
        let n2 = n.clone();
        w.set_on_change(move |_| {
            n2.fetch_add(1, Ordering::SeqCst);
        });
        let stop = Arc::new(AtomicBool::new(false));
        let w2 = w.clone();
        let stop2 = stop.clone();
        thread::spawn(move || w2.run(stop2));
        thread::sleep(Duration::from_millis(200));
        assert_eq!(n.load(Ordering::SeqCst), 0);
        p.add_process(200, "DeadFrontier.e", "/games/DeadFrontier.exe", 360_000);
        let deadline = std::time::Instant::now() + Duration::from_secs(2);
        while n.load(Ordering::SeqCst) == 0 {
            if std::time::Instant::now() > deadline {
                panic!("the game starting was not reported");
            }
            thread::sleep(Duration::from_millis(10));
        }
        assert_eq!(n.load(Ordering::SeqCst), 1);
        stop.store(true, Ordering::SeqCst);
        w.poke();
    }
}
