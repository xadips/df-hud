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

const FORBIDDEN: &[&str] = &["hunger", "itemspawn", "modify_values"];

#[derive(Clone, Debug)]
pub struct Credentials {
    pub user_id: String,
    pub password: String,
    pub sc: String,
}

#[derive(Debug)]
pub struct StatusError {
    pub status: String,
}

impl std::fmt::Display for StatusError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "df: server returned status={}", self.status)
    }
}

impl std::error::Error for StatusError {}

#[derive(Debug)]
pub struct StaleCredentials;

impl std::fmt::Display for StaleCredentials {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "df: credentials rejected (sc likely stale)")
    }
}

impl std::error::Error for StaleCredentials {}

pub fn is_stale(err: &dyn std::error::Error) -> bool {
    let msg = err.to_string();
    msg.contains("credentials rejected")
        || msg.contains("value_mismatch")
        || msg.contains("missing_value")
}

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
    format!("{:x}", hasher.finalize())
}

pub fn signed_body(salt: &str, params: &[Param]) -> String {
    let body = encode_params(params);
    format!("hash={}&{}", hash_body(salt, &body), body)
}

pub fn parse_flash(body: &str) -> Result<Vars, Box<dyn std::error::Error>> {
    if let Some(rest) = body.strip_prefix("status=") {
        let status = rest.split('&').next().unwrap_or(rest);
        if status == "value_mismatch" || status == "missing_value" {
            return Err(format!("{StaleCredentials} (status={status})").into());
        }
        return Err(Box::new(StatusError {
            status: status.to_string(),
        }));
    }
    let mut out = Vars::new();
    for seg in body.split('&') {
        if let Some((k, v)) = seg.split_once('=') {
            out.insert(k.to_string(), v.to_string());
        }
    }
    if out.is_empty() {
        return Err("df: response contained no key=value pairs".into());
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

fn numeric_id(id: &str) -> bool {
    let n = id.len();
    (1..=20).contains(&n) && id.bytes().all(|b| b.is_ascii_digit())
}

fn record_looks_real(vars: &Vars) -> bool {
    vars.get("df_level").map(|s| !s.is_empty()).unwrap_or(false)
        && vars
            .get("id_member")
            .map(|s| !s.is_empty())
            .unwrap_or(false)
}

fn allowed(endpoint: &str) -> bool {
    endpoint == GET_VALUES || endpoint == LOAD_CHALLENGE
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
            ureq::AgentBuilder::new()
                .timeout(Duration::from_secs(10))
                .build(),
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
    ) -> Result<Vars, Box<dyn std::error::Error>> {
        if !allowed(endpoint) {
            return Err(format!(
                "df: endpoint {endpoint:?} is not on the allowlist (never callable: {})",
                FORBIDDEN.join(", ")
            )
            .into());
        }
        if hashed && salt.is_empty() {
            return Err(
                "df: hashed call needs a signing salt (set df.skeygen or let the bridge report it)"
                    .into(),
            );
        }
        let mut body = encode_params(params);
        if hashed {
            body = signed_body(salt, params);
        }
        let url = format!("{}/{endpoint}.php", self.base_url);
        let mut req = self
            .agent
            .post(&url)
            .set("Content-Type", "application/x-www-form-urlencoded")
            .set("User-Agent", &self.user_agent);
        if !self.cookie.is_empty() {
            req = req.set("Cookie", &self.cookie);
        }
        let resp = req.send_string(&body)?;
        self.read_flash(resp, endpoint)
    }

    fn read_flash(
        &self,
        resp: ureq::Response,
        call: &str,
    ) -> Result<Vars, Box<dyn std::error::Error>> {
        if resp.status() != 200 {
            return Err(format!("df: {call}: HTTP {}", resp.status()).into());
        }
        let mut raw = Vec::new();
        resp.into_reader()
            .take(self.max_body)
            .read_to_end(&mut raw)?;
        let text = String::from_utf8_lossy(&raw).into_owned();
        if looks_like_html(&text) {
            return Err(format!(
                "df: {call}: got an HTML page instead of data{}",
                describe_html(&text)
            )
            .into());
        }
        parse_flash(&text).map_err(|e| format!("df: {call}: {e}").into())
    }

    pub fn get_values(&self, cr: &Credentials) -> Result<Vars, Box<dyn std::error::Error>> {
        if !self.public_failed.load(Ordering::SeqCst) {
            match self.get_values_public(&cr.user_id) {
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

    fn get_values_public(&self, user_id: &str) -> Result<Vars, Box<dyn std::error::Error>> {
        if !numeric_id(user_id) {
            return Err(format!("{user_id:?} is not a user id").into());
        }
        let url = format!("{}/{GET_VALUES}.php?userID={user_id}", self.base_url);
        let resp = self
            .agent
            .get(&url)
            .set("User-Agent", &self.user_agent)
            .call()?;
        self.read_flash(resp, GET_VALUES)
    }

    pub fn load_challenge(
        &self,
        cr: &Credentials,
        salt: &str,
    ) -> Result<Vars, Box<dyn std::error::Error>> {
        self.call(
            LOAD_CHALLENGE,
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
        let se = err.downcast_ref::<StatusError>().unwrap();
        assert_eq!(se.status, "no_results");

        for s in ["value_mismatch", "missing_value"] {
            let err = parse_flash(&format!("status={s}")).unwrap_err();
            assert!(
                err.to_string().contains("stale") || err.to_string().contains("rejected"),
                "{err}"
            );
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
        let err = c.call(LOAD_CHALLENGE, &[], true, "").unwrap_err();
        assert!(err.to_string().contains("salt"), "{err}");
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
        let got = last.lock().unwrap();
        assert!(
            got.body
                .starts_with("hash=7fe50eb1872ba13897d0b7cc8b83e5e4&"),
            "{}",
            got.body
        );
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

    #[test]
    fn no_forbidden_endpoints_in_source() {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
        for entry in std::fs::read_dir(&root).unwrap() {
            let path = entry.unwrap().path();
            if path.extension().and_then(|s| s.to_str()) != Some("rs") {
                continue;
            }
            let name = path.file_name().unwrap().to_string_lossy();
            if name == "dfclient.rs" {
                continue;
            }
            let src = std::fs::read_to_string(&path).unwrap();
            for lit in rust_string_literals(&src) {
                let segment = lit.rsplit('/').next().unwrap_or(&lit);
                for bad in FORBIDDEN {
                    assert!(
                        lit != *bad && segment != *bad,
                        "{}: forbidden endpoint {bad:?} appears as {lit:?}",
                        path.display()
                    );
                }
            }
        }
    }

    fn rust_string_literals(src: &str) -> Vec<String> {
        let mut out = Vec::new();
        let bytes = src.as_bytes();
        let mut i = 0;
        while i < bytes.len() {
            if bytes[i] == b'"' {
                i += 1;
                let mut s = String::new();
                while i < bytes.len() && bytes[i] != b'"' {
                    if bytes[i] == b'\\' && i + 1 < bytes.len() {
                        s.push(bytes[i + 1] as char);
                        i += 2;
                        continue;
                    }
                    s.push(bytes[i] as char);
                    i += 1;
                }
                out.push(s);
            }
            i += 1;
        }
        out
    }
}
