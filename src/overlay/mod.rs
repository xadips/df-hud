//! Shared 1 Hz overlay tick. Surfaces still own wait, remap, and swap.

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

use gpu::Gpu;
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

pub fn take_reload(watch: &mut Watch) -> Option<Config> {
    watch.poll().then(|| watch.cfg.clone())
}

pub fn push_config(handle: &Handle, gpu: &mut Gpu, cfg: &Config) -> Config {
    let applied = handle.replace_config(cfg.clone());
    gpu.set_font(&applied.hud.font);
    applied
}

pub fn scene(handle: &Handle, width: f32, height: f32) -> Scene {
    let cfg = handle.cfg.lock().unwrap().clone();
    present::overlay_scene(
        &handle.store.derive(Utc::now()),
        &cfg,
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
