//! Process-wide minimum gap between reserved request slots.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::thread;
use std::time::{Duration, Instant};

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
    pub fn wait(&self, stop: &AtomicBool) -> Result<(), Cancelled> {
        let delay = {
            let mut last = self.last.lock().unwrap();
            let now = Instant::now();
            let slot = match *last {
                Some(prev) => {
                    let earliest = prev + self.min;
                    if earliest > now {
                        earliest
                    } else {
                        now
                    }
                }
                None => now,
            };
            *last = Some(slot);
            slot.saturating_duration_since(now)
        };
        sleep_cancellable(delay, stop)
    }

    pub fn reserved(&self) -> Option<Instant> {
        *self.last.lock().unwrap()
    }
}

pub(crate) fn sleep_cancellable(mut delay: Duration, stop: &AtomicBool) -> Result<(), Cancelled> {
    const SLICE: Duration = Duration::from_millis(5);
    while delay > Duration::ZERO {
        if stop.load(Ordering::SeqCst) {
            return Err(Cancelled);
        }
        let step = delay.min(SLICE);
        thread::sleep(step);
        delay -= step;
    }
    if stop.load(Ordering::SeqCst) {
        return Err(Cancelled);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    #[test]
    fn spaces_sequential_requests() {
        let gate = Gate::new(Duration::from_millis(40));
        let stop = AtomicBool::new(false);
        let mut times = Vec::new();
        for _ in 0..4 {
            gate.wait(&stop).unwrap();
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
        let times = Arc::new(Mutex::new(Vec::new()));
        let mut joins = Vec::new();
        for _ in 0..4 {
            let gate = gate.clone();
            let stop = stop.clone();
            let times = times.clone();
            joins.push(thread::spawn(move || {
                gate.wait(&stop).unwrap();
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
        gate.wait(&stop).unwrap();
        let stop = AtomicBool::new(true);
        let started = Instant::now();
        assert!(gate.wait(&stop).is_err());
        assert!(started.elapsed() < Duration::from_millis(200));
    }

    #[test]
    fn reserved_slots_are_spaced() {
        let gate = Gate::new(Duration::from_millis(1));
        let stop = AtomicBool::new(false);
        gate.wait(&stop).unwrap();
        let first = gate.reserved().unwrap();
        gate.wait(&stop).unwrap();
        let second = gate.reserved().unwrap();
        assert!(second - first >= Duration::from_millis(1));
    }

    #[test]
    fn one_second_floor_spaces_reserved_slots() {
        let gate = Gate::new(Duration::from_secs(1));
        let stop = AtomicBool::new(false);
        gate.wait(&stop).unwrap();
        let first = gate.reserved().unwrap();
        gate.wait(&stop).unwrap();
        let second = gate.reserved().unwrap();
        assert!(second - first >= Duration::from_secs(1));
    }
}
