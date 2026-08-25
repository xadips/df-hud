//! Persistent HUD state: things that cannot be recovered from the server.
//! The run clock, XP sample ring, and challenge-done
//! memory survive a restart because they cannot be recovered from the server.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::Duration;

use crate::model::{RunState, XpSample};

const SCHEMA_VERSION: i32 = 1;
const SAVE_INTERVAL: Duration = Duration::from_secs(30);

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct State {
    pub schema_version: i32,
    pub saved_at: Option<DateTime<Utc>>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub xp_samples: Vec<XpSample>,
    #[serde(default, skip_serializing_if = "HashMap::is_empty")]
    pub challenge_done: HashMap<String, bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run: Option<RunState>,
}

pub struct Store {
    path: PathBuf,
    inner: Mutex<Inner>,
}

struct Inner {
    state: State,
    dirty: bool,
    revision: u64,
    last_save: Option<DateTime<Utc>>,
    now: Box<dyn Fn() -> DateTime<Utc> + Send>,
}

impl Store {
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
                let mut g = self.inner.lock().unwrap();
                g.state = st;
                Ok(())
            }
            _ => {
                let aside = format!("{}.corrupt-{}", self.path.display(), Utc::now().timestamp());
                fs::rename(&self.path, &aside).map_err(|e| {
                    format!("state file unusable and could not be moved aside: {e}")
                })?;
                eprintln!("state: file unusable, moved to {aside}, starting fresh");
                Ok(())
            }
        }
    }

    pub fn update(&self, fn_: impl FnOnce(&mut State)) {
        let mut g = self.inner.lock().unwrap();
        fn_(&mut g.state);
        g.state.schema_version = SCHEMA_VERSION;
        g.dirty = true;
        g.revision += 1;
    }

    pub fn get(&self) -> State {
        self.inner.lock().unwrap().state.clone()
    }

    pub fn maybe_save(&self) -> Result<(), String> {
        {
            let g = self.inner.lock().unwrap();
            let now = (g.now)();
            if !g.dirty {
                return Ok(());
            }
            if let Some(last) = g.last_save
                && now
                    .signed_duration_since(last)
                    .to_std()
                    .unwrap_or(Duration::ZERO)
                    < SAVE_INTERVAL
            {
                return Ok(());
            }
        }
        self.save()
    }

    pub fn save(&self) -> Result<(), String> {
        let (snapshot, revision, path) = {
            let mut g = self.inner.lock().unwrap();
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
        let mut g = self.inner.lock().unwrap();
        if g.revision == revision {
            g.dirty = false;
        }
        g.last_save = Some((g.now)());
    }

    pub fn append_xp_sample(&self, sample: XpSample, window: Duration) {
        self.update(|st| {
            if let Some(prev) = st.xp_samples.last()
                && prev.source != sample.source
            {
                eprintln!(
                    "state: cumulative XP source changed from {} to {}; resetting the rate window",
                    prev.source, sample.source
                );
                st.xp_samples.clear();
            }
            st.xp_samples.push(sample.clone());
            let cutoff = sample.at
                - chrono::Duration::from_std(window).unwrap_or(chrono::Duration::hours(1));
            if let Some(keep) = st.xp_samples.iter().position(|s| s.at >= cutoff) {
                st.xp_samples.drain(..keep);
            }
        });
    }

    pub fn reset_xp_window(&self, reason: &str) {
        self.update(|st| {
            if st.xp_samples.is_empty() {
                return;
            }
            eprintln!("state: resetting the XP rate window ({reason})");
            st.xp_samples.clear();
        });
    }

    pub fn remember_challenge_board(
        &self,
        mut board: Vec<crate::model::Challenge>,
    ) -> Vec<crate::model::Challenge> {
        let done = self.get().challenge_done;
        let newly = crate::data::challenges::apply_sticky(&mut board, &done);
        if !newly.is_empty() {
            self.update(|st| {
                for key in newly {
                    st.challenge_done.insert(key, true);
                }
            });
        }
        board
    }

    #[cfg(test)]
    fn set_now(&self, at: DateTime<Utc>) {
        self.inner.lock().unwrap().now = Box::new(move || at);
    }

    #[cfg(test)]
    fn revision(&self) -> u64 {
        self.inner.lock().unwrap().revision
    }

    #[cfg(test)]
    fn dirty(&self) -> bool {
        self.inner.lock().unwrap().dirty
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
    use crate::model::{Challenge, GameState, Objective};

    #[test]
    fn state_round_trip() {
        let dir = tempfile();
        let path = dir.join("state.json");
        let s = Store::new(&path);
        let base = Utc::now();
        s.update(|st| {
            st.challenge_done
                .insert("Kill 500 zombies|2026-08-14".into(), true);
            st.run = Some(RunState {
                started_at: base - chrono::Duration::minutes(15),
                game_pid: 4242,
                game_started_at: Some(base - chrono::Duration::hours(1)),
            });
        });
        s.append_xp_sample(
            XpSample {
                at: base,
                cumulative: 1000,
                source: "df_exptotal".into(),
            },
            Duration::from_secs(60),
        );
        s.save().unwrap();

        let loaded = Store::new(&path);
        loaded.load().unwrap();
        let got = loaded.get();
        assert!(got.challenge_done["Kill 500 zombies|2026-08-14"]);
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
    fn missing_file_is_a_fresh_start() {
        let s = Store::new(tempfile().join("absent.json"));
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
        let s = Store::new(&path);
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
        let s2 = Store::new(&path);
        s2.load().unwrap();
        assert!(s2.get().challenge_done.is_empty());
    }

    #[test]
    fn xp_window_trims() {
        let s = Store::new("");
        let base = Utc::now();
        for i in 0..10 {
            s.append_xp_sample(
                XpSample {
                    at: base + chrono::Duration::seconds(i),
                    cumulative: 1000 + i * 10,
                    source: "df_exptotal".into(),
                },
                Duration::from_secs(5),
            );
        }
        let got = s.get().xp_samples;
        assert_eq!(got.last().unwrap().cumulative, 1090);
        let cutoff = got.last().unwrap().at - chrono::Duration::seconds(5);
        assert!(got.iter().all(|s| s.at >= cutoff));
        assert_eq!(got.len(), 6);
    }

    #[test]
    fn xp_source_change_resets_the_window() {
        let s = Store::new("");
        let base = Utc::now();
        for i in 0..3 {
            s.append_xp_sample(
                XpSample {
                    at: base + chrono::Duration::seconds(i),
                    cumulative: 10_000_000 + i * 100,
                    source: "df_exptotal".into(),
                },
                Duration::from_secs(3600),
            );
        }
        assert_eq!(s.get().xp_samples.len(), 3);
        s.append_xp_sample(
            XpSample {
                at: base + chrono::Duration::seconds(3),
                cumulative: 999_000,
                source: "exp table reconstruction".into(),
            },
            Duration::from_secs(3600),
        );
        let got = s.get().xp_samples;
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].source, "exp table reconstruction");
    }

    #[test]
    fn reset_xp_window_clears() {
        let s = Store::new("");
        s.append_xp_sample(
            XpSample {
                at: Utc::now(),
                cumulative: 5,
                source: "df_exptotal".into(),
            },
            Duration::from_secs(3600),
        );
        s.reset_xp_window("death");
        assert!(s.get().xp_samples.is_empty());
        s.reset_xp_window("death");
    }

    #[test]
    fn save_is_debounced() {
        let dir = tempfile();
        let path = dir.join("state.json");
        let s = Store::new(&path);
        let now = Utc::now();
        s.set_now(now);
        s.update(|st| {
            st.challenge_done.insert("a|2026-08-14".into(), true);
        });
        s.maybe_save().unwrap();
        let first = fs::metadata(&path).unwrap().len();

        s.update(|st| {
            st.challenge_done.insert("b|2026-08-14".into(), true);
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
    fn update_during_save_remains_dirty() {
        let s = Store::new("");
        s.update(|st| {
            st.challenge_done.insert("before".into(), true);
        });
        let saved = s.revision();
        s.update(|st| {
            st.challenge_done.insert("during".into(), true);
        });
        s.mark_saved(saved);
        assert!(s.dirty());
        s.mark_saved(s.revision());
        assert!(!s.dirty());
    }

    #[test]
    fn get_returns_a_copy() {
        let s = Store::new("");
        s.update(|st| {
            st.xp_samples.push(XpSample {
                at: Utc::now(),
                cumulative: 1000,
                source: "original".into(),
            });
            st.challenge_done.insert("x".into(), true);
            st.run = Some(RunState {
                game_pid: 42,
                ..RunState::default()
            });
        });
        let mut got = s.get();
        got.xp_samples[0].source = "mutated".into();
        got.challenge_done.insert("x".into(), false);
        got.run.as_mut().unwrap().game_pid = 99;
        let inside = s.get();
        assert_eq!(inside.xp_samples[0].source, "original");
        assert!(inside.challenge_done["x"]);
        assert_eq!(inside.run.unwrap().game_pid, 42);
    }

    #[test]
    fn remember_challenge_board_latches_completion() {
        let s = Store::new("");
        let end = Utc::now();
        let live = Challenge {
            name: "Travel".into(),
            end,
            objectives: vec![Objective {
                target: 100,
                score: 100,
                has_score: true,
                ..Objective::default()
            }],
            ..Challenge::default()
        };
        let board = s.remember_challenge_board(vec![live.clone()]);
        assert!(board[0].complete());
        assert!(s.get().challenge_done[&crate::data::challenges::cycle_key(&live)]);

        let uncompleted = Challenge {
            name: "Travel".into(),
            end,
            objectives: vec![Objective {
                target: 200,
                score: 100,
                has_score: true,
                ..Objective::default()
            }],
            ..Challenge::default()
        };
        let board = s.remember_challenge_board(vec![uncompleted]);
        assert!(board[0].complete());
        assert!(!board[0].live_complete());
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
