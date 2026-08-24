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

pub fn push_config(handle: &Handle, gpu: &mut Gpu, cfg: &Config) {
    handle.replace_config(cfg.clone());
    gpu.set_font(&cfg.hud.font);
}

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
}
