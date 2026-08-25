//! Process-wide minimum gap between reserved request slots.

use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use crate::wake::Notify;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Cancelled;

impl std::fmt::Display for Cancelled {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "rategate wait cancelled")
    }
}

impl std::error::Error for Cancelled {}

pub struct Gate {
    min: Duration,
    last: Mutex<Option<Instant>>,
}

impl Gate {
    pub fn new(min: Duration) -> Self {
        Self {
            min,
            last: Mutex::new(None),
        }
    }

    /// Reserves the next slot, then sleeps until it is due.
    pub fn wait(&self, stop: &AtomicBool, wake: &Notify) -> Result<(), Cancelled> {
        let delay = {
            let mut last = self.last.lock().unwrap();
            let now = Instant::now();
            let slot = match *last {
                Some(prev) => {
                    let earliest = prev + self.min;
                    if earliest > now { earliest } else { now }
                }
                None => now,
            };
            *last = Some(slot);
            slot.saturating_duration_since(now)
        };
        sleep_cancellable(delay, stop, wake)
    }

    pub fn reserved(&self) -> Option<Instant> {
        *self.last.lock().unwrap()
    }
}

pub(crate) fn sleep_cancellable(
    delay: Duration,
    stop: &AtomicBool,
    wake: &Notify,
) -> Result<(), Cancelled> {
    let deadline = Instant::now() + delay;
    loop {
        if stop.load(Ordering::SeqCst) {
            return Err(Cancelled);
        }
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return Ok(());
        }
        wake.wait_timeout(remaining);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use std::thread;

    fn idle_wake() -> Notify {
        Notify::new()
    }

    #[test]
    fn spaces_sequential_requests() {
        let gate = Gate::new(Duration::from_millis(40));
        let stop = AtomicBool::new(false);
        let wake = idle_wake();
        let mut times = Vec::new();
        for _ in 0..4 {
            gate.wait(&stop, &wake).unwrap();
            times.push(Instant::now());
        }
        for i in 1..times.len() {
            let gap = times[i] - times[i - 1];
            assert!(
                gap >= Duration::from_millis(35),
                "gap {i} = {gap:?}, want at least 35ms"
            );
        }
    }

    #[test]
    fn spaces_concurrent_callers() {
        let min = Duration::from_millis(20);
        let gate = Arc::new(Gate::new(min));
        let stop = Arc::new(AtomicBool::new(false));
        let wake = Arc::new(idle_wake());
        let times = Arc::new(Mutex::new(Vec::new()));
        let mut joins = Vec::new();
        for _ in 0..4 {
            let gate = gate.clone();
            let stop = stop.clone();
            let wake = wake.clone();
            let times = times.clone();
            joins.push(thread::spawn(move || {
                gate.wait(&stop, &wake).unwrap();
                times.lock().unwrap().push(Instant::now());
            }));
        }
        for j in joins {
            j.join().unwrap();
        }
        let mut times = times.lock().unwrap().clone();
        times.sort();
        assert_eq!(times.len(), 4);
        let spread = times[3] - times[0];
        assert!(spread >= min * 2, "spread {spread:?} is too short");
    }

    #[test]
    fn respects_cancel() {
        let gate = Gate::new(Duration::from_secs(3600));
        let stop = AtomicBool::new(false);
        let wake = idle_wake();
        gate.wait(&stop, &wake).unwrap();
        let stop = AtomicBool::new(true);
        let started = Instant::now();
        assert!(gate.wait(&stop, &wake).is_err());
        assert!(started.elapsed() < Duration::from_millis(200));
    }

    #[test]
    fn cancel_during_wait_returns_quickly() {
        let gate = Arc::new(Gate::new(Duration::from_secs(3600)));
        let stop = Arc::new(AtomicBool::new(false));
        let wake = Arc::new(idle_wake());
        gate.wait(&stop, &wake).unwrap();
        let started = Instant::now();
        let h = {
            let gate = gate.clone();
            let stop = stop.clone();
            let wake = wake.clone();
            thread::spawn(move || gate.wait(&stop, &wake))
        };
        thread::sleep(Duration::from_millis(20));
        stop.store(true, Ordering::SeqCst);
        wake.ping();
        assert!(h.join().unwrap().is_err());
        assert!(started.elapsed() < Duration::from_millis(200));
    }

    #[test]
    fn reserved_slots_are_spaced() {
        let gate = Gate::new(Duration::from_millis(1));
        let stop = AtomicBool::new(false);
        let wake = idle_wake();
        gate.wait(&stop, &wake).unwrap();
        let first = gate.reserved().unwrap();
        gate.wait(&stop, &wake).unwrap();
        let second = gate.reserved().unwrap();
        assert!(second - first >= Duration::from_millis(1));
    }

    #[test]
    fn one_second_floor_spaces_reserved_slots() {
        let gate = Gate::new(Duration::from_secs(1));
        let stop = AtomicBool::new(false);
        let wake = idle_wake();
        gate.wait(&stop, &wake).unwrap();
        let first = gate.reserved().unwrap();
        gate.wait(&stop, &wake).unwrap();
        let second = gate.reserved().unwrap();
        assert!(second - first >= Duration::from_secs(1));
    }
}
