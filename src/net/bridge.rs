//! Loopback HTTP/1.1 bridge. Request bodies are never logged.
//!
//! Loopback does not stop browsers: any webpage can POST here with a
//! preflight-free request shape, and DNS rebinding makes responses readable.
//! Both arrive with a web Origin or a non-loopback Host; no legitimate
//! client sends either (GM.xmlHttpRequest sends no Origin or an
//! extension-scheme one).

use serde_json::{Value, json};
use std::io::{BufRead, BufReader, Read, Write};
#[cfg(test)]
use std::net::SocketAddr;
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Instant;

use crate::app::groups;
use crate::config;
use crate::net::creds::{Credentials, Store as Creds};

const MAX_BODY: usize = 1 << 20;
/// Launch Standalone's POST can land after the 1s process scan has already
/// flipped `game_running`. A different account is accepted only in this window.
const LAUNCH_GRACE_SECS: i64 = 15;
pub const VERSION: &str = env!("CARGO_PKG_VERSION");
type Hook = Arc<dyn Fn() + Send + Sync>;
type WidgetToggleHook = Arc<dyn Fn(&str) -> Result<bool, String> + Send + Sync>;

#[derive(Clone, Default)]
pub struct Hooks {
    pub on_credentials: Option<Hook>,
    pub run_start: Option<Hook>,
    pub xp_reset: Option<Hook>,
    pub overlay_toggle: Option<Hook>,
    pub widget_toggle: Option<WidgetToggleHook>,
}

pub struct Server {
    #[cfg(test)]
    pub listen: SocketAddr,
    #[cfg(test)]
    stop: Arc<AtomicBool>,
}

#[cfg(test)]
impl Server {
    pub fn stop(&self) {
        self.stop.store(true, Ordering::SeqCst);
        let _ = TcpStream::connect(self.listen);
    }
}

pub fn start(
    addr: &str,
    creds: Arc<Creds>,
    hooks: Hooks,
    game_running: Arc<AtomicBool>,
    game_started_at: Arc<AtomicI64>,
) -> Result<Server, String> {
    config::validate_loopback(addr)?;
    let listener =
        TcpListener::bind(addr).map_err(|e| format!("bridge cannot listen on {addr}: {e}"))?;
    listener.set_nonblocking(false).map_err(|e| e.to_string())?;
    let listen = listener.local_addr().map_err(|e| e.to_string())?;
    eprintln!("bridge: listening on {listen} (waiting for a browser payload)");
    let stop = Arc::new(AtomicBool::new(false));
    let inner = Arc::new(Inner {
        creds,
        hooks: Mutex::new(hooks),
        started: Instant::now(),
        game_running,
        game_started_at,
        ignored_other_account: AtomicBool::new(false),
    });
    let stop2 = stop.clone();
    thread::Builder::new()
        .name("df-hud-bridge".into())
        .spawn(move || {
            for stream in listener.incoming() {
                if stop2.load(Ordering::SeqCst) {
                    break;
                }
                match stream {
                    Ok(s) => {
                        let inner = inner.clone();
                        thread::spawn(move || inner.serve(s));
                    }
                    Err(_) => break,
                }
            }
        })
        .map_err(|e| e.to_string())?;
    Ok(Server {
        #[cfg(test)]
        listen,
        #[cfg(test)]
        stop,
    })
}

struct Inner {
    creds: Arc<Creds>,
    hooks: Mutex<Hooks>,
    started: Instant,
    game_running: Arc<AtomicBool>,
    game_started_at: Arc<AtomicI64>,
    ignored_other_account: AtomicBool,
}

impl Inner {
    fn serve(&self, stream: TcpStream) {
        let _ = stream.set_read_timeout(Some(std::time::Duration::from_secs(10)));
        let mut stream = stream;
        let Ok(clone) = stream.try_clone() else {
            return;
        };
        let mut reader = BufReader::new(clone);
        let mut req = String::new();
        if reader.read_line(&mut req).is_err() {
            return;
        }
        let mut parts = req.split_whitespace();
        let method = parts.next().unwrap_or("").to_string();
        let path = parts.next().unwrap_or("").to_string();
        let mut content_len = 0usize;
        let mut content_type = String::new();
        let mut host = String::new();
        let mut origin = String::new();
        loop {
            let mut line = String::new();
            match reader.read_line(&mut line) {
                Ok(0) | Err(_) => return,
                Ok(_) => {}
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
                if key == "host" {
                    host = val.clone();
                }
                if key == "origin" {
                    origin = val.clone();
                }
                if key == "content-type" {
                    content_type = val;
                }
            }
        }
        if content_len > MAX_BODY {
            let _ = write_http(&mut stream, 413, "text/plain", b"payload too large");
            return;
        }
        let mut body = vec![0u8; content_len];
        if content_len > 0 && reader.read_exact(&mut body).is_err() {
            let _ = write_http(&mut stream, 400, "text/plain", b"truncated body");
            return;
        }
        // Refused only after draining the body: a close mid-upload turns into
        // a TCP reset and the sender never sees the 403.
        if let Some(reason) = deny_cross_origin(&host, &origin) {
            let _ = write_http(&mut stream, 403, "text/plain", reason.as_bytes());
            return;
        }
        let (status, ctype, payload) = self.handle(&method, &path, &content_type, &body);
        let _ = write_http(&mut stream, status, ctype, &payload);
    }

    fn handle(
        &self,
        method: &str,
        path: &str,
        ctype: &str,
        body: &[u8],
    ) -> (u16, &'static str, Vec<u8>) {
        match (method, path) {
            ("POST", "/api/userData") => self.user_data(ctype, body),
            ("GET", "/healthz") => self.health(),
            ("POST", "/api/run/start") => self.hook("run clock", |h| h.run_start.clone()),
            ("POST", "/api/xp/reset") => self.hook("xp rate", |h| h.xp_reset.clone()),
            ("POST", "/api/overlay/toggle") => self.hook("overlay", |h| h.overlay_toggle.clone()),
            (m, p) if m == "POST" && p.starts_with("/api/widget/") && p.ends_with("/toggle") => {
                let group = p
                    .trim_start_matches("/api/widget/")
                    .trim_end_matches("/toggle")
                    .trim_matches('/');
                self.widget(group)
            }
            _ => (404, "text/plain", b"not found".to_vec()),
        }
    }

    fn user_data(&self, ctype: &str, body: &[u8]) -> (u16, &'static str, Vec<u8>) {
        // Strict: an untyped Blob POST arrives with no Content-Type and no
        // preflight, and the userscript always sends application/json.
        if !ctype.starts_with("application/json") {
            return (415, "text/plain", b"expected application/json".to_vec());
        }
        let parsed: Value = if let Ok(v) = serde_json::from_slice(body) {
            v
        } else {
            eprintln!("bridge: rejected a payload: malformed JSON");
            return (400, "text/plain", b"malformed JSON".to_vec());
        };
        let vars = parsed.get("userVars").and_then(Value::as_object);
        let Some(vars) = vars else {
            eprintln!("bridge: payload missing userVars (are you on a logged-in page?)");
            return (
                400,
                "text/plain",
                b"userVars missing userID, password, sc".to_vec(),
            );
        };
        let cr = Credentials {
            user_id: coerce(vars.get("userID").unwrap_or(&Value::Null)),
            password: coerce(vars.get("password").unwrap_or(&Value::Null)),
            sc: coerce(vars.get("sc").unwrap_or(&Value::Null)),
            cookie: parsed
                .get("cookies")
                .map(coerce)
                .unwrap_or_default()
                .trim()
                .to_string(),
        };
        if !cr.valid() {
            let mut missing = Vec::new();
            if cr.user_id.is_empty() {
                missing.push("userID");
            }
            if cr.password.is_empty() {
                missing.push("password");
            }
            if cr.sc.is_empty() {
                missing.push("sc");
            }
            eprintln!(
                "bridge: payload missing {} (are you on a logged-in page?)",
                missing.join(", ")
            );
            return (
                400,
                "text/plain",
                format!("userVars missing {}", missing.join(", ")).into_bytes(),
            );
        }
        let salt = parsed.get("skeygen").map(coerce).unwrap_or_default();
        let source = parsed.get("source").map(coerce).unwrap_or_default();
        if !self.should_apply(&cr.user_id, &source) {
            if !self.ignored_other_account.swap(true, Ordering::SeqCst) {
                eprintln!("bridge: keeping the current account while the game is running");
            }
            return (
                200,
                "application/json",
                b"{\"ok\":true,\"applied\":false}".to_vec(),
            );
        }
        if let Ok(changed) = self.creds.set(cr, &salt) {
            if changed {
                let extra = if salt.is_empty() {
                    ""
                } else {
                    ", signing salt reported"
                };
                eprintln!("bridge: credentials updated from browser{extra}");
                if let Some(fn_) = self.hooks.lock().unwrap().on_credentials.clone() {
                    fn_();
                }
            }
            (200, "application/json", b"{\"ok\":true}".to_vec())
        } else {
            eprintln!("bridge: could not store credentials");
            (500, "text/plain", b"could not store credentials".to_vec())
        }
    }

    fn should_apply(&self, user_id: &str, source: &str) -> bool {
        let current = self.creds.get();
        apply_session(
            current.as_ref().map(|(c, _)| c.user_id.as_str()),
            user_id,
            source,
            self.game_running.load(Ordering::SeqCst),
            self.game_started_at.load(Ordering::SeqCst),
            chrono::Utc::now().timestamp(),
        )
    }

    fn health(&self) -> (u16, &'static str, Vec<u8>) {
        let have = self.creds.get().is_some();
        let have_salt = !self.creds.salt().is_empty();
        let game_running = self.game_running.load(Ordering::SeqCst);
        let mut obj = json!({
            "ok": true,
            "have_credentials": have,
            "have_signing_salt": have_salt,
            "session_locked": game_running && have,
            "uptime_seconds": self.started.elapsed().as_secs(),
            "version": VERSION,
        });
        if let Some(t) = self.creds.updated_at() {
            obj["credentials_age_seconds"] = json!((chrono::Utc::now() - t).num_seconds().max(0));
        }
        (
            200,
            "application/json",
            serde_json::to_vec(&obj).unwrap_or_else(|_| b"{\"ok\":true}".to_vec()),
        )
    }

    fn hook(
        &self,
        what: &str,
        get: impl Fn(&Hooks) -> Option<Arc<dyn Fn() + Send + Sync>>,
    ) -> (u16, &'static str, Vec<u8>) {
        let fn_ = get(&self.hooks.lock().unwrap());
        match fn_ {
            Some(fn_) => {
                fn_();
                (200, "text/plain", Vec::new())
            }
            None => (
                503,
                "text/plain",
                format!("{what} not available").into_bytes(),
            ),
        }
    }

    fn widget(&self, group: &str) -> (u16, &'static str, Vec<u8>) {
        let fn_ = self.hooks.lock().unwrap().widget_toggle.clone();
        let Some(fn_) = fn_ else {
            return (
                503,
                "text/plain",
                b"widget toggling is not wired up".to_vec(),
            );
        };
        match fn_(group) {
            Ok(_) => (204, "text/plain", Vec::new()),
            Err(err) => (
                400,
                "text/plain",
                format!("{err}; known groups: {}", groups::TOGGLEABLE.join(", ")).into_bytes(),
            ),
        }
    }
}

/// Last write wins unless the game process is up and the incoming `userID`
/// is someone else. `source=launch` still wins for [`LAUNCH_GRACE_SECS`] after
/// the process appears, because Launch Standalone's XHR can finish after the
/// 1s scan.
fn apply_session(
    current_user_id: Option<&str>,
    incoming_user_id: &str,
    source: &str,
    game_running: bool,
    game_started_at: i64,
    now: i64,
) -> bool {
    let Some(current) = current_user_id else {
        return true;
    };
    if current == incoming_user_id {
        return true;
    }
    if !game_running {
        return true;
    }
    source.eq_ignore_ascii_case("launch")
        && game_started_at > 0
        && now.saturating_sub(game_started_at) < LAUNCH_GRACE_SECS
}

/// The anti-CSRF / anti-rebinding gate. Returns a refusal reason, or None to
/// let the request through.
fn deny_cross_origin(host: &str, origin: &str) -> Option<&'static str> {
    // Non-loopback Host = DNS rebinding, which would make replies readable.
    if !host.is_empty() && !loopback_host(host) {
        return Some("host is not loopback");
    }
    if origin.is_empty() {
        return None;
    }
    if let Some((scheme, rest)) = origin.split_once("://") {
        let ok = match scheme {
            "http" | "https" => loopback_host(rest),
            // chrome-/moz-/safari-web-extension: script managers report
            // themselves, and webpages cannot forge these schemes.
            other => other.ends_with("extension"),
        };
        if ok {
            return None;
        }
    }
    // Web origins and "null" (sandboxed iframes).
    Some("cross-origin requests are not allowed")
}

/// `localhost` or a loopback IP, with or without a port or brackets.
fn loopback_host(hostport: &str) -> bool {
    let host = if let Some(rest) = hostport.strip_prefix('[') {
        rest.split_once(']').map_or(rest, |(h, _)| h)
    } else {
        hostport.rsplit_once(':').map_or(hostport, |(h, _)| h)
    };
    host.eq_ignore_ascii_case("localhost")
        || host
            .parse::<std::net::IpAddr>()
            .is_ok_and(|ip| ip.is_loopback())
}

fn write_http(
    stream: &mut TcpStream,
    status: u16,
    ctype: &str,
    body: &[u8],
) -> std::io::Result<()> {
    let reason = match status {
        200 => "OK",
        204 => "No Content",
        400 => "Bad Request",
        403 => "Forbidden",
        404 => "Not Found",
        413 => "Payload Too Large",
        415 => "Unsupported Media Type",
        500 => "Internal Server Error",
        503 => "Service Unavailable",
        _ => "OK",
    };
    write!(
        stream,
        "HTTP/1.1 {status} {reason}\r\nContent-Type: {ctype}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    )?;
    stream.write_all(body)?;
    Ok(())
}

pub fn coerce(v: &Value) -> String {
    match v {
        Value::Null => String::new(),
        Value::String(s) => s.clone(),
        Value::Bool(true) => "1".into(),
        Value::Bool(false) => "0".into(),
        Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                i.to_string()
            } else if let Some(u) = n.as_u64() {
                u.to_string()
            } else if let Some(f) = n.as_f64() {
                if f.fract() == 0.0 && f.abs() < 1e15 {
                    format!("{}", f as i64)
                } else {
                    let s = format!("{f}");
                    if s.contains('e') || s.contains('E') {
                        format!("{f:.12}")
                            .trim_end_matches('0')
                            .trim_end_matches('.')
                            .to_string()
                    } else {
                        s
                    }
                }
            } else {
                n.to_string()
            }
        }
        other => other.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const FAKE_USER: &str = "1234567";
    const ALT_USER: &str = "7654321";
    const FAKE_PASSWORD: &str = "3a7bd3e2360a3d29eea436fcfb7e44c735d117c4";
    const FAKE_SC: &str = "0f9a1c4e8b2d6f3a7c5e9b1d4f8a2c6e";
    const FAKE_SALT: &str = "y27bigaOAA1";

    fn payload(vars: Value, salt: &str, cookies: &str) -> Vec<u8> {
        payload_source(vars, salt, cookies, None)
    }

    fn payload_source(vars: Value, salt: &str, cookies: &str, source: Option<&str>) -> Vec<u8> {
        let mut obj = json!({
            "userVars": vars,
            "skeygen": salt,
            "cookies": cookies,
        });
        if let Some(source) = source {
            obj["source"] = json!(source);
        }
        serde_json::to_vec(&obj).unwrap()
    }

    fn valid_vars() -> Value {
        json!({
            "userID": FAKE_USER,
            "password": FAKE_PASSWORD,
            "sc": FAKE_SC,
            "df_level": "415",
        })
    }

    /// ureq 3 turns a non-2xx into Err and drops the body, so these tests ask
    /// for the response either way.
    fn lenient_agent() -> ureq::Agent {
        ureq::Agent::new_with_config(
            ureq::Agent::config_builder()
                .http_status_as_error(false)
                .build(),
        )
    }

    fn post(url: &str, ctype: &str, body: &[u8]) -> (u16, String) {
        post_from(url, ctype, "", body)
    }

    fn post_from(url: &str, ctype: &str, origin: &str, body: &[u8]) -> (u16, String) {
        let mut req = lenient_agent().post(url);
        if !ctype.is_empty() {
            req = req.header("Content-Type", ctype);
        }
        if !origin.is_empty() {
            req = req.header("Origin", origin);
        }
        let mut resp = req.send(body).expect("post");
        let status = resp.status().as_u16();
        (status, resp.body_mut().read_to_string().unwrap_or_default())
    }

    fn test_srv(hooks: Hooks) -> (Server, Arc<Creds>, std::path::PathBuf) {
        let (srv, creds, dir, _, _) = test_srv_lock(hooks, false, 0);
        (srv, creds, dir)
    }

    fn test_srv_lock(
        hooks: Hooks,
        running: bool,
        started_at: i64,
    ) -> (
        Server,
        Arc<Creds>,
        std::path::PathBuf,
        Arc<AtomicBool>,
        Arc<AtomicI64>,
    ) {
        let dir = std::env::temp_dir().join(format!(
            "df-hud-bridge-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("credentials.json");
        let creds = Arc::new(Creds::new(&path));
        let game_running = Arc::new(AtomicBool::new(running));
        let game_started_at = Arc::new(AtomicI64::new(started_at));
        let srv = start(
            "127.0.0.1:0",
            creds.clone(),
            hooks,
            game_running.clone(),
            game_started_at.clone(),
        )
        .unwrap();
        (srv, creds, dir, game_running, game_started_at)
    }

    fn alt_vars() -> Value {
        let mut vars = valid_vars();
        vars["userID"] = json!(ALT_USER);
        vars
    }

    #[test]
    fn accepts_payload() {
        let (srv, creds, _dir) = test_srv(Hooks::default());
        let url = format!("http://{}/api/userData", srv.listen);
        let (status, _) = post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        let (cr, salt) = creds.get().expect("stored");
        assert_eq!(cr.user_id, FAKE_USER);
        assert_eq!(cr.password, FAKE_PASSWORD);
        assert_eq!(cr.sc, FAKE_SC);
        assert_eq!(salt, FAKE_SALT);
        srv.stop();
    }

    #[test]
    fn stores_cookie_privately() {
        let (srv, creds, _dir) = test_srv(Hooks::default());
        let url = format!("http://{}/api/userData", srv.listen);
        let cookie = "DeadFrontierFairview=session-value; lastLoginUser=someone";
        post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, cookie),
        );
        let (cr, _) = creds.get().unwrap();
        assert_eq!(cr.cookie, cookie);
        srv.stop();
    }

    #[test]
    fn rejects_incomplete() {
        let (srv, creds, _dir) = test_srv(Hooks::default());
        let url = format!("http://{}/api/userData", srv.listen);
        let (status, body) = post(
            &url,
            "application/json",
            &payload(json!({"password": FAKE_PASSWORD, "sc": FAKE_SC}), "", ""),
        );
        assert_eq!(status, 400);
        assert!(body.contains("userID"), "{body}");
        assert!(creds.get().is_none());
        srv.stop();
    }

    #[test]
    fn rejects_bad_input() {
        let (srv, _, _dir) = test_srv(Hooks::default());
        let base = format!("http://{}", srv.listen);
        let (status, _) = post(
            &format!("{base}/api/userData"),
            "application/json",
            b"{not json",
        );
        assert_eq!(status, 400);
        let (status, _) = post(
            &format!("{base}/api/userData"),
            "text/plain",
            &payload(valid_vars(), "", ""),
        );
        assert_eq!(status, 415);
        // An untyped Blob POST arrives with no Content-Type and no preflight.
        let (status, _) = post(
            &format!("{base}/api/userData"),
            "",
            &payload(valid_vars(), "", ""),
        );
        assert_eq!(status, 415);
        let status = match lenient_agent().get(&format!("{base}/api/userData")).call() {
            Ok(r) => r.status().as_u16(),
            Err(_) => 0,
        };
        assert_ne!(status, 200);
        srv.stop();
    }

    #[test]
    fn web_origins_are_rejected() {
        let toggles = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let t2 = toggles.clone();
        let (srv, creds, _dir) = test_srv(Hooks {
            overlay_toggle: Some(Arc::new(move || {
                t2.fetch_add(1, Ordering::SeqCst);
            })),
            ..Hooks::default()
        });
        let base = format!("http://{}", srv.listen);
        let body = payload(valid_vars(), FAKE_SALT, "");
        for origin in ["https://evil.example", "http://evil.example:9310", "null"] {
            let (status, _) = post_from(
                &format!("{base}/api/userData"),
                "application/json",
                origin,
                &body,
            );
            assert_eq!(status, 403, "userData from {origin}");
            let (status, _) = post_from(&format!("{base}/api/overlay/toggle"), "", origin, b"");
            assert_eq!(status, 403, "toggle from {origin}");
        }
        assert!(creds.get().is_none());
        assert_eq!(toggles.load(Ordering::SeqCst), 0);
        srv.stop();
    }

    #[test]
    fn local_and_extension_origins_are_allowed() {
        let (srv, creds, _dir) = test_srv(Hooks::default());
        let url = format!("http://{}/api/userData", srv.listen);
        let body = payload(valid_vars(), FAKE_SALT, "");
        for origin in [
            "moz-extension://0f9a1c4e-8b2d-6f3a-7c5e-9b1d4f8a2c6e",
            "chrome-extension://abcdefghijklmnop",
            &format!("http://{}", srv.listen),
            "http://localhost:9310",
        ] {
            let (status, _) = post_from(&url, "application/json", origin, &body);
            assert_eq!(status, 200, "from {origin}");
        }
        assert!(creds.get().is_some());
        srv.stop();
    }

    /// ureq will not send a forged Host, so this one speaks raw HTTP.
    #[test]
    fn rebound_host_is_rejected() {
        let (srv, creds, _dir) = test_srv(Hooks::default());
        let body = payload(valid_vars(), FAKE_SALT, "");
        let mut stream = TcpStream::connect(srv.listen).unwrap();
        write!(
            stream,
            "POST /api/userData HTTP/1.1\r\nHost: evil.example:9310\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            body.len()
        )
        .unwrap();
        stream.write_all(&body).unwrap();
        let mut status_line = String::new();
        BufReader::new(stream).read_line(&mut status_line).unwrap();
        assert!(status_line.contains("403"), "{status_line}");
        assert!(creds.get().is_none());
        srv.stop();
    }

    #[test]
    fn loopback_host_shapes() {
        for host in [
            "127.0.0.1",
            "127.0.0.1:9310",
            "127.1.2.3:80",
            "localhost",
            "LocalHost:9310",
            "[::1]",
            "[::1]:9310",
        ] {
            assert!(loopback_host(host), "{host}");
        }
        for host in [
            "evil.example",
            "evil.example:9310",
            "192.168.1.2:9310",
            "[2001:db8::1]:9310",
            "localhost.evil.example",
        ] {
            assert!(!loopback_host(host), "{host}");
        }
    }

    #[test]
    fn health_reports_no_secrets() {
        let (srv, _, _dir) = test_srv(Hooks::default());
        let base = format!("http://{}", srv.listen);
        post(
            &format!("{base}/api/userData"),
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        let resp = ureq::get(&format!("{base}/healthz")).call().unwrap();
        let text = resp.into_body().read_to_string().unwrap();
        let got: Value = serde_json::from_str(&text).unwrap();
        assert_eq!(got["have_credentials"], true);
        assert_eq!(got["have_signing_salt"], true);
        assert_eq!(got["session_locked"], false);
        for secret in [FAKE_PASSWORD, FAKE_SC, FAKE_SALT, FAKE_USER] {
            assert!(!text.contains(secret), "healthz leaked {secret}: {text}");
        }
        srv.stop();
    }

    #[test]
    fn on_credentials_fires_only_on_change() {
        let calls = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let c2 = calls.clone();
        let (srv, _, _dir) = test_srv(Hooks {
            on_credentials: Some(Arc::new(move || {
                c2.fetch_add(1, Ordering::SeqCst);
            })),
            ..Hooks::default()
        });
        let url = format!("http://{}/api/userData", srv.listen);
        let body = payload(valid_vars(), FAKE_SALT, "");
        post(&url, "application/json", &body);
        post(&url, "application/json", &body);
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        let mut vars = valid_vars();
        vars["sc"] = json!("ffffffffffffffffffffffffffffffff");
        post(&url, "application/json", &payload(vars, FAKE_SALT, ""));
        assert_eq!(calls.load(Ordering::SeqCst), 2);
        srv.stop();
    }

    #[test]
    fn coerce_numbers() {
        assert_eq!(coerce(&json!("1054")), "1054");
        assert_eq!(coerce(&json!(1054)), "1054");
        assert_eq!(coerce(&json!(415)), "415");
        assert_eq!(coerce(&Value::Null), "");
        assert_eq!(coerce(&json!(true)), "1");
        assert_eq!(coerce(&json!(false)), "0");
        assert_eq!(coerce(&json!(1.5)), "1.5");
    }

    #[test]
    fn correction_endpoints() {
        let (srv, _, _dir) = test_srv(Hooks::default());
        let base = format!("http://{}", srv.listen);
        for path in ["/api/run/start", "/api/xp/reset"] {
            let (status, _) = post(&format!("{base}{path}"), "", b"");
            assert_eq!(status, 503, "{path}");
        }
        let runs = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let resets = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let r1 = runs.clone();
        let r2 = resets.clone();
        srv.stop();
        let (srv, _, _dir) = test_srv(Hooks {
            run_start: Some(Arc::new(move || {
                r1.fetch_add(1, Ordering::SeqCst);
            })),
            xp_reset: Some(Arc::new(move || {
                r2.fetch_add(1, Ordering::SeqCst);
            })),
            ..Hooks::default()
        });
        let base = format!("http://{}", srv.listen);
        assert_eq!(post(&format!("{base}/api/run/start"), "", b"").0, 200);
        assert_eq!(post(&format!("{base}/api/xp/reset"), "", b"").0, 200);
        assert_eq!(runs.load(Ordering::SeqCst), 1);
        assert_eq!(resets.load(Ordering::SeqCst), 1);
        let status = match lenient_agent().get(&format!("{base}/api/run/start")).call() {
            Ok(r) => r.status().as_u16(),
            Err(_) => 0,
        };
        assert_ne!(status, 200);
        assert_eq!(runs.load(Ordering::SeqCst), 1);
        srv.stop();
    }

    #[test]
    fn overlay_and_widget_toggle() {
        let toggles = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let t2 = toggles.clone();
        let groups_hit = Arc::new(Mutex::new(Vec::new()));
        let g2 = groups_hit.clone();
        let (srv, _, _dir) = test_srv(Hooks {
            overlay_toggle: Some(Arc::new(move || {
                t2.fetch_add(1, Ordering::SeqCst);
            })),
            widget_toggle: Some(Arc::new(move |g| {
                if !groups::known(g) {
                    return Err(format!("unknown group {g:?}"));
                }
                g2.lock().unwrap().push(g.to_string());
                Ok(true)
            })),
            ..Hooks::default()
        });
        let base = format!("http://{}", srv.listen);
        assert_eq!(post(&format!("{base}/api/overlay/toggle"), "", b"").0, 200);
        assert_eq!(toggles.load(Ordering::SeqCst), 1);
        let (status, _) = post(&format!("{base}/api/widget/challenges/toggle"), "", b"");
        assert_eq!(status, 204);
        assert_eq!(*groups_hit.lock().unwrap(), ["challenges"]);
        let (status, body) = post(&format!("{base}/api/widget/challenge/toggle"), "", b"");
        assert_eq!(status, 400);
        assert!(body.contains("challenges"), "{body}");
        srv.stop();
    }

    #[test]
    fn game_running_keeps_the_current_account() {
        let calls = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let c2 = calls.clone();
        let now = chrono::Utc::now().timestamp();
        let (srv, creds, _dir, _, _) = test_srv_lock(
            Hooks {
                on_credentials: Some(Arc::new(move || {
                    c2.fetch_add(1, Ordering::SeqCst);
                })),
                ..Hooks::default()
            },
            true,
            now,
        );
        let url = format!("http://{}/api/userData", srv.listen);
        let (status, body) = post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert!(!body.contains("applied"), "{body}");
        let (status, body) = post(
            &url,
            "application/json",
            &payload(alt_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert!(body.contains("\"applied\":false"), "{body}");
        let (cr, _) = creds.get().expect("stored");
        assert_eq!(cr.user_id, FAKE_USER);
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        srv.stop();
    }

    #[test]
    fn game_running_still_refreshes_the_same_account() {
        let now = chrono::Utc::now().timestamp();
        let (srv, creds, _dir, _, _) = test_srv_lock(Hooks::default(), true, now);
        let url = format!("http://{}/api/userData", srv.listen);
        post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        let mut vars = valid_vars();
        vars["sc"] = json!("ffffffffffffffffffffffffffffffff");
        let (status, body) = post(&url, "application/json", &payload(vars, FAKE_SALT, ""));
        assert_eq!(status, 200);
        assert!(!body.contains("applied"), "{body}");
        let (cr, _) = creds.get().expect("stored");
        assert_eq!(cr.user_id, FAKE_USER);
        assert_eq!(cr.sc, "ffffffffffffffffffffffffffffffff");
        srv.stop();
    }

    #[test]
    fn game_not_running_still_takes_the_last_account() {
        let (srv, creds, _dir) = test_srv(Hooks::default());
        let url = format!("http://{}/api/userData", srv.listen);
        post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        let (status, _) = post(
            &url,
            "application/json",
            &payload(alt_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        let (cr, _) = creds.get().expect("stored");
        assert_eq!(cr.user_id, ALT_USER);
        srv.stop();
    }

    #[test]
    fn empty_store_accepts_the_first_account_while_the_game_runs() {
        let now = chrono::Utc::now().timestamp();
        let (srv, creds, _dir, _, _) = test_srv_lock(Hooks::default(), true, now);
        let url = format!("http://{}/api/userData", srv.listen);
        let (status, body) = post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert!(!body.contains("applied"), "{body}");
        assert_eq!(creds.get().expect("stored").0.user_id, FAKE_USER);
        srv.stop();
    }

    #[test]
    fn launch_source_wins_only_inside_the_grace_window() {
        let (srv, creds, _dir, running, started_at) = test_srv_lock(Hooks::default(), false, 0);
        let url = format!("http://{}/api/userData", srv.listen);
        post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        running.store(true, Ordering::SeqCst);
        started_at.store(chrono::Utc::now().timestamp(), Ordering::SeqCst);
        let (status, body) = post(
            &url,
            "application/json",
            &payload_source(alt_vars(), FAKE_SALT, "", Some("launch")),
        );
        assert_eq!(status, 200);
        assert!(!body.contains("applied"), "{body}");
        assert_eq!(creds.get().expect("stored").0.user_id, ALT_USER);

        started_at.store(
            chrono::Utc::now().timestamp() - LAUNCH_GRACE_SECS - 1,
            Ordering::SeqCst,
        );
        let mut later = valid_vars();
        later["userID"] = json!("9999999");
        let (status, body) = post(
            &url,
            "application/json",
            &payload_source(later, FAKE_SALT, "", Some("launch")),
        );
        assert_eq!(status, 200);
        assert!(body.contains("\"applied\":false"), "{body}");
        assert_eq!(creds.get().expect("stored").0.user_id, ALT_USER);
        srv.stop();
    }

    #[test]
    fn health_session_locked_while_the_game_runs() {
        let now = chrono::Utc::now().timestamp();
        let (srv, _, _dir, _, _) = test_srv_lock(Hooks::default(), true, now);
        let base = format!("http://{}", srv.listen);
        post(
            &format!("{base}/api/userData"),
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        let resp = ureq::get(&format!("{base}/healthz")).call().unwrap();
        let got: Value = serde_json::from_str(&resp.into_body().read_to_string().unwrap()).unwrap();
        assert_eq!(got["session_locked"], true);
        srv.stop();
    }

    #[test]
    fn apply_session_rules() {
        assert!(apply_session(None, FAKE_USER, "", true, 1, 1));
        assert!(apply_session(Some(FAKE_USER), FAKE_USER, "", true, 1, 100));
        assert!(apply_session(Some(FAKE_USER), ALT_USER, "", false, 0, 100));
        assert!(!apply_session(Some(FAKE_USER), ALT_USER, "", true, 1, 100));
        assert!(apply_session(
            Some(FAKE_USER),
            ALT_USER,
            "launch",
            true,
            90,
            100
        ));
        assert!(!apply_session(
            Some(FAKE_USER),
            ALT_USER,
            "launch",
            true,
            80,
            100
        ));
    }

    #[test]
    fn validate_loopback_addrs() {
        for addr in ["127.0.0.1:9310", "localhost:9310", "[::1]:9310"] {
            config::validate_loopback(addr).unwrap();
        }
        for addr in [
            ":9310",
            "0.0.0.0:9310",
            "192.168.1.2:9310",
            "example.com:9310",
            "9310",
        ] {
            assert!(config::validate_loopback(addr).is_err(), "{addr}");
        }
    }
}
