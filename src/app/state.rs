//! Persistent HUD state: the run clock, XP sample ring, and challenge-done
//! memory survive a restart because they cannot be recovered from the server.
//! [`Persist`] owns the file; the HUD truth is `app::store::Store`.

use crate::wake::lock;
use chrono::{DateTime, Utc};
use serde::{Deserialize, Deserializer, Serialize};
use std::collections::{HashMap, HashSet};
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use super::store::push_xp_sample;
use crate::data::challenges;
use crate::model::{Challenge, RunState, XpSample};
use crate::wake::Notify;

const SCHEMA_VERSION: i32 = 1;
const SAVE_INTERVAL: Duration = Duration::from_secs(30);

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct State {
    pub schema_version: i32,
    pub saved_at: Option<DateTime<Utc>>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub xp_samples: Vec<XpSample>,
    /// Cycle keys ([`challenges::cycle_key`]) of completed challenges.
    #[serde(
        default,
        skip_serializing_if = "HashSet::is_empty",
        deserialize_with = "challenge_done_set"
    )]
    pub challenge_done: HashSet<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run: Option<RunState>,
}

/// Files written before 0.4.13 hold `{"key": true}`; the values were never
/// anything but `true`, so the set form is a plain array.
fn challenge_done_set<'de, D: Deserializer<'de>>(d: D) -> Result<HashSet<String>, D::Error> {
    #[derive(Deserialize)]
    #[serde(untagged)]
    enum Shape {
        Set(HashSet<String>),
        Map(HashMap<String, bool>),
    }
    Ok(match Shape::deserialize(d)? {
        Shape::Set(s) => s,
        Shape::Map(m) => m
            .into_iter()
            .filter_map(|(k, done)| done.then_some(k))
            .collect(),
    })
}

pub struct Persist {
    path: PathBuf,
    inner: Mutex<Inner>,
    /// Pinged by `update` so the saver thread sleeps until there is
    /// something to write, and by `poke` so it notices a stop.
    changed: Notify,
    /// One writer at a time on the temp file: the saver thread and the
    /// shutdown flush both call `save`.
    writing: Mutex<()>,
}

struct Inner {
    state: State,
    dirty: bool,
    revision: u64,
    last_save: Option<DateTime<Utc>>,
    now: Box<dyn Fn() -> DateTime<Utc> + Send>,
}

impl Persist {
    pub fn new(path: impl Into<PathBuf>) -> Self {
        Self {
            path: path.into(),
            inner: Mutex::new(Inner {
                state: State {
                    schema_version: SCHEMA_VERSION,
                    ..State::default()
                },
                dirty: false,
                revision: 0,
                last_save: None,
                now: Box::new(Utc::now),
            }),
            changed: Notify::new(),
            writing: Mutex::new(()),
        }
    }

    pub fn load(&self) -> Result<(), String> {
        let data = match fs::read(&self.path) {
            Ok(d) => d,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(()),
            Err(err) => return Err(err.to_string()),
        };
        let parsed: Result<State, _> = serde_json::from_slice(&data);
        match parsed {
            Ok(st) if st.schema_version == SCHEMA_VERSION => {
                let mut g = lock(&self.inner);
                g.state = st;
                Ok(())
            }
            _ => {
                let aside = format!("{}.corrupt-{}", self.path.display(), Utc::now().timestamp());
                fs::rename(&self.path, &aside).map_err(|e| {
                    format!("state file unusable and could not be moved aside: {e}")
                })?;
                warn!("state: file unusable, moved to {aside}, starting fresh");
                Ok(())
            }
        }
    }

    pub fn update(&self, fn_: impl FnOnce(&mut State)) {
        {
            let mut g = lock(&self.inner);
            fn_(&mut g.state);
            g.state.schema_version = SCHEMA_VERSION;
            g.dirty = true;
            g.revision += 1;
        }
        self.changed.ping();
    }

    pub fn get(&self) -> State {
        lock(&self.inner).state.clone()
    }

    fn now(&self) -> DateTime<Utc> {
        (lock(&self.inner).now)()
    }

    /// Time until the debounced save is due: `None` while nothing is dirty,
    /// zero when `maybe_save` would write now.
    pub fn save_due_in(&self) -> Option<Duration> {
        let g = lock(&self.inner);
        if !g.dirty {
            return None;
        }
        let now = (g.now)();
        Some(g.last_save.map_or(Duration::ZERO, |last| {
            let since = now
                .signed_duration_since(last)
                .to_std()
                .unwrap_or(Duration::ZERO);
            SAVE_INTERVAL.saturating_sub(since)
        }))
    }

    pub fn maybe_save(&self) -> Result<(), String> {
        match self.save_due_in() {
            Some(due) if due.is_zero() => self.save(),
            _ => Ok(()),
        }
    }

    /// Saver thread body. Blocks until `update` dirties the state, then for
    /// the rest of the debounce, and writes; no wakeups while clean. Returns
    /// once `stop` is set and `poke` is called.
    pub fn run_saver(&self, stop: &AtomicBool) {
        while !stop.load(Ordering::SeqCst) {
            match self.save_due_in() {
                None => self.changed.wait(),
                Some(due) if due.is_zero() => {
                    if let Err(err) = self.maybe_save() {
                        error!("state: could not save: {err}");
                        // The write failed, so `last_save` did not move and
                        // the state is still due: hold the retry to the
                        // debounce (or the next update) instead of spinning.
                        self.changed.wait_timeout(SAVE_INTERVAL);
                    }
                }
                Some(due) => {
                    self.changed.wait_timeout(due);
                }
            }
        }
    }

    /// Wakes the saver thread so it re-reads `stop`.
    pub fn poke(&self) {
        self.changed.ping();
    }

    pub fn save(&self) -> Result<(), String> {
        let _one_writer = lock(&self.writing);
        let (snapshot, revision, path) = {
            let mut g = lock(&self.inner);
            if self.path.as_os_str().is_empty() {
                g.dirty = false;
                return Ok(());
            }
            g.state.saved_at = Some((g.now)());
            (g.state.clone(), g.revision, self.path.clone())
        };
        write_file(&path, &snapshot)?;
        self.mark_saved(revision);
        Ok(())
    }

    fn mark_saved(&self, revision: u64) {
        let mut g = lock(&self.inner);
        if g.revision == revision {
            g.dirty = false;
        }
        g.last_save = Some((g.now)());
    }

    pub fn append_xp_sample(&self, sample: XpSample) {
        self.update(|st| {
            if let Some(prev) = st.xp_samples.last()
                && prev.source != sample.source
            {
                info!(
                    "state: cumulative XP source changed from {} to {}; resetting the rate window",
                    prev.source, sample.source
                );
            }
            push_xp_sample(&mut st.xp_samples, sample);
        });
    }

    pub fn reset_xp_window(&self, reason: &str) {
        self.update(|st| {
            if st.xp_samples.is_empty() {
                return;
            }
            info!("state: resetting the XP rate window ({reason})");
            st.xp_samples.clear();
        });
    }

    /// Applies sticky completion to `board`, latches what just finished, and
    /// forgets cycles that have ended and left the board: their key can never
    /// come round again, since the next cycle carries a new end date.
    pub fn remember_challenge_board(&self, mut board: Vec<Challenge>) -> Vec<Challenge> {
        let done = self.get().challenge_done;
        let newly = challenges::apply_sticky(&mut board, &done);
        let now = self.now();
        let on_board: HashSet<String> = board.iter().map(challenges::cycle_key).collect();
        let stale: Vec<String> = done
            .iter()
            .filter(|key| !on_board.contains(*key) && challenges::cycle_ended(key, now))
            .cloned()
            .collect();
        if !newly.is_empty() || !stale.is_empty() {
            self.update(|st| {
                for key in &stale {
                    st.challenge_done.remove(key);
                }
                st.challenge_done.extend(newly);
            });
        }
        board
    }

    #[cfg(test)]
    fn set_now(&self, at: DateTime<Utc>) {
        lock(&self.inner).now = Box::new(move || at);
    }

    #[cfg(test)]
    fn revision(&self) -> u64 {
        lock(&self.inner).revision
    }

    #[cfg(test)]
    fn dirty(&self) -> bool {
        lock(&self.inner).dirty
    }
}

fn write_file(path: &Path, snapshot: &State) -> Result<(), String> {
    let data = serde_json::to_vec_pretty(snapshot).map_err(|e| e.to_string())?;
    if let Some(dir) = path.parent() {
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            fs::DirBuilder::new()
                .recursive(true)
                .mode(0o700)
                .create(dir)
                .map_err(|e| e.to_string())?;
        }
        #[cfg(not(unix))]
        {
            fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
    }
    let tmp = path.with_extension("json.tmp");
    {
        let mut fh = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(&tmp)
            .map_err(|e| e.to_string())?;
        fh.write_all(&data).map_err(|e| e.to_string())?;
        fh.sync_all().map_err(|e| e.to_string())?;
    }
    fs::rename(&tmp, path).map_err(|e| {
        let _ = fs::remove_file(&tmp);
        e.to_string()
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{GameState, Objective};

    #[test]
    fn state_round_trip() {
        let dir = tempfile();
        let path = dir.join("state.json");
        let s = Persist::new(&path);
        let base = Utc::now();
        s.update(|st| {
            st.challenge_done
                .insert("Kill 500 zombies|2026-08-14".into());
            st.run = Some(RunState {
                started_at: base - chrono::Duration::minutes(15),
                game_pid: 4242,
                game_started_at: Some(base - chrono::Duration::hours(1)),
            });
        });
        s.append_xp_sample(XpSample {
            at: base,
            cumulative: 1000,
            source: "df_exptotal".into(),
        });
        s.save().unwrap();

        let loaded = Persist::new(&path);
        loaded.load().unwrap();
        let got = loaded.get();
        assert!(got.challenge_done.contains("Kill 500 zombies|2026-08-14"));
        assert_eq!(got.xp_samples.len(), 1);
        assert_eq!(got.xp_samples[0].cumulative, 1000);
        let run = got.run.expect("run");
        assert_eq!(run.game_pid, 4242);
        assert!(run.matches(GameState {
            running: true,
            pid: 4242,
            started_at: Some(base - chrono::Duration::hours(1)),
        }));
    }

    #[test]
    fn challenge_done_is_written_as_a_set_and_read_from_the_old_map() {
        let dir = tempfile();
        let path = dir.join("state.json");
        let s = Persist::new(&path);
        s.update(|st| {
            st.challenge_done.insert("a|2026-08-14".into());
        });
        s.save().unwrap();
        let raw: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
        assert_eq!(raw["challenge_done"], serde_json::json!(["a|2026-08-14"]));

        fs::write(
            &path,
            r#"{"schema_version":1,"challenge_done":{"a|2026-08-14":true,"b|2026-08-14":false}}"#,
        )
        .unwrap();
        let old = Persist::new(&path);
        old.load().unwrap();
        assert_eq!(
            old.get().challenge_done,
            HashSet::from(["a|2026-08-14".to_string()]),
            "pre-0.4.13 map shape, false entries dropped"
        );
        assert!(fs::metadata(&path).is_ok(), "not quarantined");
    }

    #[test]
    fn missing_file_is_a_fresh_start() {
        let s = Persist::new(tempfile().join("absent.json"));
        s.load().unwrap();
        let got = s.get();
        assert!(got.xp_samples.is_empty());
        assert!(got.run.is_none());
    }

    #[test]
    fn corrupt_file_is_quarantined() {
        let dir = tempfile();
        let path = dir.join("state.json");
        fs::write(&path, "{ not json").unwrap();
        let s = Persist::new(&path);
        s.load().unwrap();
        assert!(fs::metadata(&path).is_err());
        let matches: Vec<_> = fs::read_dir(&dir)
            .unwrap()
            .flatten()
            .filter(|e| {
                e.file_name()
                    .to_string_lossy()
                    .starts_with("state.json.corrupt-")
            })
            .collect();
        assert_eq!(matches.len(), 1);

        fs::write(
            &path,
            r#"{"schema_version":0,"challenge_done":{"x|1":true}}"#,
        )
        .unwrap();
        let s2 = Persist::new(&path);
        s2.load().unwrap();
        assert!(s2.get().challenge_done.is_empty());
    }

    #[test]
    fn xp_samples_accumulate_until_reset() {
        let s = Persist::new("");
        let base = Utc::now();
        for i in 0..10 {
            s.append_xp_sample(XpSample {
                at: base + chrono::Duration::seconds(i),
                cumulative: 1000 + i * 10,
                source: "df_exptotal".into(),
            });
        }
        let got = s.get().xp_samples;
        assert_eq!(got.last().unwrap().cumulative, 1090);
        assert_eq!(got.len(), 10);
    }

    #[test]
    fn xp_source_change_resets_the_window() {
        let s = Persist::new("");
        let base = Utc::now();
        for i in 0..3 {
            s.append_xp_sample(XpSample {
                at: base + chrono::Duration::seconds(i),
                cumulative: 1_000_000 + i * 100,
                source: "df_exptotal".into(),
            });
        }
        assert_eq!(s.get().xp_samples.len(), 3);
        s.append_xp_sample(XpSample {
            at: base + chrono::Duration::seconds(3),
            cumulative: 999_000,
            source: "exp table reconstruction".into(),
        });
        let got = s.get().xp_samples;
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].source, "exp table reconstruction");
    }

    #[test]
    fn reset_xp_window_clears() {
        let s = Persist::new("");
        s.append_xp_sample(XpSample {
            at: Utc::now(),
            cumulative: 5,
            source: "df_exptotal".into(),
        });
        s.reset_xp_window("death");
        assert!(s.get().xp_samples.is_empty());
        s.reset_xp_window("death");
    }

    #[test]
    fn save_is_debounced() {
        let dir = tempfile();
        let path = dir.join("state.json");
        let s = Persist::new(&path);
        let now = Utc::now();
        s.set_now(now);
        s.update(|st| {
            st.challenge_done.insert("a|2026-08-14".into());
        });
        s.maybe_save().unwrap();
        let first = fs::metadata(&path).unwrap().len();

        s.update(|st| {
            st.challenge_done.insert("b|2026-08-14".into());
        });
        s.maybe_save().unwrap();
        assert_eq!(fs::metadata(&path).unwrap().len(), first);

        s.set_now(now + chrono::Duration::seconds(31));
        s.maybe_save().unwrap();
        assert_ne!(fs::metadata(&path).unwrap().len(), first);

        let third = fs::metadata(&path).unwrap().len();
        s.set_now(now + chrono::Duration::seconds(62));
        s.maybe_save().unwrap();
        assert_eq!(fs::metadata(&path).unwrap().len(), third);
    }

    #[test]
    fn save_due_tracks_dirty_and_the_debounce() {
        let dir = tempfile();
        let s = Persist::new(dir.join("state.json"));
        let now = Utc::now();
        s.set_now(now);
        assert_eq!(
            s.save_due_in(),
            None,
            "clean: the saver has nothing to wait for"
        );
        s.update(|st| {
            st.challenge_done.insert("a|2026-08-14".into());
        });
        assert_eq!(
            s.save_due_in(),
            Some(Duration::ZERO),
            "never saved: due now"
        );
        s.maybe_save().unwrap();
        assert_eq!(s.save_due_in(), None);
        s.update(|st| {
            st.challenge_done.insert("b|2026-08-14".into());
        });
        assert_eq!(s.save_due_in(), Some(SAVE_INTERVAL));
        s.set_now(now + chrono::Duration::seconds(10));
        assert_eq!(
            s.save_due_in(),
            Some(SAVE_INTERVAL - Duration::from_secs(10))
        );
        s.set_now(now + chrono::Duration::seconds(31));
        assert_eq!(s.save_due_in(), Some(Duration::ZERO));
    }

    #[test]
    fn saver_thread_writes_on_update_and_exits_on_poke() {
        use std::sync::Arc;
        let dir = tempfile();
        let path = dir.join("state.json");
        let s = Arc::new(Persist::new(&path));
        let stop = Arc::new(AtomicBool::new(false));
        let saver = {
            let s = s.clone();
            let stop = stop.clone();
            std::thread::spawn(move || s.run_saver(&stop))
        };
        assert!(
            fs::metadata(&path).is_err(),
            "nothing dirty, nothing written"
        );
        s.update(|st| {
            st.challenge_done.insert("a|2026-08-14".into());
        });
        let started = std::time::Instant::now();
        while fs::metadata(&path).is_err() {
            assert!(
                started.elapsed() < Duration::from_secs(5),
                "the saver never woke for the update"
            );
            std::thread::sleep(Duration::from_millis(5));
        }
        // A second update sits inside the debounce: the saver must block on
        // its timeout, then still exit promptly when stopped.
        s.update(|st| {
            st.challenge_done.insert("b|2026-08-14".into());
        });
        std::thread::sleep(Duration::from_millis(20));
        assert_eq!(s.save_due_in().map(|d| d.is_zero()), Some(false));
        let stopping = std::time::Instant::now();
        stop.store(true, Ordering::SeqCst);
        s.poke();
        saver.join().unwrap();
        assert!(stopping.elapsed() < Duration::from_secs(2));
    }

    #[test]
    fn update_during_save_remains_dirty() {
        let s = Persist::new("");
        s.update(|st| {
            st.challenge_done.insert("before".into());
        });
        let saved = s.revision();
        s.update(|st| {
            st.challenge_done.insert("during".into());
        });
        s.mark_saved(saved);
        assert!(s.dirty());
        s.mark_saved(s.revision());
        assert!(!s.dirty());
    }

    #[test]
    fn get_returns_a_copy() {
        let s = Persist::new("");
        s.update(|st| {
            st.xp_samples.push(XpSample {
                at: Utc::now(),
                cumulative: 1000,
                source: "original".into(),
            });
            st.challenge_done.insert("x".into());
            st.run = Some(RunState {
                game_pid: 42,
                ..RunState::default()
            });
        });
        let mut got = s.get();
        got.xp_samples[0].source = "mutated".into();
        got.challenge_done.remove("x");
        got.run.as_mut().unwrap().game_pid = 99;
        let inside = s.get();
        assert_eq!(inside.xp_samples[0].source, "original");
        assert!(inside.challenge_done.contains("x"));
        assert_eq!(inside.run.unwrap().game_pid, 42);
    }

    #[test]
    fn remember_challenge_board_latches_completion() {
        let s = Persist::new("");
        let end = Utc::now();
        let live = Challenge {
            name: "Travel".into(),
            end,
            objectives: vec![Objective {
                target: 100,
                score: Some(100),
                ..Objective::default()
            }],
            ..Challenge::default()
        };
        let board = s.remember_challenge_board(vec![live.clone()]);
        assert!(board[0].complete());
        assert!(
            s.get()
                .challenge_done
                .contains(&challenges::cycle_key(&live))
        );

        let uncompleted = Challenge {
            name: "Travel".into(),
            end,
            objectives: vec![Objective {
                target: 200,
                score: Some(100),
                ..Objective::default()
            }],
            ..Challenge::default()
        };
        let board = s.remember_challenge_board(vec![uncompleted]);
        assert!(board[0].complete());
        assert!(!board[0].live_complete());
    }

    #[test]
    fn remember_challenge_board_forgets_ended_cycles() {
        let s = Persist::new("");
        // Fixed, mid-day: the rule is in whole UTC dates, so the wall clock
        // must not decide which side of the boundary "a day ago" lands on.
        let now = "2026-09-06T12:00:00Z".parse::<DateTime<Utc>>().unwrap();
        s.set_now(now);
        let done = |name: &str, end: DateTime<Utc>| Challenge {
            name: name.into(),
            end,
            objectives: vec![Objective {
                target: 1,
                score: Some(1),
                ..Objective::default()
            }],
            ..Challenge::default()
        };
        let old = done("Old", now - chrono::Duration::days(2));
        let recent = done("Recent", now - chrono::Duration::days(1));
        let live = done("Live", now + chrono::Duration::days(3));
        let dateless = done("Dateless", DateTime::<Utc>::UNIX_EPOCH);
        s.remember_challenge_board(vec![
            old.clone(),
            recent.clone(),
            live.clone(),
            dateless.clone(),
        ]);
        let keys = |cs: &[&Challenge]| -> HashSet<String> {
            cs.iter().map(|c| challenges::cycle_key(c)).collect()
        };
        assert_eq!(
            s.get().challenge_done,
            keys(&[&old, &recent, &live, &dateless]),
            "everything on the board is kept, however old"
        );

        // The board moved on: only the live cycle is still listed.
        let revision = s.revision();
        let board = s.remember_challenge_board(vec![live.clone()]);
        assert!(board[0].complete());
        assert_eq!(
            s.get().challenge_done,
            keys(&[&recent, &live, &dateless]),
            "ended two days ago: gone; yesterday: kept; no date: kept"
        );
        assert_eq!(s.revision(), revision + 1, "one write for the prune");

        s.remember_challenge_board(vec![live]);
        assert_eq!(s.revision(), revision + 1, "nothing to do, nothing dirtied");
    }

    fn tempfile() -> PathBuf {
        static N: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let dir = std::env::temp_dir().join(format!(
            "df-hud-state-{}-{}",
            std::process::id(),
            N.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        fs::create_dir_all(&dir).unwrap();
        dir
    }
}
