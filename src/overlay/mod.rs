//! Shared 1 Hz overlay tick. Surfaces still own wait, remap, and swap.
//!
//! Each surface loop is: dispatch platform events → [`step`] → platform
//! wait. `step` runs the tick bookkeeping that used to be copied into both
//! loops (config reload, config sync, output follow, map/unmap on
//! visibility, present) and calls back into the surface through [`Hooks`]
//! for the five things that differ. That is deliberately not a platform
//! abstraction: the EGL/WGL window, `ack_configure`, output vs monitor
//! selection and the poll/MsgWait stay in `wayland.rs` and `win32.rs`.

use std::error::Error;
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::{Duration, Instant};

use chrono::Utc;

use crate::app::Handle;
use crate::config::{Config, Watch};

#[cfg(test)]
pub mod dummy;
#[cfg(target_os = "linux")]
pub mod egl;
pub mod font;
pub mod gpu;
pub mod layout;
pub mod present;
pub mod scene;
#[cfg(target_os = "linux")]
pub mod wayland;
#[cfg(windows)]
pub mod wgl;
#[cfg(windows)]
pub mod win32;

use scene::Scene;

pub const TICK: Duration = Duration::from_secs(1);
#[cfg(any(test, windows))]
const WIN32_WAIT_TIMEOUT: u32 = 0x102;

#[cfg(any(test, windows))]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Win32Wait {
    Wake,
    Messages,
    Timeout,
    Failed(u32),
}

#[cfg(any(test, windows))]
pub fn classify_win32_wait(result: u32, handle_count: u32) -> Win32Wait {
    if result < handle_count {
        Win32Wait::Wake
    } else if result == handle_count {
        Win32Wait::Messages
    } else if result == WIN32_WAIT_TIMEOUT {
        Win32Wait::Timeout
    } else {
        Win32Wait::Failed(result)
    }
}

/// Primary panel size for stamping `hud.reference_*` into a first-run config.
pub fn seed_reference() -> Option<(i32, i32)> {
    #[cfg(windows)]
    {
        win32::primary_panel()
    }
    #[cfg(not(windows))]
    {
        None
    }
}

pub fn start_tick(started: Instant) -> Instant {
    started + TICK
}

pub fn due(now: Instant, next_tick: &mut Instant) -> bool {
    if now < *next_tick {
        return false;
    }
    *next_tick = now + TICK;
    true
}

pub fn expired(started: Instant, duration: Duration) -> bool {
    duration > Duration::ZERO && started.elapsed() >= duration
}

/// The one copy out of `Watch` on a reload; everything downstream shares it.
pub fn take_reload(watch: &mut Watch) -> Option<Config> {
    watch.poll().then(|| watch.cfg.clone())
}

/// Tick state shared by both surfaces: the config file watch, the config
/// snapshot the surface last saw, and the 1 Hz / `--duration` clocks.
pub struct Runtime {
    watch: Watch,
    cfg: Arc<Config>,
    started: Instant,
    duration: Duration,
    next_tick: Instant,
}

impl Runtime {
    pub fn new(watch: Watch, cfg: Arc<Config>, duration: Duration) -> Self {
        let started = Instant::now();
        Self {
            watch,
            cfg,
            started,
            duration,
            next_tick: start_tick(started),
        }
    }

    pub fn expired(&self) -> bool {
        expired(self.started, self.duration)
    }

    /// Milliseconds until the next tick or the `--duration` end, whichever is
    /// sooner. The surface hands this to `poll` / `MsgWaitForMultipleObjects`.
    pub fn wait_ms(&self) -> u32 {
        wait_ms(Instant::now(), self.started, self.duration, self.next_tick)
    }
}

/// What a surface does at the five points of a tick where Wayland and Win32
/// differ. Every hook is idempotent; `step` calls `follow` and one of
/// `show` / `hide` every time, and `present` every time — the surface skips
/// it while unmapped and `Gpu::draw` skips identical frames.
pub trait Hooks {
    /// A new `Config` is in effect (file reload, tray, SIGHUP). Refresh what
    /// the surface caches from it, e.g. `Gpu::set_font`.
    fn config_changed(&mut self, cfg: &Config);
    /// Re-evaluate the pinned output / monitor; unmap if it moved.
    fn follow(&mut self, cfg: &Config);
    /// Visible: map (or start the remap) if not already mapped.
    fn show(&mut self, cfg: &Config) -> Result<(), Box<dyn Error>>;
    /// Hidden: unmap and drop the GL context if mapped.
    fn hide(&mut self);
    /// Build and draw one frame if the surface can take one right now.
    fn present(&mut self, cfg: &Config) -> Result<(), Box<dyn Error>>;
}

/// One pass of the tick: reload the config file when due, pick up any
/// config another thread replaced, follow the output, map or unmap to match
/// `Handle::visible`, then present.
pub fn step(
    rt: &mut Runtime,
    handle: &Handle,
    hooks: &mut impl Hooks,
) -> Result<(), Box<dyn Error>> {
    if due(Instant::now(), &mut rt.next_tick) {
        if let Some(cfg) = take_reload(&mut rt.watch) {
            handle.note_config_watch(&rt.watch, true);
            handle.replace_config(cfg);
        } else {
            handle.note_config_watch(&rt.watch, false);
        }
    }
    let cfg = handle.config();
    if !Arc::ptr_eq(&rt.cfg, &cfg) {
        rt.cfg = cfg;
        hooks.config_changed(&rt.cfg);
    }
    hooks.follow(&rt.cfg);
    if handle.visible.load(Ordering::SeqCst) {
        hooks.show(&rt.cfg)?;
    } else {
        hooks.hide();
    }
    hooks.present(&rt.cfg)
}

/// `cfg` is the caller's snapshot for this frame, so the surface reads the
/// same config it hands to the GPU.
pub fn scene(handle: &Handle, cfg: &Config, width: f32, height: f32) -> Scene {
    present::overlay_scene(
        &handle.store.derive(Utc::now()),
        cfg,
        &handle.groups,
        width,
        height,
    )
}

pub fn wait_ms(now: Instant, started: Instant, duration: Duration, next_tick: Instant) -> u32 {
    let mut at = next_tick;
    if duration > Duration::ZERO {
        let end = started + duration;
        if end < at {
            at = end;
        }
    }
    at.saturating_duration_since(now)
        .as_millis()
        .min(i32::MAX as u128) as u32
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn due_only_when_reached() {
        let now = Instant::now();
        let mut next = now + Duration::from_secs(10);
        assert!(!due(now, &mut next));
        let mut next = now;
        assert!(due(now, &mut next));
        assert!(next > now);
    }

    #[test]
    fn wait_picks_sooner_of_tick_and_duration() {
        let started = Instant::now();
        let next = started + TICK;
        assert_eq!(
            wait_ms(started, started, Duration::from_millis(200), next),
            200
        );
        assert_eq!(wait_ms(started, started, Duration::ZERO, next), 1000);
    }

    #[test]
    fn expired_ignores_zero_duration() {
        let started = Instant::now();
        assert!(!expired(started, Duration::ZERO));
        assert!(!expired(started, Duration::from_secs(10)));
    }

    #[test]
    fn classifies_win32_wait_results() {
        assert_eq!(classify_win32_wait(0, 1), Win32Wait::Wake);
        assert_eq!(classify_win32_wait(1, 1), Win32Wait::Messages);
        assert_eq!(
            classify_win32_wait(WIN32_WAIT_TIMEOUT, 1),
            Win32Wait::Timeout
        );
        assert_eq!(
            classify_win32_wait(u32::MAX, 1),
            Win32Wait::Failed(u32::MAX)
        );
    }
}
