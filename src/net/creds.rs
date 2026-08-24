//! Credential store. The bridge payload is account-equivalent.
//!
//! Discipline:
//! - the listener binds loopback only
//! - request bodies are never logged
//! - the on-disk file is 0600 (re-verified after write)
//! - Display / Debug / JSON all redact

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::fmt;
use std::fs;
use std::io::Write;
use std::path::PathBuf;
use std::sync::Mutex;

const SCHEMA_VERSION: i32 = 1;

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Credentials {
    pub user_id: String,
    pub password: String,
    pub sc: String,
    pub cookie: String,
}

impl Credentials {
    pub fn valid(&self) -> bool {
        !self.user_id.is_empty() && !self.password.is_empty() && !self.sc.is_empty()
    }

    pub fn to_df(&self) -> crate::net::dfclient::Credentials {
        crate::net::dfclient::Credentials {
            user_id: self.user_id.clone(),
            password: self.password.clone(),
            sc: self.sc.clone(),
        }
    }
}

impl fmt::Display for Credentials {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if !self.valid() {
            return write!(f, "Credentials{{unset}}");
        }
        write!(
            f,
            "Credentials{{UserID:{} Password:{} SC:{} Cookie:{}}}",
            redact(&self.user_id),
            redact(&self.password),
            redact(&self.sc),
            redact(&self.cookie)
        )
    }
}

impl Serialize for Credentials {
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        use serde::ser::SerializeStruct;
        let mut st = s.serialize_struct("Credentials", 4)?;
        st.serialize_field("userID", &redact(&self.user_id))?;
        st.serialize_field("password", &redact(&self.password))?;
        st.serialize_field("sc", &redact(&self.sc))?;
        st.serialize_field("cookie", &redact(&self.cookie))?;
        st.end()
    }
}

fn redact(s: &str) -> String {
    if s.is_empty() {
        return String::new();
    }
    if s.len() <= 4 {
        return "[redacted]".into();
    }
    format!("{}…[redacted {}ch]", &s[..2], s.len())
}

#[derive(Serialize, Deserialize)]
struct CredsFile {
    schema_version: i32,
    #[serde(rename = "userID")]
    user_id: String,
    password: String,
    sc: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    cookie: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    skeygen: String,
    updated_at: DateTime<Utc>,
}

struct Inner {
    creds: Credentials,
    salt: String,
    updated_at: Option<DateTime<Utc>>,
}

pub struct Store {
    path: PathBuf,
    inner: Mutex<Inner>,
}

impl fmt::Display for Store {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let g = self.inner.lock().unwrap();
        write!(
            f,
            "credStore{{path:{} creds:{} salt:{} updated:{}}}",
            self.path.display(),
            g.creds,
            redact(&g.salt),
            g.updated_at
                .map_or_else(|| "never".into(), |t| t.to_rfc3339())
        )
    }
}

impl fmt::Debug for Store {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{self}")
    }
}

impl Store {
    pub fn new(path: impl Into<PathBuf>) -> Self {
        Self {
            path: path.into(),
            inner: Mutex::new(Inner {
                creds: Credentials::default(),
                salt: String::new(),
                updated_at: None,
            }),
        }
    }

    pub fn load(&self) -> Result<(), String> {
        let data = match fs::read(&self.path) {
            Ok(d) => d,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(()),
            Err(err) => return Err(err.to_string()),
        };
        let parsed: Result<CredsFile, _> = serde_json::from_slice(&data);
        let file = match parsed {
            Ok(f) if f.schema_version == SCHEMA_VERSION => f,
            _ => {
                let quarantine =
                    format!("{}.corrupt-{}", self.path.display(), Utc::now().timestamp());
                let _ = fs::rename(&self.path, &quarantine);
                return Err(format!(
                    "credentials file unusable, moved to {quarantine}; waiting for the browser bridge"
                ));
            }
        };
        let mut g = self.inner.lock().unwrap();
        g.creds = Credentials {
            user_id: file.user_id,
            password: file.password,
            sc: file.sc,
            cookie: file.cookie,
        };
        g.salt = file.skeygen;
        g.updated_at = Some(file.updated_at);
        Ok(())
    }

    /// Returns whether anything actually changed.
    pub fn set(&self, c: Credentials, skeygen: &str) -> Result<bool, String> {
        if !c.valid() {
            return Err("refusing to store an incomplete credential triple".into());
        }
        let (changed, snapshot) = {
            let mut g = self.inner.lock().unwrap();
            let mut changed = g.creds != c;
            if !skeygen.is_empty() && skeygen != g.salt {
                changed = true;
                g.salt = skeygen.to_string();
            }
            g.creds = c.clone();
            g.updated_at = Some(Utc::now());
            (
                changed,
                CredsFile {
                    schema_version: SCHEMA_VERSION,
                    user_id: c.user_id,
                    password: c.password,
                    sc: c.sc,
                    cookie: c.cookie,
                    skeygen: g.salt.clone(),
                    updated_at: g.updated_at.unwrap(),
                },
            )
        };
        if self.path.as_os_str().is_empty() {
            return Ok(changed);
        }
        self.save(&snapshot)?;
        Ok(changed)
    }

    fn save(&self, file: &CredsFile) -> Result<(), String> {
        let data = serde_json::to_vec_pretty(file).map_err(|e| e.to_string())?;
        if let Some(dir) = self.path.parent() {
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
        let tmp = self.path.with_extension("json.tmp");
        {
            let mut fh = fs::OpenOptions::new()
                .write(true)
                .create(true)
                .truncate(true)
                .open(&tmp)
                .map_err(|e| e.to_string())?;
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                fh.set_permissions(fs::Permissions::from_mode(0o600))
                    .map_err(|e| e.to_string())?;
            }
            fh.write_all(&data).map_err(|e| e.to_string())?;
            fh.sync_all().map_err(|e| e.to_string())?;
        }
        fs::rename(&tmp, &self.path).map_err(|e| e.to_string())?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let meta = fs::metadata(&self.path).map_err(|e| e.to_string())?;
            let perm = meta.permissions().mode() & 0o777;
            if perm != 0o600 {
                return Err(format!("credentials file is mode {perm:o}, want 600"));
            }
        }
        Ok(())
    }

    pub fn get(&self) -> Option<(Credentials, String)> {
        let g = self.inner.lock().unwrap();
        if g.creds.valid() {
            Some((g.creds.clone(), g.salt.clone()))
        } else {
            None
        }
    }

    pub fn salt(&self) -> String {
        self.inner.lock().unwrap().salt.clone()
    }

    pub fn updated_at(&self) -> Option<DateTime<Utc>> {
        self.inner.lock().unwrap().updated_at
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_file_is_ok() {
        let s = Store::new("/no/such/df-hud-credentials.json");
        s.load().unwrap();
        assert!(s.get().is_none());
    }

    #[test]
    fn set_persists_and_redacts() {
        let dir = tempfile();
        let path = dir.join("credentials.json");
        let s = Store::new(&path);
        let secret = "3a7bd3e2360a3d29eea436fcfb7e44c735d117c4";
        let changed = s
            .set(
                Credentials {
                    user_id: "1234567".into(),
                    password: secret.into(),
                    sc: "0f9a1c4e8b2d6f3a7c5e9b1d4f8a2c6e".into(),
                    cookie: "session=abc".into(),
                },
                "salt-from-page",
            )
            .unwrap();
        assert!(changed);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
        }
        let raw = fs::read_to_string(&path).unwrap();
        assert!(raw.contains("1234567"));
        assert!(raw.contains("salt-from-page"));
        let text = format!("{}", s);
        assert!(
            !text.contains(secret),
            "Display leaked the password: {text}"
        );
        assert!(text.contains("[redacted"));
        let json = serde_json::to_string(&s.get().unwrap().0).unwrap();
        assert!(!json.contains(secret), "JSON leaked the password: {json}");
    }

    #[test]
    fn corrupt_is_quarantined() {
        let dir = tempfile();
        let path = dir.join("credentials.json");
        fs::write(&path, "not json").unwrap();
        let s = Store::new(&path);
        let err = s.load().unwrap_err();
        assert!(err.contains("unusable"));
        assert!(!path.exists());
    }

    fn tempfile() -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "df-hud-creds-{}-{}",
            std::process::id(),
            Utc::now().timestamp_nanos_opt().unwrap_or(0)
        ));
        fs::create_dir_all(&dir).unwrap();
        dir
    }
}
