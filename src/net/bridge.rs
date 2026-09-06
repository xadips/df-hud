//! Loopback HTTP/1.1 bridge. Request bodies are never logged.
//!
//! Loopback does not stop browsers: any webpage can POST here with a
//! preflight-free request shape, and DNS rebinding makes responses readable.
//! Both arrive with a web Origin or a non-loopback Host; no legitimate
//! client sends either (GM.xmlHttpRequest sends no Origin or an
//! extension-scheme one).

use crate::wake::lock;
use serde_json::{Value, json};
use std::io::{BufRead, BufReader, Read, Write};
#[cfg(test)]
use std::net::SocketAddr;
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicBool, AtomicI64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use crate::app::groups::Group;
use crate::config;
use crate::net::creds::{Credentials, Store as Creds};

const MAX_BODY: usize = 1 << 20;
/// Connections served at once; one thread each. The userscript sends one
/// request at a time, so anything past this is a stuck or hostile peer.
const MAX_CONNECTIONS: usize = 8;
/// After Launch Standalone, block other accounts' periodic syncs until the
/// process watcher takes over (or this elapses if the client never appears).
const PENDING_SECS: i64 = 15;
pub const VERSION: &str = env!("CARGO_PKG_VERSION");
type Hook = Arc<dyn Fn() + Send + Sync>;
type WidgetToggleHook = Arc<dyn Fn(Group) + Send + Sync>;

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
    pending_launch_at: Arc<AtomicI64>,
) -> Result<Server, String> {
    config::validate_loopback(addr)?;
    let listener =
        TcpListener::bind(addr).map_err(|e| format!("bridge cannot listen on {addr}: {e}"))?;
    listener.set_nonblocking(false).map_err(|e| e.to_string())?;
    let listen = listener.local_addr().map_err(|e| e.to_string())?;
    info!("bridge: listening on {listen} (waiting for a browser payload)");
    let stop = Arc::new(AtomicBool::new(false));
    let inner = Arc::new(Inner {
        creds,
        hooks: Mutex::new(hooks),
        started: Instant::now(),
        game_running,
        pending_launch_at,
        ignored_other_account: AtomicBool::new(false),
        in_flight: AtomicUsize::new(0),
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
                    Ok(s) => inner.dispatch(s),
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
    pending_launch_at: Arc<AtomicI64>,
    ignored_other_account: AtomicBool,
    in_flight: AtomicUsize,
}

/// One connection's place under [`MAX_CONNECTIONS`]. Released on drop, so a
/// handler that panics still gives it back.
struct Slot(Arc<Inner>);

impl Drop for Slot {
    fn drop(&mut self) {
        self.0.in_flight.fetch_sub(1, Ordering::SeqCst);
    }
}

impl Inner {
    /// Hand the connection to its own thread, or answer 503 from the accept
    /// thread when [`MAX_CONNECTIONS`] are already being served.
    fn dispatch(self: &Arc<Self>, mut stream: TcpStream) {
        if self.in_flight.fetch_add(1, Ordering::SeqCst) >= MAX_CONNECTIONS {
            self.in_flight.fetch_sub(1, Ordering::SeqCst);
            drain_request(&mut stream);
            let _ = stream.set_write_timeout(Some(Duration::from_secs(2)));
            let _ = write_http(&mut stream, 503, "text/plain", b"too many connections");
            let _ = stream.shutdown(std::net::Shutdown::Write);
            return;
        }
        let slot = Slot(self.clone());
        // A failed spawn drops the closure, and the slot with it.
        if let Err(err) = thread::Builder::new()
            .name("df-hud-bridge-conn".into())
            .spawn(move || slot.0.serve(stream))
        {
            warn!("bridge: could not start a connection thread ({err})");
        }
    }

    fn serve(&self, stream: TcpStream) {
        let _ = stream.set_read_timeout(Some(Duration::from_secs(10)));
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
            warn!("bridge: rejected a payload: malformed JSON");
            return (400, "text/plain", b"malformed JSON".to_vec());
        };
        let vars = parsed.get("userVars").and_then(Value::as_object);
        let Some(vars) = vars else {
            warn!("bridge: payload missing userVars (are you on a logged-in page?)");
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
            warn!(
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
                info!("bridge: keeping the current account");
            }
            return applied_reply(false);
        }
        self.ignored_other_account.store(false, Ordering::SeqCst);
        if source.eq_ignore_ascii_case("launch") {
            self.pending_launch_at
                .store(chrono::Utc::now().timestamp(), Ordering::SeqCst);
        }
        if let Ok(changed) = self.creds.set(cr, &salt) {
            if changed {
                let extra = if salt.is_empty() {
                    ""
                } else {
                    ", signing salt reported"
                };
                info!("bridge: credentials updated from browser{extra}");
                if let Some(fn_) = lock(&self.hooks).on_credentials.clone() {
                    fn_();
                }
            }
            applied_reply(true)
        } else {
            error!("bridge: could not store credentials");
            (500, "text/plain", b"could not store credentials".to_vec())
        }
    }

    fn should_apply(&self, user_id: &str, source: &str) -> bool {
        let current = self.creds.get();
        let now = chrono::Utc::now().timestamp();
        apply_session(SessionApply {
            current: current.as_ref().map(|(c, _)| c.user_id.as_str()),
            incoming: user_id,
            source,
            game_running: self.game_running.load(Ordering::SeqCst),
            pending_at: self.pending_launch_at.load(Ordering::SeqCst),
            now,
        })
    }

    fn pending_active(&self, now: i64) -> bool {
        pending_active(self.pending_launch_at.load(Ordering::SeqCst), now)
    }

    fn health(&self) -> (u16, &'static str, Vec<u8>) {
        let have = self.creds.get().is_some();
        let have_salt = !self.creds.salt().is_empty();
        let game_running = self.game_running.load(Ordering::SeqCst);
        let now = chrono::Utc::now().timestamp();
        let mut obj = json!({
            "ok": true,
            "have_credentials": have,
            "have_signing_salt": have_salt,
            "session_locked": (game_running || self.pending_active(now)) && have,
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
        let fn_ = get(&lock(&self.hooks));
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
        let group = match group.parse::<Group>() {
            Ok(g) => g,
            Err(err) => {
                return (
                    400,
                    "text/plain",
                    format!("{err}; known groups: {}", Group::names()).into_bytes(),
                );
            }
        };
        let fn_ = lock(&self.hooks).widget_toggle.clone();
        let Some(fn_) = fn_ else {
            return (
                503,
                "text/plain",
                b"widget toggling is not wired up".to_vec(),
            );
        };
        fn_(group);
        (204, "text/plain", Vec::new())
    }
}

fn applied_reply(applied: bool) -> (u16, &'static str, Vec<u8>) {
    (
        200,
        "application/json",
        if applied {
            b"{\"ok\":true,\"applied\":true}".to_vec()
        } else {
            b"{\"ok\":true,\"applied\":false}".to_vec()
        },
    )
}

struct SessionApply<'a> {
    current: Option<&'a str>,
    incoming: &'a str,
    source: &'a str,
    game_running: bool,
    pending_at: i64,
    now: i64,
}

fn pending_active(at: i64, now: i64) -> bool {
    at > 0 && now.saturating_sub(at) < PENDING_SECS
}

/// Locked while the game is running, or for [`PENDING_SECS`] after a launch
/// POST until the watcher takes over. Same account always refreshes.
/// `source=launch` switches only while the client is down.
fn apply_session(a: SessionApply<'_>) -> bool {
    let Some(current) = a.current else {
        return true;
    };
    if current == a.incoming {
        return true;
    }
    if a.game_running {
        return false;
    }
    if a.source.eq_ignore_ascii_case("launch") {
        return true;
    }
    !pending_active(a.pending_at, a.now)
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

/// Most a refused request is read before the 503. A userscript sync is the
/// whole `userVars` object plus cookies, tens of KiB on a big account, so
/// this is generous; the time bounds below stop a slow peer, not the size.
const DRAIN_LIMIT: usize = 64 << 10;

/// Reads a request before it is refused. Closing with unread bytes in the
/// socket turns into a TCP reset, and a client mid-POST then sees the reset
/// instead of the status. Bounded in bytes and time: this runs on the accept
/// thread. Stops at the end of the body once `Content-Length` is known, at
/// [`DRAIN_LIMIT`], or when the peer goes quiet.
fn drain_request(stream: &mut TcpStream) {
    let deadline = Instant::now() + Duration::from_secs(1);
    let _ = stream.set_read_timeout(Some(Duration::from_millis(250)));
    let mut buf = Vec::new();
    let mut chunk = [0u8; 4096];
    let mut want: Option<usize> = None;
    while buf.len() < DRAIN_LIMIT && want.is_none_or(|n| buf.len() < n) && Instant::now() < deadline
    {
        match stream.read(&mut chunk) {
            Ok(0) | Err(_) => break,
            Ok(n) => buf.extend_from_slice(&chunk[..n]),
        }
        if want.is_none()
            && let Some(end) = buf.windows(4).position(|w| w == b"\r\n\r\n")
        {
            want = Some(drain_want(end, content_length(&buf[..end])));
        }
    }
}

/// Bytes to read in total for a request whose headers end at `header_end`
/// and declare `content_length`. A hostile length near `usize::MAX` must not
/// overflow (a panic here would take the accept thread with it), and nothing
/// past [`DRAIN_LIMIT`] is read anyway.
fn drain_want(header_end: usize, content_length: usize) -> usize {
    header_end
        .saturating_add(4)
        .saturating_add(content_length)
        .min(DRAIN_LIMIT)
}

/// `Content-Length` out of a raw header block; 0 when absent or malformed.
fn content_length(headers: &[u8]) -> usize {
    String::from_utf8_lossy(headers)
        .lines()
        .filter_map(|line| line.split_once(':'))
        .find(|(k, _)| k.trim().eq_ignore_ascii_case("content-length"))
        .and_then(|(_, v)| v.trim().parse().ok())
        .unwrap_or(0)
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
        let (srv, creds, dir, _, _) = test_srv_lock(hooks, false);
        (srv, creds, dir)
    }

    fn test_srv_lock(
        hooks: Hooks,
        running: bool,
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
        let pending_launch_at = Arc::new(AtomicI64::new(0));
        let srv = start(
            "127.0.0.1:0",
            creds.clone(),
            hooks,
            game_running.clone(),
            pending_launch_at.clone(),
        )
        .unwrap();
        (srv, creds, dir, game_running, pending_launch_at)
    }

    fn assert_applied(body: &str, want: bool) {
        let got: Value = serde_json::from_str(body).expect(body);
        assert_eq!(got["ok"], true, "{body}");
        assert_eq!(got["applied"], want, "{body}");
    }

    fn apply(
        current: Option<&str>,
        incoming: &str,
        source: &str,
        running: bool,
        pending_at: i64,
        now: i64,
    ) -> bool {
        apply_session(SessionApply {
            current,
            incoming,
            source,
            game_running: running,
            pending_at,
            now,
        })
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

    /// Idle connections each hold a handler thread. Past the cap the accept
    /// thread answers 503 itself, even to a POST with a body it never
    /// served, and the slots come back once they close.
    #[test]
    fn saturated_bridge_answers_503_then_recovers() {
        let (srv, _, _dir) = test_srv(Hooks::default());
        let idle: Vec<TcpStream> = (0..MAX_CONNECTIONS)
            .map(|_| TcpStream::connect(srv.listen).unwrap())
            .collect();
        let read_503 = |stream: TcpStream| {
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut reader = BufReader::new(stream);
            let mut status_line = String::new();
            reader.read_line(&mut status_line).unwrap();
            assert!(status_line.starts_with("HTTP/1.1 503"), "{status_line}");
            let mut headers = String::new();
            let mut body_len = 0usize;
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).unwrap() == 0 || line == "\r\n" {
                    break;
                }
                if let Some(n) = line.to_ascii_lowercase().strip_prefix("content-length:") {
                    body_len = n.trim().parse().unwrap();
                }
                headers.push_str(&line.to_ascii_lowercase());
            }
            assert!(headers.contains("connection: close"), "{headers}");
            let mut body = vec![0u8; body_len];
            reader.read_exact(&mut body).unwrap();
            assert_eq!(body, b"too many connections");
        };
        read_503(TcpStream::connect(srv.listen).unwrap());

        // A real sync: the body must be read before the refusal, or the
        // close turns into a reset and the client never sees the 503. A big
        // account's userVars plus cookies run to tens of KiB.
        let cookie = "x".repeat(20 << 10);
        let payload = payload(valid_vars(), FAKE_SALT, &cookie);
        assert!(payload.len() > 20 << 10 && payload.len() < DRAIN_LIMIT);
        let mut posting = TcpStream::connect(srv.listen).unwrap();
        write!(
            posting,
            "POST /api/userData HTTP/1.1\r\nHost: {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            srv.listen,
            payload.len()
        )
        .unwrap();
        posting.write_all(&payload).unwrap();
        read_503(posting);

        // A hostile length must not overflow the drain arithmetic (a panic
        // would kill the accept thread); the 503 still arrives once the
        // peer goes quiet.
        let mut hostile = TcpStream::connect(srv.listen).unwrap();
        write!(
            hostile,
            "POST /api/userData HTTP/1.1\r\nHost: {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{{}}",
            srv.listen,
            usize::MAX
        )
        .unwrap();
        read_503(hostile);

        drop(idle);
        let health = format!("http://{}/healthz", srv.listen);
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            let status = lenient_agent()
                .get(&health)
                .call()
                .map_or(0, |r| r.status().as_u16());
            if status == 200 {
                break;
            }
            assert!(
                Instant::now() < deadline,
                "still refused after the idle connections closed ({status})"
            );
            thread::sleep(Duration::from_millis(10));
        }
        srv.stop();
    }

    #[test]
    fn drain_want_saturates_and_clamps() {
        assert_eq!(drain_want(100, 0), 104);
        assert_eq!(drain_want(100, 2000), 2104);
        assert_eq!(drain_want(100, DRAIN_LIMIT), DRAIN_LIMIT);
        assert_eq!(drain_want(100, usize::MAX), DRAIN_LIMIT);
        assert_eq!(drain_want(usize::MAX, usize::MAX), DRAIN_LIMIT);
        assert_eq!(
            content_length(b"Content-Length: 18446744073709551615"),
            usize::MAX
        );
        assert_eq!(content_length(b"Content-Length: 18446744073709551616"), 0);
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
                g2.lock().unwrap().push(g);
            })),
            ..Hooks::default()
        });
        let base = format!("http://{}", srv.listen);
        assert_eq!(post(&format!("{base}/api/overlay/toggle"), "", b"").0, 200);
        assert_eq!(toggles.load(Ordering::SeqCst), 1);
        let (status, _) = post(&format!("{base}/api/widget/challenges/toggle"), "", b"");
        assert_eq!(status, 204);
        assert_eq!(*groups_hit.lock().unwrap(), [Group::Challenges]);
        let (status, body) = post(&format!("{base}/api/widget/challenge/toggle"), "", b"");
        assert_eq!(status, 400);
        assert!(body.contains("unknown group \"challenge\""), "{body}");
        assert!(body.contains("challenges"), "{body}");
        assert_eq!(*groups_hit.lock().unwrap(), [Group::Challenges]);
        srv.stop();
    }

    #[test]
    fn game_running_keeps_the_current_account() {
        let calls = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let c2 = calls.clone();
        let (srv, creds, _dir, _, _) = test_srv_lock(
            Hooks {
                on_credentials: Some(Arc::new(move || {
                    c2.fetch_add(1, Ordering::SeqCst);
                })),
                ..Hooks::default()
            },
            true,
        );
        let url = format!("http://{}/api/userData", srv.listen);
        let (status, body) = post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert_applied(&body, true);
        let (status, body) = post(
            &url,
            "application/json",
            &payload(alt_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert_applied(&body, false);
        let (cr, _) = creds.get().expect("stored");
        assert_eq!(cr.user_id, FAKE_USER);
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        srv.stop();
    }

    #[test]
    fn game_running_still_refreshes_the_same_account() {
        let (srv, creds, _dir, _, _) = test_srv_lock(Hooks::default(), true);
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
        assert_applied(&body, true);
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
        let (srv, creds, _dir, _, _) = test_srv_lock(Hooks::default(), true);
        let url = format!("http://{}/api/userData", srv.listen);
        let (status, body) = post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert_applied(&body, true);
        assert_eq!(creds.get().expect("stored").0.user_id, FAKE_USER);
        srv.stop();
    }

    #[test]
    fn launch_does_not_switch_while_the_game_runs() {
        let (srv, creds, _dir, _, _) = test_srv_lock(Hooks::default(), true);
        let url = format!("http://{}/api/userData", srv.listen);
        post(
            &url,
            "application/json",
            &payload(valid_vars(), FAKE_SALT, ""),
        );
        let (status, body) = post(
            &url,
            "application/json",
            &payload_source(alt_vars(), FAKE_SALT, "", Some("launch")),
        );
        assert_eq!(status, 200);
        assert_applied(&body, false);
        assert_eq!(creds.get().expect("stored").0.user_id, FAKE_USER);
        srv.stop();
    }

    #[test]
    fn health_session_locked_while_the_game_runs() {
        let (srv, _, _dir, _, _) = test_srv_lock(Hooks::default(), true);
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
    fn health_session_locked_during_pending_launch() {
        let (srv, _, _dir) = test_srv(Hooks::default());
        let base = format!("http://{}", srv.listen);
        post(
            &format!("{base}/api/userData"),
            "application/json",
            &payload_source(valid_vars(), FAKE_SALT, "", Some("launch")),
        );
        let resp = ureq::get(&format!("{base}/healthz")).call().unwrap();
        let got: Value = serde_json::from_str(&resp.into_body().read_to_string().unwrap()).unwrap();
        assert_eq!(got["session_locked"], true);
        srv.stop();
    }

    #[test]
    fn apply_session_rules() {
        assert!(apply(None, FAKE_USER, "", true, 0, 1));
        assert!(apply(Some(FAKE_USER), FAKE_USER, "", true, 0, 100));
        assert!(apply(Some(FAKE_USER), ALT_USER, "", false, 0, 100));
        assert!(!apply(Some(FAKE_USER), ALT_USER, "", true, 0, 100));
        assert!(
            !apply(Some(FAKE_USER), ALT_USER, "launch", true, 0, 100),
            "launch does not switch while the client is running"
        );
        assert!(
            apply(Some(FAKE_USER), ALT_USER, "launch", false, 95, 100),
            "launch switches while the client is down, even during pending"
        );
        assert!(
            !apply(Some(FAKE_USER), ALT_USER, "", false, 95, 100),
            "periodic sync loses to a recent launch"
        );
        assert!(
            apply(Some(FAKE_USER), ALT_USER, "", false, 20, 100),
            "pending launch expires if the client never appears"
        );
    }

    #[test]
    fn pending_launch_blocks_periodic_sync_before_the_process() {
        let (srv, creds, _dir) = test_srv(Hooks::default());
        let url = format!("http://{}/api/userData", srv.listen);
        let (status, body) = post(
            &url,
            "application/json",
            &payload_source(valid_vars(), FAKE_SALT, "", Some("launch")),
        );
        assert_eq!(status, 200);
        assert_applied(&body, true);
        let (status, body) = post(
            &url,
            "application/json",
            &payload(alt_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert_applied(&body, false);
        assert_eq!(creds.get().expect("stored").0.user_id, FAKE_USER);
        srv.stop();
    }

    #[test]
    fn after_game_close_periodic_can_switch() {
        let (srv, creds, _dir, _, pending) = test_srv_lock(Hooks::default(), false);
        let url = format!("http://{}/api/userData", srv.listen);
        post(
            &url,
            "application/json",
            &payload_source(valid_vars(), FAKE_SALT, "", Some("launch")),
        );
        let (status, body) = post(
            &url,
            "application/json",
            &payload(alt_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert_applied(&body, false);
        pending.store(0, Ordering::SeqCst);
        let (status, body) = post(
            &url,
            "application/json",
            &payload(alt_vars(), FAKE_SALT, ""),
        );
        assert_eq!(status, 200);
        assert_applied(&body, true);
        assert_eq!(creds.get().expect("stored").0.user_id, ALT_USER);
        srv.stop();
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
