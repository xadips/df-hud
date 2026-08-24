//! Loopback HTTP/1.1 bridge. Request bodies are never logged.

use serde_json::{json, Value};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
#[cfg(test)]
use std::net::SocketAddr;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Instant;

use crate::config;
use crate::creds::{Credentials, Store as Creds};
use crate::groups;

const MAX_BODY: usize = 1 << 20;
pub const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Clone, Default)]
pub struct Hooks {
    pub on_credentials: Option<Arc<dyn Fn() + Send + Sync>>,
    pub run_start: Option<Arc<dyn Fn() + Send + Sync>>,
    pub xp_reset: Option<Arc<dyn Fn() + Send + Sync>>,
    pub overlay_toggle: Option<Arc<dyn Fn() + Send + Sync>>,
    pub widget_toggle: Option<Arc<dyn Fn(&str) -> Result<bool, String> + Send + Sync>>,
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

pub fn start(addr: &str, creds: Arc<Creds>, hooks: Hooks) -> Result<Server, String> {
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
        loop {
            let mut line = String::new();
            match reader.read_line(&mut line) {
                Ok(0) => return,
                Ok(_) => {}
                Err(_) => return,
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
        if !ctype.is_empty() && !ctype.starts_with("application/json") {
            return (415, "text/plain", b"expected application/json".to_vec());
        }
        let parsed: Value = match serde_json::from_slice(body) {
            Ok(v) => v,
            Err(_) => {
                eprintln!("bridge: rejected a payload: malformed JSON");
                return (400, "text/plain", b"malformed JSON".to_vec());
            }
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
        match self.creds.set(cr, &salt) {
            Ok(changed) => {
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
            }
            Err(_) => {
                eprintln!("bridge: could not store credentials");
                (500, "text/plain", b"could not store credentials".to_vec())
            }
        }
    }

    fn health(&self) -> (u16, &'static str, Vec<u8>) {
        let have = self.creds.get().is_some();
        let have_salt = !self.creds.salt().is_empty();
        let mut obj = json!({
            "ok": true,
            "have_credentials": have,
            "have_signing_salt": have_salt,
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
    const FAKE_PASSWORD: &str = "3a7bd3e2360a3d29eea436fcfb7e44c735d117c4";
    const FAKE_SC: &str = "0f9a1c4e8b2d6f3a7c5e9b1d4f8a2c6e";
    const FAKE_SALT: &str = "y27bigaOAA1";

    fn payload(vars: Value, salt: &str, cookies: &str) -> Vec<u8> {
        serde_json::to_vec(&json!({
            "userVars": vars,
            "skeygen": salt,
            "cookies": cookies,
        }))
        .unwrap()
    }

    fn valid_vars() -> Value {
        json!({
            "userID": FAKE_USER,
            "password": FAKE_PASSWORD,
            "sc": FAKE_SC,
            "df_level": "415",
        })
    }

    fn post(url: &str, ctype: &str, body: &[u8]) -> (u16, String) {
        let mut req = ureq::post(url);
        if !ctype.is_empty() {
            req = req.set("Content-Type", ctype);
        }
        match req.send_bytes(body) {
            Ok(resp) => {
                let status = resp.status();
                let text = resp.into_string().unwrap_or_default();
                (status, text)
            }
            Err(ureq::Error::Status(status, resp)) => {
                (status, resp.into_string().unwrap_or_default())
            }
            Err(e) => panic!("{e}"),
        }
    }

    fn test_srv(hooks: Hooks) -> (Server, Arc<Creds>, std::path::PathBuf) {
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
        let srv = start("127.0.0.1:0", creds.clone(), hooks).unwrap();
        (srv, creds, dir)
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
        let status = match ureq::get(&format!("{base}/api/userData")).call() {
            Ok(r) => r.status(),
            Err(ureq::Error::Status(s, _)) => s,
            Err(_) => 0,
        };
        assert_ne!(status, 200);
        srv.stop();
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
        let text = resp.into_string().unwrap();
        let got: Value = serde_json::from_str(&text).unwrap();
        assert_eq!(got["have_credentials"], true);
        assert_eq!(got["have_signing_salt"], true);
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
        let status = match ureq::get(&format!("{base}/api/run/start")).call() {
            Ok(r) => r.status(),
            Err(ureq::Error::Status(s, _)) => s,
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
    fn validate_loopback_addrs() {
        for addr in ["127.0.0.1:9275", "localhost:9275", "[::1]:9275"] {
            config::validate_loopback(addr).unwrap();
        }
        for addr in [
            ":9275",
            "0.0.0.0:9275",
            "192.168.1.2:9275",
            "example.com:9275",
            "9275",
        ] {
            assert!(config::validate_loopback(addr).is_err(), "{addr}");
        }
    }
}
