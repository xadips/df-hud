//! Parse the game client's Discord rich-presence `details` string, and serve
//! the fake Discord IPC endpoint the client publishes to.

use crate::wake::lock;
use chrono::{DateTime, Utc};
use serde::Deserialize;
use serde_json::{Value, json};
use std::io::{self, Read, Write};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;

use crate::data::citymap;
use crate::model::PresenceState;
use crate::wake::Wake;

const INNER_CITY: &str = "Inner City";

const OP_HANDSHAKE: u32 = 0;
const OP_FRAME: u32 = 1;
const OP_CLOSE: u32 = 2;
const OP_PING: u32 = 3;
const OP_PONG: u32 = 4;
const MAX_FRAME: u32 = 64 << 10;
/// Connections served at once; one thread each. The game opens one IPC
/// connection, so anything past this is a stuck or misbehaving peer.
const MAX_CLIENTS: usize = 8;

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
        s.position = Some((x, y));
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
///
/// `wake` is the server thread's only alarm clock: it is polled next to the
/// Unix listener fd while accepting, and waited on while the bind is failed,
/// so the thread never wakes on a timer. A ping means "look at `retry` and
/// the stop flag".
pub struct Control {
    bind_failed: AtomicBool,
    retry: AtomicBool,
    wake: Wake,
}

impl Control {
    pub fn new() -> io::Result<Arc<Self>> {
        Ok(Arc::new(Self {
            bind_failed: AtomicBool::new(false),
            retry: AtomicBool::new(false),
            wake: Wake::new()?,
        }))
    }

    pub fn bind_failed(&self) -> bool {
        self.bind_failed.load(Ordering::SeqCst)
    }

    /// Asks for another bind. `false` when there is nothing to retry.
    pub fn retry(&self) -> bool {
        if !self.bind_failed() {
            return false;
        }
        self.retry.store(true, Ordering::SeqCst);
        self.wake.ping();
        true
    }

    /// Wakes the server thread so it notices the stop flag.
    pub fn poke(&self) {
        self.wake.ping();
    }
}

pub fn serve(
    path: &str,
    on_state: impl Fn(PresenceState) + Send + Sync + 'static,
    on_connection: impl Fn(bool) + Send + Sync + 'static,
    control: Arc<Control>,
    stop: Arc<AtomicBool>,
) {
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
                info!("presence: listening on {path}");
                accept_loop(server, listener, &stop, &control);
                if stop.load(Ordering::SeqCst) {
                    return;
                }
                control.bind_failed.store(true, Ordering::SeqCst);
                warn!("presence: listener ended; position will come from the poll until retried");
            }
            Err(err) => {
                control.bind_failed.store(true, Ordering::SeqCst);
                error!(
                    "presence: not listening ({err}); position will come from the poll until retried"
                );
            }
        }
        loop {
            if stop.load(Ordering::SeqCst) {
                return;
            }
            if control.retry.swap(false, Ordering::SeqCst) {
                info!("presence: retrying IPC bind");
                break;
            }
            control.wake.wait();
            control.wake.take();
        }
    }
}

struct Server {
    on_state: Arc<dyn Fn(PresenceState) + Send + Sync>,
    on_connection: Arc<dyn Fn(bool) + Send + Sync>,
    last: Mutex<Option<PresenceState>>,
    clients: AtomicUsize,
}

/// One connection's place under [`MAX_CLIENTS`]. Released on drop, so a
/// handler that panics still gives it back.
struct Slot(Arc<AtomicUsize>);

impl Drop for Slot {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::SeqCst);
    }
}

fn accept_loop(server: Arc<Server>, listener: Listener, stop: &AtomicBool, control: &Control) {
    let path = listener.path.clone();
    let in_flight = Arc::new(AtomicUsize::new(0));
    loop {
        if stop.load(Ordering::SeqCst) {
            break;
        }
        match listener.accept(stop, &control.wake) {
            Ok(stream) => {
                if in_flight.fetch_add(1, Ordering::SeqCst) >= MAX_CLIENTS {
                    in_flight.fetch_sub(1, Ordering::SeqCst);
                    warn!("presence: dropping a connection ({MAX_CLIENTS} already open)");
                    drop(stream);
                    continue;
                }
                let server = server.clone();
                let slot = Slot(in_flight.clone());
                // A failed spawn drops the closure, and the slot with it.
                if let Err(err) = thread::Builder::new()
                    .name("df-hud-presence-conn".into())
                    .spawn(move || {
                        let _slot = slot;
                        serve_conn(&server, stream);
                    })
                {
                    warn!("presence: could not start a connection thread ({err})");
                }
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
                warn!("presence: accept ({err})");
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
        info!("presence: client connected");
        (server.on_connection)(true);
    }
    loop {
        match read_frame(&mut stream) {
            Ok((op, body)) => {
                if let Err(err) = handle(server, &mut stream, op, &body) {
                    if err.kind() != io::ErrorKind::UnexpectedEof {
                        warn!("presence: {err}");
                    }
                    break;
                }
            }
            Err(err) => {
                if err.kind() != io::ErrorKind::UnexpectedEof
                    && err.kind() != io::ErrorKind::ConnectionAborted
                {
                    warn!("presence: connection ended ({err})");
                }
                break;
            }
        }
    }
    let last = server.clients.fetch_sub(1, Ordering::SeqCst) == 1;
    if last {
        info!("presence: client disconnected");
        (server.on_connection)(false);
    }
}

fn handle(server: &Server, stream: &mut IpcStream, op: u32, body: &[u8]) -> io::Result<()> {
    match op {
        OP_HANDSHAKE => {
            debug!("presence: handshake received");
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
                    warn!("presence: unparsable frame ({err})");
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

    let mut last = lock(&server.last);
    let unknown = !state.details.is_empty()
        && state.position.is_none()
        && !state.in_outpost
        && !state.loading
        && last.as_ref().is_none_or(|p| p.details != state.details);
    let kind = presence_kind(&state);
    let kind_changed = last.as_ref().is_none_or(|p| presence_kind(p) != kind);
    *last = Some(state.clone());
    drop(last);

    if kind_changed {
        info!("presence: {kind}");
    }
    if unknown {
        warn!(
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
    if let Some((x, y)) = s.position {
        return if s.indoors {
            format!("{} {x},{y}", s.place)
        } else {
            format!("inner city {x},{y}")
        };
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
    let len = u32::try_from(body.len())
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "presence frame is too large"))?;
    if len > MAX_FRAME {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("presence: frame of {len} bytes is over the {MAX_FRAME} limit"),
        ));
    }
    let mut head = [0u8; 8];
    head[0..4].copy_from_slice(&op.to_le_bytes());
    head[4..8].copy_from_slice(&len.to_le_bytes());
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
    /// Blocks until a client connects or `wake` is pinged with `stop` set.
    /// On Windows `wake` is unused: `ConnectNamedPipe` blocks on its own and
    /// the process exit ends the thread.
    fn accept(&self, stop: &AtomicBool, wake: &Wake) -> io::Result<IpcStream> {
        #[cfg(windows)]
        let _ = wake;
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
                        wait_readable(l, wake);
                        wake.take();
                    }
                    Err(err) => return Err(err),
                }
            },
            #[cfg(windows)]
            ListenerInner::Windows(l) => l.accept(stop),
        }
    }
}

/// Sleeps until the listener has a pending connection or `wake` is pinged.
/// A signal (EINTR) or a listener error also returns; the caller's `accept`
/// then reports it.
#[cfg(unix)]
fn wait_readable(listener: &std::os::unix::net::UnixListener, wake: &Wake) {
    use std::os::fd::AsRawFd;
    let mut fds = [
        libc::pollfd {
            fd: listener.as_raw_fd(),
            events: libc::POLLIN,
            revents: 0,
        },
        libc::pollfd {
            fd: wake.read_fd(),
            events: libc::POLLIN,
            revents: 0,
        },
    ];
    // SAFETY: `fds` is a live array of two pollfds and nfds is 2; both fds
    // stay open for the call because `listener` and `wake` are borrowed and
    // own them.
    unsafe { libc::poll(fds.as_mut_ptr(), 2, -1) };
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
    if let Some(dir) = p.parent()
        && !dir.as_os_str().is_empty()
        && !dir.exists()
    {
        std::fs::DirBuilder::new()
            .mode(0o700)
            .recursive(true)
            .create(dir)?;
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

// SAFETY: `handle` is an opaque kernel object handle, not memory this struct
// owns or dereferences; it is used by one connection thread and closed once
// in `Drop`.
#[cfg(windows)]
unsafe impl Send for WindowsPipe {}

#[cfg(windows)]
struct WindowsListener {
    path: String,
    pending: Mutex<windows_sys::Win32::Foundation::HANDLE>,
}

// SAFETY: the only non-Send field is the HANDLE inside `pending`, an opaque
// kernel object handle guarded by the mutex; it is not a pointer into memory
// this struct owns.
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
    use windows_sys::Win32::Foundation::{INVALID_HANDLE_VALUE, LocalFree};
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
    // SAFETY: `sddl` is a NUL-terminated u16 buffer that outlives the call;
    // `sd` is a live out-pointer, and a null size pointer is allowed.
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
    // SAFETY: `name` is a NUL-terminated u16 buffer and `sa` a live
    // SECURITY_ATTRIBUTES whose descriptor `sd` was just allocated; both
    // outlive the call. `sd` came from LocalAlloc inside the conversion above
    // and is freed exactly once, after the pipe has copied the descriptor.
    let handle = unsafe {
        let handle = CreateNamedPipeW(
            name.as_ptr(),
            mode,
            PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT | PIPE_REJECT_REMOTE_CLIENTS,
            PIPE_UNLIMITED_INSTANCES,
            MAX_FRAME + 8,
            MAX_FRAME + 8,
            0,
            &sa,
        );
        LocalFree(sd as _);
        handle
    };
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
        // SAFETY: `self.handle` is an open pipe handle (checked above) that this
        // stream owns; `buf` is writable for `buf.len()` bytes, `n` is a live
        // out-pointer, and a null OVERLAPPED means a synchronous read.
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
        // SAFETY: `self.handle` is an open pipe handle (checked above) that this
        // stream owns; `buf` is readable for `buf.len()` bytes, `n` is a live
        // out-pointer, and a null OVERLAPPED means a synchronous write.
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
            // SAFETY: the handle this stream owns is closed exactly once and
            // nulled so nothing can use it afterwards.
            unsafe { CloseHandle(self.handle) };
            self.handle = std::ptr::null_mut();
        }
    }
}

#[cfg(windows)]
impl WindowsListener {
    fn accept(&self, stop: &AtomicBool) -> io::Result<IpcStream> {
        use windows_sys::Win32::Foundation::{
            ERROR_PIPE_CONNECTED, GetLastError, INVALID_HANDLE_VALUE,
        };
        use windows_sys::Win32::System::Pipes::ConnectNamedPipe;

        if stop.load(Ordering::SeqCst) {
            return Err(io::Error::new(io::ErrorKind::Interrupted, "stopped"));
        }
        let handle = *lock(&self.pending);
        if handle.is_null() || handle == INVALID_HANDLE_VALUE {
            return Err(io::Error::other("pipe closed"));
        }
        // SAFETY: `handle` is the open, not yet connected pipe instance
        // `pending` holds (checked above); a null OVERLAPPED blocks until a
        // client connects or `Drop` connects to it to unblock this thread.
        // GetLastError reads this thread's last error, no arguments.
        let ok = unsafe { ConnectNamedPipe(handle, std::ptr::null_mut()) };
        if ok == 0 {
            // SAFETY: as above.
            let err = unsafe { GetLastError() };
            if err != ERROR_PIPE_CONNECTED {
                if stop.load(Ordering::SeqCst) {
                    return Err(io::Error::new(io::ErrorKind::Interrupted, "stopped"));
                }
                return Err(io::Error::from_raw_os_error(err as i32));
            }
        }
        let next = match create_pipe(&self.path, false) {
            Ok(handle) => handle,
            Err(err) => {
                warn!("presence: next pipe instance ({err})");
                std::ptr::null_mut()
            }
        };
        *lock(&self.pending) = next;
        Ok(IpcStream::Windows(WindowsPipe { handle }))
    }
}

#[cfg(windows)]
impl Drop for WindowsListener {
    fn drop(&mut self) {
        use windows_sys::Win32::Foundation::{
            CloseHandle, ERROR_PIPE_BUSY, GENERIC_READ, GENERIC_WRITE, GetLastError,
            INVALID_HANDLE_VALUE,
        };
        use windows_sys::Win32::Storage::FileSystem::{CreateFileW, OPEN_EXISTING};

        let handle = {
            let mut pending = lock(&self.pending);
            std::mem::replace(&mut *pending, std::ptr::null_mut())
        };
        if handle.is_null() || handle == INVALID_HANDLE_VALUE {
            return;
        }
        let name = crate::overlay::win32::wide(&self.path);
        // SAFETY: `name` is a NUL-terminated u16 buffer that outlives the call;
        // null security attributes and template are the defaults. Connecting
        // as a client unblocks the accept thread's ConnectNamedPipe. `wake`
        // and `handle` are each closed exactly once: `handle` was taken out
        // of `pending` above, so no other path can close it.
        unsafe {
            let wake = CreateFileW(
                name.as_ptr(),
                GENERIC_READ | GENERIC_WRITE,
                0,
                std::ptr::null(),
                OPEN_EXISTING,
                0,
                std::ptr::null_mut(),
            );
            if !wake.is_null() && wake != INVALID_HANDLE_VALUE {
                CloseHandle(wake);
            } else if GetLastError() != ERROR_PIPE_BUSY {
                CloseHandle(handle);
                return;
            }
            CloseHandle(handle);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::data::citymap;
    use std::io::Cursor;
    #[cfg(unix)]
    use std::time::Duration;

    #[test]
    fn parse_presence_details() {
        let now = DateTime::from_timestamp(1000, 0).unwrap();
        let cases: &[(&str, PresenceState)] = &[
            (
                "Inner City 1054 x 986",
                PresenceState {
                    position: Some((1054, 986)),
                    place: "Inner City".into(),
                    ..PresenceState::default()
                },
            ),
            (
                "Inner City 1055 x 985",
                PresenceState {
                    position: Some((1055, 985)),
                    place: "Inner City".into(),
                    ..PresenceState::default()
                },
            ),
            (
                "Hospital 1058 x 1016",
                PresenceState {
                    position: Some((1058, 1016)),
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
        let (x, y) = got.position.unwrap();
        assert!(citymap::default().is_block(x, y));
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

    #[test]
    fn presence_writer_refuses_oversized_frame() {
        let body = vec![0; MAX_FRAME as usize + 1];
        let err = write_frame_raw(&mut Vec::new(), OP_FRAME, &body).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::InvalidInput);
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
        assert_eq!(got.position, Some((1054, 986)));
        let last = server.last.lock().unwrap().clone().unwrap();
        assert_eq!(last.position, Some((1054, 986)));
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

    /// The accept loop blocks in `poll` with no timeout, so a stop has to be
    /// delivered through the wake fd; the thread must still leave promptly
    /// and take its socket with it.
    #[cfg(unix)]
    #[test]
    fn serve_blocks_without_a_timer_and_stops_on_poke() {
        use std::os::unix::net::UnixStream;

        let dir = std::env::temp_dir().join(format!("df-hud-presence-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("ipc").display().to_string();
        let control = Control::new().unwrap();
        let stop = Arc::new(AtomicBool::new(false));
        let states = Arc::new(Mutex::new(Vec::new()));
        let server = {
            let (path, control, stop, states) =
                (path.clone(), control.clone(), stop.clone(), states.clone());
            thread::spawn(move || {
                serve(
                    &path,
                    move |s| states.lock().unwrap().push(s.details),
                    |_| {},
                    control,
                    stop,
                )
            })
        };
        let deadline = std::time::Instant::now() + Duration::from_secs(2);
        while UnixStream::connect(&path).is_err() {
            assert!(std::time::Instant::now() < deadline, "never bound {path}");
            thread::sleep(Duration::from_millis(5));
        }
        assert!(!control.bind_failed());
        assert!(!control.retry(), "nothing to retry while listening");

        let mut client = UnixStream::connect(&path).unwrap();
        write_frame_raw(&mut client, OP_HANDSHAKE, br#"{"v":1,"client_id":"x"}"#).unwrap();
        let (op, _) = read_frame(&mut client).unwrap();
        assert_eq!(op, OP_FRAME, "handshake answered over the accepted stream");
        drop(client);

        let asked = std::time::Instant::now();
        stop.store(true, Ordering::SeqCst);
        control.poke();
        server.join().unwrap();
        assert!(
            asked.elapsed() < Duration::from_secs(1),
            "stop took {:?}",
            asked.elapsed()
        );
        assert!(!std::path::Path::new(&path).exists(), "socket cleaned up");
        let _ = std::fs::remove_dir_all(&dir);
    }
}
