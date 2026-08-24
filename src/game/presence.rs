//! Parse the game client's Discord rich-presence `details` string, and serve
//! the fake Discord IPC endpoint the client publishes to.

use chrono::{DateTime, Utc};
use serde::Deserialize;
use serde_json::{json, Value};
use std::io::{self, Read, Write};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use crate::data::citymap;
use crate::model::PresenceState;

const INNER_CITY: &str = "Inner City";

const OP_HANDSHAKE: u32 = 0;
const OP_FRAME: u32 = 1;
const OP_CLOSE: u32 = 2;
const OP_PING: u32 = 3;
const OP_PONG: u32 = 4;
const MAX_FRAME: u32 = 64 << 10;

pub fn parse_details(details: &str, at: DateTime<Utc>) -> PresenceState {
    let mut s = PresenceState {
        at,
        details: details.to_string(),
        ..PresenceState::default()
    };
    let text = details.trim();
    if text.is_empty() {
        return s;
    }
    if text.eq_ignore_ascii_case("loading...") || text.eq_ignore_ascii_case("loading") {
        s.loading = true;
        return s;
    }
    if let Some((place, x, y)) = parse_block_position(text) {
        s.has_position = true;
        s.x = x;
        s.y = y;
        s.place = place.clone();
        s.indoors = !place.eq_ignore_ascii_case(INNER_CITY);
        return s;
    }
    if citymap::outpost_coords(text).is_some() {
        s.in_outpost = true;
        s.outpost_name = text.to_string();
        return s;
    }
    s
}

fn parse_block_position(text: &str) -> Option<(String, i32, i32)> {
    let f: Vec<&str> = text.split_whitespace().collect();
    if f.len() < 4 || f[f.len() - 2] != "x" {
        return None;
    }
    let x: i32 = f[f.len() - 3].parse().ok()?;
    let y: i32 = f[f.len() - 1].parse().ok()?;
    Some((f[..f.len() - 3].join(" "), x, y))
}

pub fn default_socket() -> String {
    #[cfg(unix)]
    {
        let dir = std::env::var("XDG_RUNTIME_DIR")
            .unwrap_or_else(|_| std::env::temp_dir().display().to_string());
        format!("{dir}/discord-ipc-0")
    }
    #[cfg(windows)]
    {
        r"\\.\pipe\discord-ipc-0".to_string()
    }
    #[cfg(not(any(unix, windows)))]
    {
        "discord-ipc-0".to_string()
    }
}

/// Manual retry after a failed Discord IPC bind. Same contract as Go:
/// automatic looping would only spam the log while real Discord owns the socket.
pub struct Control {
    bind_failed: AtomicBool,
    retry: Mutex<Option<std::sync::mpsc::Sender<()>>>,
}

impl Control {
    pub fn new() -> Arc<Self> {
        Arc::new(Self {
            bind_failed: AtomicBool::new(false),
            retry: Mutex::new(None),
        })
    }

    pub fn bind_failed(&self) -> bool {
        self.bind_failed.load(Ordering::SeqCst)
    }

    pub fn retry(&self) -> bool {
        if !self.bind_failed() {
            return false;
        }
        self.retry
            .lock()
            .unwrap()
            .as_ref()
            .is_some_and(|tx| tx.send(()).is_ok())
    }
}

pub fn serve(
    path: &str,
    on_state: impl Fn(PresenceState) + Send + Sync + 'static,
    on_connection: impl Fn(bool) + Send + Sync + 'static,
    control: Arc<Control>,
    stop: Arc<AtomicBool>,
) {
    let (retry_tx, retry_rx) = std::sync::mpsc::channel();
    *control.retry.lock().unwrap() = Some(retry_tx);
    let on_state: Arc<dyn Fn(PresenceState) + Send + Sync> = Arc::new(on_state);
    let on_connection: Arc<dyn Fn(bool) + Send + Sync> = Arc::new(on_connection);
    loop {
        if stop.load(Ordering::SeqCst) {
            return;
        }
        let server = Arc::new(Server {
            on_state: on_state.clone(),
            on_connection: on_connection.clone(),
            last: Mutex::new(None),
            clients: AtomicUsize::new(0),
        });
        match listen(path) {
            Ok(listener) => {
                control.bind_failed.store(false, Ordering::SeqCst);
                eprintln!("presence: listening on {path}");
                accept_loop(server, listener, stop.clone());
                if stop.load(Ordering::SeqCst) {
                    return;
                }
                control.bind_failed.store(true, Ordering::SeqCst);
                eprintln!(
                    "presence: listener ended; position will come from the poll until retried"
                );
            }
            Err(err) => {
                control.bind_failed.store(true, Ordering::SeqCst);
                eprintln!(
                    "presence: not listening ({err}); position will come from the poll until retried"
                );
            }
        }
        loop {
            if stop.load(Ordering::SeqCst) {
                return;
            }
            match retry_rx.recv_timeout(Duration::from_millis(200)) {
                Ok(()) => {
                    eprintln!("presence: retrying IPC bind");
                    break;
                }
                Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {}
                Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => return,
            }
        }
    }
}

struct Server {
    on_state: Arc<dyn Fn(PresenceState) + Send + Sync>,
    on_connection: Arc<dyn Fn(bool) + Send + Sync>,
    last: Mutex<Option<PresenceState>>,
    clients: AtomicUsize,
}

fn accept_loop(server: Arc<Server>, listener: Listener, stop: Arc<AtomicBool>) {
    let path = listener.path.clone();
    loop {
        if stop.load(Ordering::SeqCst) {
            break;
        }
        match listener.accept(&stop) {
            Ok(stream) => {
                let server = server.clone();
                thread::Builder::new()
                    .name("df-hud-presence-conn".into())
                    .spawn(move || serve_conn(&server, stream))
                    .ok();
            }
            Err(err) if err.kind() == io::ErrorKind::Interrupted => break,
            Err(err)
                if err.kind() == io::ErrorKind::WouldBlock
                    || err.kind() == io::ErrorKind::TimedOut =>
            {
                continue;
            }
            Err(err) => {
                if stop.load(Ordering::SeqCst) {
                    break;
                }
                eprintln!("presence: accept ({err})");
                break;
            }
        }
    }
    drop(listener);
    cleanup(&path);
}

fn serve_conn(server: &Server, mut stream: IpcStream) {
    let first = server.clients.fetch_add(1, Ordering::SeqCst) == 0;
    if first {
        eprintln!("presence: client connected");
        (server.on_connection)(true);
    }
    loop {
        match read_frame(&mut stream) {
            Ok((op, body)) => {
                if let Err(err) = handle(server, &mut stream, op, &body) {
                    if err.kind() != io::ErrorKind::UnexpectedEof {
                        eprintln!("presence: {err}");
                    }
                    break;
                }
            }
            Err(err) => {
                if err.kind() != io::ErrorKind::UnexpectedEof
                    && err.kind() != io::ErrorKind::ConnectionAborted
                {
                    eprintln!("presence: connection ended ({err})");
                }
                break;
            }
        }
    }
    let last = server.clients.fetch_sub(1, Ordering::SeqCst) == 1;
    if last {
        eprintln!("presence: client disconnected");
        (server.on_connection)(false);
    }
}

fn handle(server: &Server, stream: &mut IpcStream, op: u32, body: &[u8]) -> io::Result<()> {
    match op {
        OP_HANDSHAKE => {
            eprintln!("presence: handshake received");
            write_frame(
                stream,
                OP_FRAME,
                &json!({
                    "cmd": "DISPATCH",
                    "evt": "READY",
                    "data": {
                        "v": 1,
                        "config": {
                            "api_endpoint": "//discord.com/api",
                            "environment": "production"
                        },
                        "user": {
                            "id": "0",
                            "username": "df-hud",
                            "discriminator": "0000"
                        }
                    }
                }),
            )
        }
        OP_PING => write_frame_raw(stream, OP_PONG, body),
        OP_CLOSE => Err(io::Error::new(io::ErrorKind::UnexpectedEof, "close")),
        _ => {
            let frame: Frame = match serde_json::from_slice(body) {
                Ok(f) => f,
                Err(err) => {
                    eprintln!("presence: unparsable frame ({err})");
                    return Ok(());
                }
            };
            if frame.cmd == "SET_ACTIVITY" {
                apply_activity(server, frame.args);
            }
            write_frame(
                stream,
                OP_FRAME,
                &json!({
                    "cmd": frame.cmd,
                    "evt": Value::Null,
                    "nonce": frame.nonce,
                    "data": Value::Null
                }),
            )
        }
    }
}

fn apply_activity(server: &Server, args: Option<Value>) {
    let Some(args) = args else {
        return;
    };
    if args.is_null() {
        return;
    }
    let Some(activity) = args.get("activity") else {
        return;
    };
    if activity.is_null() {
        return;
    }
    if !activity.is_object() {
        return;
    }
    let details = activity
        .get("details")
        .and_then(|v| v.as_str())
        .unwrap_or("");
    let state = parse_details(details, Utc::now());

    let mut last = server.last.lock().unwrap();
    let unknown = !state.details.is_empty()
        && !state.has_position
        && !state.in_outpost
        && !state.loading
        && last
            .as_ref()
            .map(|p| p.details != state.details)
            .unwrap_or(true);
    let kind = presence_kind(&state);
    let kind_changed = last
        .as_ref()
        .map(|p| presence_kind(p) != kind)
        .unwrap_or(true);
    *last = Some(state.clone());
    drop(last);

    if kind_changed {
        eprintln!("presence: {kind}");
    }
    if unknown {
        eprintln!(
            "presence: unrecognised details {:?} - position still coming from the poll",
            state.details
        );
    }
    (server.on_state)(state);
}

fn presence_kind(s: &PresenceState) -> String {
    if s.loading {
        return "loading".into();
    }
    if s.in_outpost {
        return format!("outpost {}", s.outpost_name);
    }
    if s.has_position && s.indoors {
        return format!("{} {},{}", s.place, s.x, s.y);
    }
    if s.has_position {
        return format!("inner city {},{}", s.x, s.y);
    }
    if s.details.is_empty() {
        return "nothing".into();
    }
    "unparsed".into()
}

#[derive(Deserialize)]
struct Frame {
    #[serde(default)]
    cmd: String,
    #[serde(default)]
    nonce: String,
    #[serde(default)]
    args: Option<Value>,
}

fn read_frame(r: &mut impl Read) -> io::Result<(u32, Vec<u8>)> {
    let mut head = [0u8; 8];
    r.read_exact(&mut head)?;
    let op = u32::from_le_bytes(head[0..4].try_into().unwrap());
    let len = u32::from_le_bytes(head[4..8].try_into().unwrap());
    if len > MAX_FRAME {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("presence: frame of {len} bytes is over the {MAX_FRAME} limit"),
        ));
    }
    let mut body = vec![0u8; len as usize];
    r.read_exact(&mut body)?;
    Ok((op, body))
}

fn write_frame(w: &mut impl Write, op: u32, payload: &impl serde::Serialize) -> io::Result<()> {
    let body = serde_json::to_vec(payload).map_err(io::Error::other)?;
    write_frame_raw(w, op, &body)
}

fn write_frame_raw(w: &mut impl Write, op: u32, body: &[u8]) -> io::Result<()> {
    let mut head = [0u8; 8];
    head[0..4].copy_from_slice(&op.to_le_bytes());
    head[4..8].copy_from_slice(&(body.len() as u32).to_le_bytes());
    w.write_all(&head)?;
    w.write_all(body)?;
    w.flush()?;
    Ok(())
}

struct Listener {
    path: String,
    inner: ListenerInner,
}

enum ListenerInner {
    #[cfg(unix)]
    Unix(std::os::unix::net::UnixListener),
    #[cfg(windows)]
    Windows(WindowsListener),
}

enum IpcStream {
    #[cfg(unix)]
    Unix(std::os::unix::net::UnixStream),
    #[cfg(windows)]
    Windows(WindowsPipe),
}

impl Read for IpcStream {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        match self {
            #[cfg(unix)]
            IpcStream::Unix(s) => s.read(buf),
            #[cfg(windows)]
            IpcStream::Windows(s) => s.read(buf),
        }
    }
}

impl Write for IpcStream {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        match self {
            #[cfg(unix)]
            IpcStream::Unix(s) => s.write(buf),
            #[cfg(windows)]
            IpcStream::Windows(s) => s.write(buf),
        }
    }

    fn flush(&mut self) -> io::Result<()> {
        match self {
            #[cfg(unix)]
            IpcStream::Unix(s) => s.flush(),
            #[cfg(windows)]
            IpcStream::Windows(_) => Ok(()),
        }
    }
}

fn listen(path: &str) -> io::Result<Listener> {
    #[cfg(unix)]
    {
        listen_unix(path)
    }
    #[cfg(windows)]
    {
        listen_windows(path)
    }
    #[cfg(not(any(unix, windows)))]
    {
        let _ = path;
        Err(io::Error::other("presence unsupported on this platform"))
    }
}

fn cleanup(path: &str) {
    #[cfg(unix)]
    {
        let _ = std::fs::remove_file(path);
    }
    let _ = path;
}

impl Listener {
    fn accept(&self, stop: &AtomicBool) -> io::Result<IpcStream> {
        match &self.inner {
            #[cfg(unix)]
            ListenerInner::Unix(l) => loop {
                if stop.load(Ordering::SeqCst) {
                    return Err(io::Error::new(io::ErrorKind::Interrupted, "stopped"));
                }
                match l.accept() {
                    Ok((stream, _)) => {
                        let _ = stream.set_nonblocking(false);
                        return Ok(IpcStream::Unix(stream));
                    }
                    Err(err) if err.kind() == io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(50));
                    }
                    Err(err) => return Err(err),
                }
            },
            #[cfg(windows)]
            ListenerInner::Windows(l) => l.accept(stop),
        }
    }
}

#[cfg(unix)]
fn listen_unix(path: &str) -> io::Result<Listener> {
    use std::os::unix::fs::DirBuilderExt;
    use std::os::unix::net::{UnixListener, UnixStream};
    use std::path::Path;

    let p = Path::new(path);
    if p.exists() {
        if UnixStream::connect(path).is_ok() {
            return Err(io::Error::other(format!(
                "{path} is already served by something else"
            )));
        }
        std::fs::remove_file(path)
            .map_err(|e| io::Error::other(format!("removing the stale socket at {path}: {e}")))?;
    }
    if let Some(dir) = p.parent() {
        if !dir.as_os_str().is_empty() && !dir.exists() {
            std::fs::DirBuilder::new()
                .mode(0o700)
                .recursive(true)
                .create(dir)?;
        }
    }
    let unix = UnixListener::bind(path)?;
    unix.set_nonblocking(true)?;
    Ok(Listener {
        path: path.to_string(),
        inner: ListenerInner::Unix(unix),
    })
}

#[cfg(windows)]
struct WindowsPipe {
    handle: windows_sys::Win32::Foundation::HANDLE,
}

#[cfg(windows)]
unsafe impl Send for WindowsPipe {}

#[cfg(windows)]
struct WindowsListener {
    path: String,
    pending: Mutex<windows_sys::Win32::Foundation::HANDLE>,
}

#[cfg(windows)]
unsafe impl Send for WindowsListener {}

#[cfg(windows)]
fn listen_windows(path: &str) -> io::Result<Listener> {
    if !path.to_ascii_lowercase().starts_with(r"\\.\pipe\") {
        return Err(io::Error::other(format!(
            "Windows presence endpoint {path:?} must start with \\\\.\\pipe\\"
        )));
    }
    let first = create_pipe(path, true).map_err(|e| {
        io::Error::other(format!(
            "{path} is already served or cannot be created: {e}"
        ))
    })?;
    Ok(Listener {
        path: path.to_string(),
        inner: ListenerInner::Windows(WindowsListener {
            path: path.to_string(),
            pending: Mutex::new(first),
        }),
    })
}

#[cfg(windows)]
fn create_pipe(path: &str, first: bool) -> io::Result<windows_sys::Win32::Foundation::HANDLE> {
    use windows_sys::Win32::Foundation::{LocalFree, INVALID_HANDLE_VALUE};
    use windows_sys::Win32::Security::Authorization::ConvertStringSecurityDescriptorToSecurityDescriptorW;
    use windows_sys::Win32::Security::SECURITY_ATTRIBUTES;
    use windows_sys::Win32::Storage::FileSystem::{
        FILE_FLAG_FIRST_PIPE_INSTANCE, PIPE_ACCESS_DUPLEX,
    };
    use windows_sys::Win32::System::Pipes::{
        CreateNamedPipeW, PIPE_READMODE_BYTE, PIPE_REJECT_REMOTE_CLIENTS, PIPE_TYPE_BYTE,
        PIPE_UNLIMITED_INSTANCES, PIPE_WAIT,
    };

    const SDDL: &str = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;OW)(A;;GRGW;;;AU)";
    const SDDL_REVISION_1: u32 = 1;

    let name = crate::overlay::win32::wide(path);
    let sddl = crate::overlay::win32::wide(SDDL);
    let mut sd = std::ptr::null_mut();
    let ok = unsafe {
        ConvertStringSecurityDescriptorToSecurityDescriptorW(
            sddl.as_ptr(),
            SDDL_REVISION_1,
            &mut sd,
            std::ptr::null_mut(),
        )
    };
    if ok == 0 {
        return Err(io::Error::last_os_error());
    }
    let sa = SECURITY_ATTRIBUTES {
        nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: sd,
        bInheritHandle: 0,
    };
    let mut mode = PIPE_ACCESS_DUPLEX;
    if first {
        mode |= FILE_FLAG_FIRST_PIPE_INSTANCE;
    }
    let handle = unsafe {
        CreateNamedPipeW(
            name.as_ptr(),
            mode,
            PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT | PIPE_REJECT_REMOTE_CLIENTS,
            PIPE_UNLIMITED_INSTANCES,
            MAX_FRAME + 8,
            MAX_FRAME + 8,
            0,
            &sa,
        )
    };
    unsafe { LocalFree(sd as _) };
    if handle.is_null() || handle == INVALID_HANDLE_VALUE {
        return Err(io::Error::last_os_error());
    }
    Ok(handle)
}

#[cfg(windows)]
impl Read for WindowsPipe {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        use windows_sys::Win32::Foundation::INVALID_HANDLE_VALUE;
        use windows_sys::Win32::Storage::FileSystem::ReadFile;
        if self.handle.is_null() || self.handle == INVALID_HANDLE_VALUE {
            return Err(io::Error::new(io::ErrorKind::NotConnected, "closed"));
        }
        let mut n = 0u32;
        let ok = unsafe {
            ReadFile(
                self.handle,
                buf.as_mut_ptr(),
                buf.len() as u32,
                &mut n,
                std::ptr::null_mut(),
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(n as usize)
    }
}

#[cfg(windows)]
impl Write for WindowsPipe {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        use windows_sys::Win32::Foundation::INVALID_HANDLE_VALUE;
        use windows_sys::Win32::Storage::FileSystem::WriteFile;
        if self.handle.is_null() || self.handle == INVALID_HANDLE_VALUE {
            return Err(io::Error::new(io::ErrorKind::NotConnected, "closed"));
        }
        let mut n = 0u32;
        let ok = unsafe {
            WriteFile(
                self.handle,
                buf.as_ptr(),
                buf.len() as u32,
                &mut n,
                std::ptr::null_mut(),
            )
        };
        if ok == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(n as usize)
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[cfg(windows)]
impl Drop for WindowsPipe {
    fn drop(&mut self) {
        use windows_sys::Win32::Foundation::{CloseHandle, INVALID_HANDLE_VALUE};
        if !self.handle.is_null() && self.handle != INVALID_HANDLE_VALUE {
            unsafe { CloseHandle(self.handle) };
            self.handle = std::ptr::null_mut();
        }
    }
}

#[cfg(windows)]
impl WindowsListener {
    fn accept(&self, stop: &AtomicBool) -> io::Result<IpcStream> {
        use windows_sys::Win32::Foundation::{
            GetLastError, ERROR_PIPE_CONNECTED, INVALID_HANDLE_VALUE,
        };
        use windows_sys::Win32::System::Pipes::ConnectNamedPipe;

        if stop.load(Ordering::SeqCst) {
            return Err(io::Error::new(io::ErrorKind::Interrupted, "stopped"));
        }
        let handle = *self.pending.lock().unwrap();
        if handle.is_null() || handle == INVALID_HANDLE_VALUE {
            return Err(io::Error::other("pipe closed"));
        }
        let ok = unsafe { ConnectNamedPipe(handle, std::ptr::null_mut()) };
        if ok == 0 {
            let err = unsafe { GetLastError() };
            if err != ERROR_PIPE_CONNECTED {
                if stop.load(Ordering::SeqCst) {
                    return Err(io::Error::new(io::ErrorKind::Interrupted, "stopped"));
                }
                return Err(io::Error::from_raw_os_error(err as i32));
            }
        }
        let next = create_pipe(&self.path, false).unwrap_or(std::ptr::null_mut());
        *self.pending.lock().unwrap() = next;
        Ok(IpcStream::Windows(WindowsPipe { handle }))
    }
}

#[cfg(windows)]
impl Drop for WindowsListener {
    fn drop(&mut self) {
        use windows_sys::Win32::Foundation::{
            CloseHandle, GetLastError, ERROR_PIPE_BUSY, GENERIC_READ, GENERIC_WRITE,
            INVALID_HANDLE_VALUE,
        };
        use windows_sys::Win32::Storage::FileSystem::{CreateFileW, OPEN_EXISTING};

        let handle = {
            let mut pending = self.pending.lock().unwrap();
            std::mem::replace(&mut *pending, std::ptr::null_mut())
        };
        if handle.is_null() || handle == INVALID_HANDLE_VALUE {
            return;
        }
        let name = crate::overlay::win32::wide(&self.path);
        let wake = unsafe {
            CreateFileW(
                name.as_ptr(),
                GENERIC_READ | GENERIC_WRITE,
                0,
                std::ptr::null(),
                OPEN_EXISTING,
                0,
                std::ptr::null_mut(),
            )
        };
        if !wake.is_null() && wake != INVALID_HANDLE_VALUE {
            unsafe { CloseHandle(wake) };
        } else {
            let err = unsafe { GetLastError() };
            if err != ERROR_PIPE_BUSY {
                unsafe { CloseHandle(handle) };
                return;
            }
        }
        unsafe { CloseHandle(handle) };
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::data::citymap;
    use std::io::Cursor;

    #[test]
    fn parse_presence_details() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let cases: &[(&str, PresenceState)] = &[
            (
                "Inner City 1054 x 986",
                PresenceState {
                    has_position: true,
                    x: 1054,
                    y: 986,
                    place: "Inner City".into(),
                    ..PresenceState::default()
                },
            ),
            (
                "Inner City 1055 x 985",
                PresenceState {
                    has_position: true,
                    x: 1055,
                    y: 985,
                    place: "Inner City".into(),
                    ..PresenceState::default()
                },
            ),
            (
                "Hospital 1058 x 1016",
                PresenceState {
                    has_position: true,
                    x: 1058,
                    y: 1016,
                    place: "Hospital".into(),
                    indoors: true,
                    ..PresenceState::default()
                },
            ),
            (
                "Secronom Bunker",
                PresenceState {
                    in_outpost: true,
                    outpost_name: "Secronom Bunker".into(),
                    ..PresenceState::default()
                },
            ),
            (
                "Nastya's Holdout",
                PresenceState {
                    in_outpost: true,
                    outpost_name: "Nastya's Holdout".into(),
                    ..PresenceState::default()
                },
            ),
            (
                "Loading...",
                PresenceState {
                    loading: true,
                    ..PresenceState::default()
                },
            ),
            ("", PresenceState::default()),
            ("Somewhere New", PresenceState::default()),
            ("Inner City 1054", PresenceState::default()),
            ("Inner City abc x def", PresenceState::default()),
        ];
        for (details, mut want) in cases.iter().cloned() {
            want.at = now;
            want.details = details.to_string();
            let got = parse_details(details, now);
            assert_eq!(got, want, "{details:?}");
        }
    }

    #[test]
    fn presence_position_is_a_city_block() {
        let got = parse_details(
            "Inner City 1054 x 986",
            DateTime::from_timestamp(1000, 0).unwrap(),
        );
        assert!(got.has_position);
        assert!(citymap::default().is_block(got.x, got.y));
    }

    #[test]
    fn presence_frame_round_trip() {
        let mut buf = Cursor::new(Vec::new());
        write_frame(&mut buf, OP_FRAME, &json!({"cmd": "SET_ACTIVITY"})).unwrap();
        let bytes = buf.get_ref();
        let op = u32::from_le_bytes(bytes[0..4].try_into().unwrap());
        assert_eq!(op, OP_FRAME);
        let n = u32::from_le_bytes(bytes[4..8].try_into().unwrap());
        assert_eq!(n as usize, bytes.len() - 8);
        buf.set_position(0);
        let (op, body) = read_frame(&mut buf).unwrap();
        assert_eq!(op, OP_FRAME);
        assert_eq!(
            String::from_utf8(body).unwrap(),
            r#"{"cmd":"SET_ACTIVITY"}"#
        );
    }

    #[test]
    fn presence_frame_refuses_absurd_length() {
        let mut head = [0u8; 8];
        head[0..4].copy_from_slice(&OP_FRAME.to_le_bytes());
        head[4..8].copy_from_slice(&(1u32 << 30).to_le_bytes());
        let err = read_frame(&mut Cursor::new(head)).unwrap_err();
        assert!(err.to_string().contains("over the"));
    }

    fn test_server(on_state: Arc<dyn Fn(PresenceState) + Send + Sync>) -> Server {
        Server {
            on_state,
            on_connection: Arc::new(|_| {}),
            last: Mutex::new(None),
            clients: AtomicUsize::new(0),
        }
    }

    #[test]
    fn presence_handles_null_args() {
        let got = Arc::new(AtomicUsize::new(0));
        let server = test_server({
            let got = got.clone();
            Arc::new(move |_| {
                got.fetch_add(1, Ordering::SeqCst);
            })
        });
        apply_activity(&server, Some(Value::Null));
        apply_activity(&server, None);
        apply_activity(&server, Some(json!({"pid": 1})));
        assert_eq!(got.load(Ordering::SeqCst), 0);
    }

    #[test]
    fn presence_applies_activity() {
        let got = Arc::new(Mutex::new(PresenceState::default()));
        let server = test_server({
            let got = got.clone();
            Arc::new(move |s| {
                *got.lock().unwrap() = s;
            })
        });
        apply_activity(
            &server,
            Some(json!({
                "pid": 42,
                "activity": {
                    "details": "Inner City 1054 x 986",
                    "state": "Multiplayer"
                }
            })),
        );
        let got = got.lock().unwrap().clone();
        assert!(got.has_position);
        assert_eq!(got.x, 1054);
        assert_eq!(got.y, 986);
        let last = server.last.lock().unwrap().clone().unwrap();
        assert_eq!(last.x, 1054);
    }

    #[cfg(unix)]
    #[test]
    fn presence_reports_connection_lifecycle() {
        use std::os::unix::net::UnixStream;

        let changes = Arc::new(Mutex::new(Vec::new()));
        let server = Arc::new(Server {
            on_state: Arc::new(|_| {}),
            on_connection: {
                let changes = changes.clone();
                Arc::new(move |c| changes.lock().unwrap().push(c))
            },
            last: Mutex::new(None),
            clients: AtomicUsize::new(0),
        });
        let (a, b) = UnixStream::pair().unwrap();
        let t = {
            let server = server.clone();
            thread::spawn(move || serve_conn(&server, IpcStream::Unix(a)))
        };
        let deadline = std::time::Instant::now() + Duration::from_secs(1);
        while changes.lock().unwrap().is_empty() {
            if std::time::Instant::now() > deadline {
                panic!("timed out waiting for connection callback");
            }
            thread::sleep(Duration::from_millis(5));
        }
        assert_eq!(*changes.lock().unwrap(), vec![true]);
        drop(b);
        t.join().unwrap();
        assert_eq!(*changes.lock().unwrap(), vec![true, false]);
        assert_eq!(server.clients.load(Ordering::SeqCst), 0);
    }
}
