//! Last launch wins: an overlay or headless run first stops every older
//! df-hud of this user, gracefully, and ends the ones that do not quit.

use std::time::Duration;

use chrono::{DateTime, Utc};

const GRACE: Duration = Duration::from_secs(5);

#[derive(Clone, Debug, PartialEq)]
struct Peer {
    pid: u32,
    started_at: DateTime<Utc>,
}

#[derive(Debug, PartialEq)]
enum Outcome {
    Quit,
    Killed,
    Failed(String),
}

/// Ties go to the higher pid, so two copies started together do not stop
/// each other.
fn older_than(procs: Vec<Peer>, me: u32) -> Vec<Peer> {
    let Some(mine) = procs.iter().find(|p| p.pid == me).cloned() else {
        return Vec::new();
    };
    procs
        .into_iter()
        .filter(|p| (p.started_at, p.pid) < (mine.started_at, mine.pid))
        .collect()
}

pub fn take_over() {
    let older = older_than(platform::df_hud_processes(), std::process::id());
    if older.is_empty() {
        return;
    }
    for (peer, outcome) in older.iter().zip(platform::stop(&older)) {
        match outcome {
            Outcome::Quit => info!(
                "instance: stopped the df-hud already running (pid {})",
                peer.pid
            ),
            Outcome::Killed => warn!(
                "instance: the df-hud already running (pid {}) did not quit within {}s; ended it",
                peer.pid,
                GRACE.as_secs()
            ),
            Outcome::Failed(err) => {
                warn!(
                    "instance: could not stop the df-hud already running (pid {}): {err}",
                    peer.pid
                )
            }
        }
    }
}

#[cfg(target_os = "linux")]
mod platform {
    use super::{GRACE, Outcome, Peer};
    use crate::game::{base_name, linux};
    use std::os::unix::fs::MetadataExt;
    use std::path::Path;
    use std::time::{Duration, Instant};

    const PROC: &str = "/proc";

    pub fn df_hud_processes() -> Vec<Peer> {
        let root = Path::new(PROC);
        let (Ok(boot), Ok(me), Ok(entries)) = (
            linux::boot_time(root),
            std::fs::metadata("/proc/self"),
            std::fs::read_dir(root),
        ) else {
            return Vec::new();
        };
        let mut out = Vec::new();
        for entry in entries.flatten() {
            let Some(pid) = entry.file_name().to_str().and_then(|s| s.parse().ok()) else {
                continue;
            };
            if !entry.metadata().is_ok_and(|m| m.uid() == me.uid()) {
                continue;
            }
            let Ok(argv0) = linux::process_argv0(root, pid) else {
                continue;
            };
            if base_name(&argv0) != "df-hud" {
                continue;
            }
            if let Ok(started_at) = linux::process_start_time(root, pid, boot) {
                out.push(Peer {
                    pid: pid as u32,
                    started_at,
                });
            }
        }
        out
    }

    fn alive(peer: &Peer) -> bool {
        let root = Path::new(PROC);
        linux::boot_time(root)
            .and_then(|boot| linux::process_start_time(root, peer.pid as i32, boot))
            .is_ok_and(|started| started == peer.started_at)
    }

    fn signal(peer: &Peer, sig: libc::c_int) -> Result<(), String> {
        if !alive(peer) {
            return Ok(());
        }
        // SAFETY: kill(2) takes a pid and a signal number; no pointers.
        if unsafe { libc::kill(peer.pid as libc::pid_t, sig) } == 0 {
            return Ok(());
        }
        let err = std::io::Error::last_os_error();
        if err.raw_os_error() == Some(libc::ESRCH) {
            Ok(())
        } else {
            Err(err.to_string())
        }
    }

    fn wait(peer: &Peer, until: Instant) -> bool {
        loop {
            if !alive(peer) {
                return true;
            }
            if Instant::now() >= until {
                return false;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    }

    pub fn stop(peers: &[Peer]) -> Vec<Outcome> {
        let asked: Vec<_> = peers.iter().map(|p| signal(p, libc::SIGTERM)).collect();
        let deadline = Instant::now() + GRACE;
        peers
            .iter()
            .zip(asked)
            .map(|(peer, asked)| {
                if let Err(err) = asked {
                    return Outcome::Failed(err);
                }
                if wait(peer, deadline) {
                    return Outcome::Quit;
                }
                if let Err(err) = signal(peer, libc::SIGKILL) {
                    return Outcome::Failed(err);
                }
                if wait(peer, Instant::now() + Duration::from_secs(2)) {
                    Outcome::Killed
                } else {
                    Outcome::Failed("still running after SIGKILL".into())
                }
            })
            .collect()
    }
}

#[cfg(windows)]
mod platform {
    use super::{GRACE, Outcome, Peer};
    use crate::game::base_name;
    use crate::game::windows::{creation_time, enumerate};
    use crate::overlay::win32::{OVERLAY_CLASS, last_err, wide};
    use std::ptr;
    use std::time::{Duration, Instant};
    use windows_sys::Win32::Foundation::{CloseHandle, HANDLE, HWND, WAIT_OBJECT_0};
    use windows_sys::Win32::System::Threading::{
        OpenProcess, PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE, PROCESS_TERMINATE,
        TerminateProcess, WaitForSingleObject,
    };
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        FindWindowExW, GetWindowThreadProcessId, PostMessageW, WM_CLOSE,
    };

    pub fn df_hud_processes() -> Vec<Peer> {
        enumerate(true)
            .unwrap_or_default()
            .into_iter()
            .filter(|p| base_name(&p.exe).eq_ignore_ascii_case("df-hud.exe"))
            .filter_map(|p| {
                Some(Peer {
                    pid: p.pid,
                    started_at: p.started_at?,
                })
            })
            .collect()
    }

    struct Process(HANDLE);

    impl Drop for Process {
        fn drop(&mut self) {
            // SAFETY: `open` returned this handle and nothing else closes it.
            unsafe { CloseHandle(self.0) };
        }
    }

    /// `None` once the pid is gone or belongs to a newer process.
    fn open(peer: &Peer) -> Result<Option<Process>, String> {
        let access = PROCESS_SYNCHRONIZE | PROCESS_TERMINATE | PROCESS_QUERY_LIMITED_INFORMATION;
        // SAFETY: takes an access mask, an inherit flag and a pid; no pointers.
        let handle = unsafe { OpenProcess(access, 0, peer.pid) };
        if handle.is_null() {
            return Err(last_err("OpenProcess").to_string());
        }
        let process = Process(handle);
        Ok((creation_time(process.0) == Some(peer.started_at)).then_some(process))
    }

    fn close_windows(pids: &[u32]) {
        let class = wide(OVERLAY_CLASS);
        let mut after: HWND = ptr::null_mut();
        loop {
            // SAFETY: `class` is NUL-terminated and outlives the call; a null
            // parent walks top-level windows, starting after `after`.
            let hwnd =
                unsafe { FindWindowExW(ptr::null_mut(), after, class.as_ptr(), ptr::null()) };
            if hwnd.is_null() {
                return;
            }
            let mut pid = 0u32;
            // SAFETY: `hwnd` came from FindWindowExW; `pid` is a live out-param.
            unsafe { GetWindowThreadProcessId(hwnd, &mut pid) };
            if pids.contains(&pid) {
                // SAFETY: no pointer arguments; a window that is already gone fails the post.
                unsafe { PostMessageW(hwnd, WM_CLOSE, 0, 0) };
            }
            after = hwnd;
        }
    }

    fn wait(process: &Process, until: Instant) -> bool {
        let ms = until.saturating_duration_since(Instant::now()).as_millis();
        // SAFETY: `process.0` is open with SYNCHRONIZE access.
        unsafe { WaitForSingleObject(process.0, ms.min(u32::MAX as u128) as u32) == WAIT_OBJECT_0 }
    }

    pub fn stop(peers: &[Peer]) -> Vec<Outcome> {
        let opened: Vec<_> = peers.iter().map(open).collect();
        close_windows(&peers.iter().map(|p| p.pid).collect::<Vec<_>>());
        let deadline = Instant::now() + GRACE;
        opened
            .into_iter()
            .map(|opened| {
                let process = match opened {
                    Ok(Some(process)) => process,
                    Ok(None) => return Outcome::Quit,
                    Err(err) => return Outcome::Failed(err),
                };
                if wait(&process, deadline) {
                    return Outcome::Quit;
                }
                // SAFETY: `process.0` is open with PROCESS_TERMINATE access.
                if unsafe { TerminateProcess(process.0, 1) } == 0 {
                    return Outcome::Failed(last_err("TerminateProcess").to_string());
                }
                if wait(&process, Instant::now() + Duration::from_secs(2)) {
                    Outcome::Killed
                } else {
                    Outcome::Failed("still running after TerminateProcess".into())
                }
            })
            .collect()
    }
}

#[cfg(not(any(target_os = "linux", windows)))]
mod platform {
    use super::{Outcome, Peer};

    pub fn df_hud_processes() -> Vec<Peer> {
        Vec::new()
    }

    pub fn stop(_: &[Peer]) -> Vec<Outcome> {
        Vec::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn peer(pid: u32, secs: i64) -> Peer {
        Peer {
            pid,
            started_at: DateTime::from_timestamp(secs, 0).unwrap(),
        }
    }

    #[test]
    fn only_copies_started_before_this_one_are_stopped() {
        let procs = vec![peer(10, 100), peer(20, 300), peer(30, 200)];
        assert_eq!(older_than(procs, 30), vec![peer(10, 100)]);
    }

    #[test]
    fn the_newest_copy_stops_all_the_others() {
        let procs = vec![peer(10, 100), peer(20, 300), peer(30, 200)];
        assert_eq!(older_than(procs, 20), vec![peer(10, 100), peer(30, 200)]);
    }

    #[test]
    fn copies_started_together_do_not_stop_each_other() {
        let procs = vec![peer(10, 100), peer(11, 100)];
        assert_eq!(older_than(procs.clone(), 11), vec![peer(10, 100)]);
        assert!(older_than(procs, 10).is_empty());
    }

    #[test]
    fn a_scan_that_misses_this_process_stops_nothing() {
        assert!(older_than(vec![peer(10, 100)], 99).is_empty());
    }
}
