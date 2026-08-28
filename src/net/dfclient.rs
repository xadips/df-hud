//! Allowlisted Dead Frontier HTTP client. Wire format is the game's own:
//! unescaped `k=v&k=v` and Flash `&k=v` replies. Write endpoints are unreachable.

use md5::{Digest, Md5};
use std::collections::HashMap;
use std::io::Read;
use std::sync::atomic::{AtomicBool, Ordering};
#[cfg(test)]
use std::time::Duration;

const GET_VALUES: &str = "get_values";
const LOAD_CHALLENGE: &str = "hotrods/load_challenge";
const LOAD_MASTERIES: &str = "hotrods/load_masteries";

const FORBIDDEN: &[&str] = &["hunger", "itemspawn", "modify_values"];

#[derive(Clone, Debug)]
pub struct Credentials {
    pub user_id: String,
    pub password: String,
    pub sc: String,
}

/// Split by what the caller does: `Stale` pauses the pollers until the bridge
/// delivers a fresh session, everything else is retried with backoff.
#[derive(Debug)]
pub enum Error {
    /// `status=value_mismatch` or `missing_value`: the credentials are dead
    /// and retrying them invites rate limiting.
    Stale { status: String },
    /// Transport, HTTP, HTML, other `status=` refusals. Worded for the log.
    Other(String),
}

impl Error {
    pub fn stale(&self) -> bool {
        matches!(self, Error::Stale { .. })
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::Stale { status } => {
                write!(
                    f,
                    "df: credentials rejected (sc likely stale, status={status})"
                )
            }
            Error::Other(msg) => write!(f, "df: {msg}"),
        }
    }
}

impl std::error::Error for Error {}

pub type Vars = HashMap<String, String>;

#[derive(Clone, Debug)]
pub struct Param {
    pub key: String,
    pub value: String,
}

pub fn encode_params(params: &[Param]) -> String {
    let mut out = String::new();
    for (i, kv) in params.iter().enumerate() {
        if i > 0 {
            out.push('&');
        }
        out.push_str(&kv.key);
        out.push('=');
        out.push_str(&kv.value);
    }
    out
}

/// MD5(salt + each pair's second `=`-split field). JS `split("=")` then `[1]`.
pub fn hash_body(salt: &str, body: &str) -> String {
    let mut hasher = Md5::new();
    hasher.update(salt.as_bytes());
    for pair in body.split('&') {
        let mut fields = pair.split('=');
        let _key = fields.next();
        if let Some(second) = fields.next() {
            hasher.update(second.as_bytes());
        }
    }
    // digest 0.11 returns hybrid_array::Array, which has no LowerHex.
    hasher.finalize().iter().fold(String::new(), |mut s, b| {
        use std::fmt::Write;
        let _ = write!(s, "{b:02x}");
        s
    })
}

pub fn signed_body(salt: &str, params: &[Param]) -> String {
    let body = encode_params(params);
    format!("hash={}&{}", hash_body(salt, &body), body)
}

pub fn parse_flash(body: &str) -> Result<Vars, Error> {
    if let Some(rest) = body.strip_prefix("status=") {
        let status = rest.split('&').next().unwrap_or(rest);
        if status == "value_mismatch" || status == "missing_value" {
            return Err(Error::Stale {
                status: status.to_string(),
            });
        }
        return Err(Error::Other(format!("server returned status={status}")));
    }
    let mut out = Vars::new();
    for seg in body.split('&') {
        if let Some((k, v)) = seg.split_once('=') {
            out.insert(k.to_string(), v.to_string());
        }
    }
    if out.is_empty() {
        return Err(Error::Other("response contained no key=value pairs".into()));
    }
    Ok(out)
}

pub fn looks_like_html(body: &str) -> bool {
    let mut head = body.trim_start().to_ascii_lowercase();
    if head.len() > 512 {
        head.truncate(512);
    }
    head.starts_with("<!doctype")
        || head.starts_with("<html")
        || head.contains("<title>")
        || head.contains("cloudflare")
}

fn describe_html(body: &str) -> String {
    let lower = body.to_ascii_lowercase();
    if let Some(i) = lower.find("<title>") {
        let rest = &body[i + "<title>".len()..];
        let rest_l = rest.to_ascii_lowercase();
        if let Some(j) = rest_l.find("</title>") {
            return format!(" titled {:?}", excerpt(&rest[..j], 80));
        }
    }
    format!(": {}", excerpt(body, 160))
}

fn excerpt(body: &str, max: usize) -> String {
    let flat: String = body.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.len() > max {
        format!("{}...", &flat[..max])
    } else {
        flat
    }
}

/// What `get_values` accepts as an account id. Config validation shares this so
/// a rejected id is a startup error rather than a puzzling poll failure.
pub fn numeric_id(id: &str) -> bool {
    let n = id.len();
    (1..=20).contains(&n) && id.bytes().all(|b| b.is_ascii_digit())
}

fn record_looks_real(vars: &Vars) -> bool {
    vars.get("df_level").is_some_and(|s| !s.is_empty())
        && vars.get("id_member").is_some_and(|s| !s.is_empty())
}

fn allowed(endpoint: &str) -> bool {
    endpoint == GET_VALUES || endpoint == LOAD_CHALLENGE || endpoint == LOAD_MASTERIES
}

pub struct Client {
    agent: ureq::Agent,
    pub base_url: String,
    pub user_agent: String,
    pub max_body: u64,
    pub cookie: String,
    public_failed: AtomicBool,
}

impl Client {
    #[cfg(test)]
    pub fn new(base_url: &str, user_agent: &str) -> Self {
        Self::with_agent(
            ureq::Agent::new_with_config(
                ureq::Agent::config_builder()
                    .timeout_global(Some(Duration::from_secs(10)))
                    .http_status_as_error(false)
                    .build(),
            ),
            base_url,
            user_agent,
        )
    }

    pub fn with_agent(agent: ureq::Agent, base_url: &str, user_agent: &str) -> Self {
        Self {
            agent,
            base_url: base_url.trim_end_matches('/').to_string(),
            user_agent: user_agent.to_string(),
            max_body: 8 << 20,
            cookie: String::new(),
            public_failed: AtomicBool::new(false),
        }
    }

    #[cfg(test)]
    pub fn disable_public_get_values(&self) {
        self.public_failed.store(true, Ordering::SeqCst);
    }

    fn call(
        &self,
        endpoint: &str,
        params: &[Param],
        hashed: bool,
        salt: &str,
    ) -> Result<Vars, Error> {
        if !allowed(endpoint) {
            return Err(Error::Other(format!(
                "endpoint {endpoint:?} is not on the allowlist (never callable: {})",
                FORBIDDEN.join(", ")
            )));
        }
        if hashed && salt.is_empty() {
            return Err(Error::Other(
                "hashed call needs a signing salt (set df.skeygen or let the bridge report it)"
                    .into(),
            ));
        }
        let mut body = encode_params(params);
        if hashed {
            body = signed_body(salt, params);
        }
        let url = format!("{}/{endpoint}.php", self.base_url);
        let mut req = self
            .agent
            .post(&url)
            .header("Content-Type", "application/x-www-form-urlencoded")
            .header("User-Agent", &self.user_agent);
        if !self.cookie.is_empty() {
            req = req.header("Cookie", &self.cookie);
        }
        let resp = req
            .send(&body)
            .map_err(|e| Error::Other(format!("{endpoint}: {e}")))?;
        self.read_flash(resp, endpoint)
    }

    fn read_flash(
        &self,
        resp: ureq::http::Response<ureq::Body>,
        call: &str,
    ) -> Result<Vars, Error> {
        if resp.status() != 200 {
            return Err(Error::Other(format!("{call}: HTTP {}", resp.status())));
        }
        let mut raw = Vec::new();
        resp.into_body()
            .into_reader()
            .take(self.max_body)
            .read_to_end(&mut raw)
            .map_err(|e| Error::Other(format!("{call}: {e}")))?;
        let text = String::from_utf8_lossy(&raw).into_owned();
        if looks_like_html(&text) {
            return Err(Error::Other(format!(
                "{call}: got an HTML page instead of data{}",
                describe_html(&text)
            )));
        }
        parse_flash(&text).map_err(|err| match err {
            Error::Other(msg) => Error::Other(format!("{call}: {msg}")),
            stale => stale,
        })
    }

    pub fn get_values(&self, cr: &Credentials) -> Result<Vars, Error> {
        if !self.public_failed.load(Ordering::SeqCst) {
            match self.fetch_public(&cr.user_id) {
                Ok(vars) if record_looks_real(&vars) => return Ok(vars),
                Ok(_) | Err(_) => {
                    self.public_failed.store(true, Ordering::SeqCst);
                }
            }
        }
        self.call(
            GET_VALUES,
            &[
                Param {
                    key: "userID".into(),
                    value: cr.user_id.clone(),
                },
                Param {
                    key: "password".into(),
                    value: cr.password.clone(),
                },
                Param {
                    key: "sc".into(),
                    value: cr.sc.clone(),
                },
            ],
            false,
            "",
        )
    }

    /// `get_values` for a bare account id, with no session behind it. This is
    /// what `df.user_id` buys before the bridge has delivered anything: a real
    /// record, but a public one, so challenges stay out of reach.
    ///
    /// Unlike [`Self::get_values`] this ignores the `public_failed` latch. There
    /// is no authenticated call to fall back to here, so a failure is just a
    /// failure and the next poll should try again. An empty record is an error
    /// rather than a blank HUD, since the usual cause is a wrong `df.user_id`.
    pub fn get_values_public(&self, user_id: &str) -> Result<Vars, Error> {
        let vars = self.fetch_public(user_id)?;
        if !record_looks_real(&vars) {
            return Err(Error::Other(format!(
                "{GET_VALUES}: no public record for user id {user_id:?}; check df.user_id"
            )));
        }
        Ok(vars)
    }

    fn fetch_public(&self, user_id: &str) -> Result<Vars, Error> {
        if !numeric_id(user_id) {
            return Err(Error::Other(format!("{user_id:?} is not a user id")));
        }
        let url = format!("{}/{GET_VALUES}.php?userID={user_id}", self.base_url);
        let resp = self
            .agent
            .get(&url)
            .header("User-Agent", &self.user_agent)
            .call()
            .map_err(|e| Error::Other(format!("{GET_VALUES}: {e}")))?;
        self.read_flash(resp, GET_VALUES)
    }

    pub fn load_challenge(&self, cr: &Credentials, salt: &str) -> Result<Vars, Error> {
        self.load_hotrods(LOAD_CHALLENGE, cr, salt)
    }

    pub fn load_masteries(&self, cr: &Credentials, salt: &str) -> Result<Vars, Error> {
        self.load_hotrods(LOAD_MASTERIES, cr, salt)
    }

    /// The hotrods `action=get` shape both boards share: hashed POST with the
    /// session credentials, same signing salt.
    fn load_hotrods(&self, endpoint: &str, cr: &Credentials, salt: &str) -> Result<Vars, Error> {
        self.call(
            endpoint,
            &[
                Param {
                    key: "userID".into(),
                    value: cr.user_id.clone(),
                },
                Param {
                    key: "password".into(),
                    value: cr.password.clone(),
                },
                Param {
                    key: "sc".into(),
                    value: cr.sc.clone(),
                },
                Param {
                    key: "action".into(),
                    value: "get".into(),
                },
            ],
            true,
            salt,
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Write};
    use std::net::TcpListener;
    use std::sync::{Arc, Mutex};
    use std::thread;

    const TEST_SALT: &str = "y27bigaOAA1";

    #[test]
    fn hash_body_vectors() {
        let cases = [
            (
                "userID=999&password=hashhash&sc=sc123&action=get",
                "7fe50eb1872ba13897d0b7cc8b83e5e4",
            ),
            ("k1=a&k2=b=c", "adedaf7c7c63f8c975c6e129793aa786"),
            (
                "userID=999&password=hashhash&sc=sc123&action=get&extra=",
                "7fe50eb1872ba13897d0b7cc8b83e5e4",
            ),
        ];
        for (body, want) in cases {
            assert_eq!(hash_body(TEST_SALT, body), want, "body {body}");
        }
    }

    #[test]
    fn signed_body_shape() {
        let p = [
            Param {
                key: "userID".into(),
                value: "999".into(),
            },
            Param {
                key: "password".into(),
                value: "hashhash".into(),
            },
            Param {
                key: "sc".into(),
                value: "sc123".into(),
            },
            Param {
                key: "action".into(),
                value: "get".into(),
            },
        ];
        let got = signed_body(TEST_SALT, &p);
        let want = "hash=7fe50eb1872ba13897d0b7cc8b83e5e4&userID=999&password=hashhash&sc=sc123&action=get";
        assert_eq!(got, want);
        assert!(got.starts_with("hash="));
        assert!(got.ends_with(&encode_params(&p)));
    }

    #[test]
    fn encode_does_not_escape() {
        let p = [
            Param {
                key: "a".into(),
                value: "b c".into(),
            },
            Param {
                key: "d".into(),
                value: "e&f".into(),
            },
            Param {
                key: "g".into(),
                value: "h=i".into(),
            },
        ];
        assert_eq!(encode_params(&p), "a=b c&d=e&f&g=h=i");
    }

    #[test]
    fn parse_flash_cases() {
        let got = parse_flash("&df_level=415&df_cash=1000").unwrap();
        assert_eq!(got.len(), 2);
        assert_eq!(got["df_level"], "415");

        let got = parse_flash("&df_inv1_type=gun_stats14=2&df_level=1").unwrap();
        assert_eq!(got["df_inv1_type"], "gun_stats14=2");

        let got = parse_flash("&junk&df_level=415").unwrap();
        assert!(!got.contains_key("junk"));

        let got = parse_flash("&k=first&k=second").unwrap();
        assert_eq!(got["k"], "second");

        assert!(parse_flash("").is_err());

        let err = parse_flash("status=no_results&foo=bar").unwrap_err();
        assert!(!err.stale());
        assert!(err.to_string().contains("status=no_results"), "{err}");

        for s in ["value_mismatch", "missing_value"] {
            let err = parse_flash(&format!("status={s}")).unwrap_err();
            assert!(err.stale(), "{err}");
            assert!(err.to_string().contains("rejected"), "{err}");
        }
    }

    #[test]
    fn looks_like_html_cases() {
        for body in [
            "<!DOCTYPE html><html>",
            "<html><head>",
            "  <TITLE>Just a moment</TITLE>",
            "<div>attention required cloudflare</div>",
        ] {
            assert!(looks_like_html(body), "{body}");
        }
        assert!(!looks_like_html("&df_level=415&df_cash=10"));
    }

    #[test]
    fn client_allowlist() {
        let c = Client::new("http://127.0.0.1:1", "test");
        for call in FORBIDDEN {
            let err = c.call(call, &[], false, "").unwrap_err();
            assert!(err.to_string().contains("allowlist"), "{err}");
        }
        let err = c.call("get_storage", &[], false, "").unwrap_err();
        assert!(err.to_string().contains("allowlist"), "{err}");
    }

    #[test]
    fn hashed_call_needs_salt() {
        let c = Client::new("http://127.0.0.1:1", "test");
        for endpoint in [LOAD_CHALLENGE, LOAD_MASTERIES] {
            let err = c.call(endpoint, &[], true, "").unwrap_err();
            assert!(err.to_string().contains("salt"), "{err}");
        }
    }

    struct Hit {
        method: String,
        path: String,
        query: String,
        body: String,
        ua: String,
        cookie: String,
    }

    fn spawn(
        handler: impl Fn(&Hit) -> (u16, String) + Send + Sync + 'static,
    ) -> (String, thread::JoinHandle<()>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        listener.set_nonblocking(false).unwrap();
        let addr = listener.local_addr().unwrap();
        let handler = Arc::new(handler);
        let h = thread::spawn(move || {
            for _ in 0..32 {
                let Ok((mut stream, _)) = listener.accept() else {
                    break;
                };
                stream.set_read_timeout(Some(Duration::from_secs(2))).ok();
                let mut reader = BufReader::new(stream.try_clone().unwrap());
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() {
                    continue;
                }
                let parts: Vec<&str> = line.split_whitespace().collect();
                if parts.len() < 2 {
                    continue;
                }
                let method = parts[0].to_string();
                let raw_path = parts[1];
                let (path, query) = match raw_path.split_once('?') {
                    Some((p, q)) => (p.to_string(), q.to_string()),
                    None => (raw_path.to_string(), String::new()),
                };
                let mut headers = HashMap::new();
                let mut content_len = 0usize;
                loop {
                    line.clear();
                    if reader.read_line(&mut line).unwrap_or(0) == 0 {
                        break;
                    }
                    if line == "\r\n" || line == "\n" {
                        break;
                    }
                    if let Some((k, v)) = line.split_once(':') {
                        let key = k.trim().to_ascii_lowercase();
                        let val = v.trim().to_string();
                        if key == "content-length" {
                            content_len = val.parse().unwrap_or(0);
                        }
                        headers.insert(key, val);
                    }
                }
                let mut body = vec![0; content_len];
                if content_len > 0 {
                    let _ = std::io::Read::read_exact(&mut reader, &mut body);
                }
                let hit = Hit {
                    method,
                    path,
                    query,
                    body: String::from_utf8_lossy(&body).into_owned(),
                    ua: headers.get("user-agent").cloned().unwrap_or_default(),
                    cookie: headers.get("cookie").cloned().unwrap_or_default(),
                };
                let (status, body) = handler(&hit);
                let _ = write!(
                    stream,
                    "HTTP/1.1 {status} OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
            }
        });
        (format!("http://{addr}"), h)
    }

    #[test]
    fn client_against_fake_server() {
        let last: Arc<Mutex<Hit>> = Arc::new(Mutex::new(Hit {
            method: String::new(),
            path: String::new(),
            query: String::new(),
            body: String::new(),
            ua: String::new(),
            cookie: String::new(),
        }));
        let slot = last.clone();
        let (base, _) = spawn(move |hit| {
            *slot.lock().unwrap() = Hit {
                method: hit.method.clone(),
                path: hit.path.clone(),
                query: hit.query.clone(),
                body: hit.body.clone(),
                ua: hit.ua.clone(),
                cookie: hit.cookie.clone(),
            };
            (
                200,
                "&df_level=415&id_member=1&df_positionx=1054&df_positiony=987".into(),
            )
        });
        let c = Client::new(&base, "df-hud/test");
        let cr = Credentials {
            user_id: "999".into(),
            password: "hashhash".into(),
            sc: "sc123".into(),
        };
        let vars = c.get_values(&cr).unwrap();
        assert_eq!(vars["df_positionx"], "1054");
        {
            let got = last.lock().unwrap();
            assert_eq!(got.path, "/get_values.php");
            assert_eq!(got.ua, "df-hud/test");
            assert_eq!(got.method, "GET");
            assert!(!got.body.starts_with("hash="));
            assert!(got.query.contains("userID=999"));
        }
        c.load_challenge(&cr, TEST_SALT).unwrap();
        {
            let got = last.lock().unwrap();
            assert_eq!(got.path, "/hotrods/load_challenge.php");
            assert!(
                got.body
                    .starts_with("hash=7fe50eb1872ba13897d0b7cc8b83e5e4&"),
                "{}",
                got.body
            );
        }
        c.load_masteries(&cr, TEST_SALT).unwrap();
        let got = last.lock().unwrap();
        assert_eq!(got.path, "/hotrods/load_masteries.php");
        // Same params, same salt: the signature matches the challenge call.
        assert!(
            got.body
                .starts_with("hash=7fe50eb1872ba13897d0b7cc8b83e5e4&"),
            "{}",
            got.body
        );
        assert!(got.body.ends_with("&action=get"), "{}", got.body);
    }

    #[test]
    fn rejects_html_response() {
        let (base, _) = spawn(|_| {
            (
                200,
                "<!DOCTYPE html><html><title>Attention Required! | Cloudflare</title>".into(),
            )
        });
        let c = Client::new(&base, "test");
        let err = c
            .get_values(&Credentials {
                user_id: "1".into(),
                password: "2".into(),
                sc: "3".into(),
            })
            .unwrap_err();
        assert!(err.to_string().contains("HTML"), "{err}");
    }

    #[test]
    fn get_values_sends_no_credential_by_default() {
        let secret = "hunter2-password";
        let last: Arc<Mutex<Hit>> = Arc::new(Mutex::new(Hit {
            method: String::new(),
            path: String::new(),
            query: String::new(),
            body: String::new(),
            ua: String::new(),
            cookie: String::new(),
        }));
        let slot = last.clone();
        let (base, _) = spawn(move |hit| {
            *slot.lock().unwrap() = Hit {
                method: hit.method.clone(),
                path: hit.path.clone(),
                query: hit.query.clone(),
                body: hit.body.clone(),
                ua: hit.ua.clone(),
                cookie: hit.cookie.clone(),
            };
            (
                200,
                "&id_member=1234567&df_level=415&df_exptotal=10000000".into(),
            )
        });
        let mut c = Client::new(&base, "df-hud/test");
        c.cookie = "session=abc".into();
        let vars = c
            .get_values(&Credentials {
                user_id: "1234567".into(),
                password: secret.into(),
                sc: "sc-value".into(),
            })
            .unwrap();
        assert_eq!(vars["df_level"], "415");
        let got = last.lock().unwrap();
        assert_eq!(got.method, "GET");
        assert_eq!(got.body, "");
        for (what, s) in [("url", got.query.as_str()), ("body", got.body.as_str())] {
            for bad in [secret, "sc-value", "password"] {
                assert!(!s.contains(bad), "{what} carried {bad}: {s}");
            }
        }
        assert!(got.query.contains("userID=1234567"));
    }

    #[test]
    fn get_values_falls_back_to_authenticated_call() {
        let (base, _) = spawn(|hit| {
            if hit.method == "GET" {
                (404, "gone".into())
            } else {
                assert!(hit.body.contains("password=hunter2"), "{}", hit.body);
                (200, "&id_member=1&df_level=415".into())
            }
        });
        let c = Client::new(&base, "df-hud/test");
        c.get_values(&Credentials {
            user_id: "1".into(),
            password: "hunter2".into(),
            sc: "sc".into(),
        })
        .unwrap();
    }

    #[test]
    fn get_values_rejects_a_reply_that_is_not_a_record() {
        let posts = Arc::new(Mutex::new(0u32));
        let n = posts.clone();
        let (base, _) = spawn(move |hit| {
            if hit.method == "GET" {
                (200, "&something=else&unrelated=1".into())
            } else {
                *n.lock().unwrap() += 1;
                (200, "&id_member=1&df_level=415".into())
            }
        });
        let c = Client::new(&base, "df-hud/test");
        c.get_values(&Credentials {
            user_id: "1".into(),
            password: "p".into(),
            sc: "s".into(),
        })
        .unwrap();
        assert_eq!(*posts.lock().unwrap(), 1);
    }

    #[test]
    fn get_values_refuses_a_user_id_that_is_not_a_number() {
        let gets = Arc::new(Mutex::new(0u32));
        let n = gets.clone();
        let (base, _) = spawn(move |hit| {
            if hit.method == "GET" {
                *n.lock().unwrap() += 1;
            }
            (200, "&id_member=1&df_level=415".into())
        });
        let c = Client::new(&base, "df-hud/test");
        c.get_values(&Credentials {
            user_id: "1&evil=1".into(),
            password: "p".into(),
            sc: "s".into(),
        })
        .unwrap();
        assert_eq!(*gets.lock().unwrap(), 0);
    }

    #[test]
    fn credential_free_probe_is_tried_only_once() {
        let gets = Arc::new(Mutex::new(0u32));
        let posts = Arc::new(Mutex::new(0u32));
        let g = gets.clone();
        let p = posts.clone();
        let (base, _) = spawn(move |hit| {
            if hit.method == "GET" {
                *g.lock().unwrap() += 1;
                (404, "gone".into())
            } else {
                *p.lock().unwrap() += 1;
                (200, "&id_member=1&df_level=415".into())
            }
        });
        let c = Client::new(&base, "df-hud/test");
        let cr = Credentials {
            user_id: "1".into(),
            password: "p".into(),
            sc: "s".into(),
        };
        for _ in 0..5 {
            c.get_values(&cr).unwrap();
        }
        assert_eq!(*gets.lock().unwrap(), 1);
        assert_eq!(*posts.lock().unwrap(), 5);
    }
}
