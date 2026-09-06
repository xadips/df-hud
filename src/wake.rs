//! Cross-thread overlay wakeup: Linux pipe fd, Windows event HANDLE.
//! [`Notify`] is the condvar used by process and visibility watchers, and
//! [`lock`] is the one way non-test code takes a `Mutex`.

use std::io;
use std::sync::{Condvar, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

/// Takes the mutex, ignoring poisoning.
///
/// Release builds are `panic = "abort"`, so a mutex is never poisoned there.
/// In dev and test builds a poisoned guard still protects a valid `T`; taking
/// it keeps the thread that hit the poison from panicking too, which would
/// bury the original panic under a cascade of "PoisonError" aborts.
pub(crate) fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(PoisonError::into_inner)
}

#[cfg(unix)]
use std::os::fd::RawFd;

#[cfg(windows)]
use windows_sys::Win32::Foundation::HANDLE;

pub struct Wake {
    #[cfg(unix)]
    read: Mutex<std::fs::File>,
    #[cfg(unix)]
    write: Mutex<std::fs::File>,
    #[cfg(windows)]
    event: HANDLE,
}

// On Unix the two `Mutex<File>` fields make `Wake` Send + Sync on their own.
// SAFETY: `event` is a kernel event object; SetEvent/ResetEvent/WaitForSingleObject
// are thread-safe on a shared HANDLE, and it is closed only in `Drop`.
#[cfg(windows)]
unsafe impl Send for Wake {}
// SAFETY: as for `Send`; no call through `&Wake` mutates Rust-side state.
#[cfg(windows)]
unsafe impl Sync for Wake {}

impl Wake {
    pub fn new() -> io::Result<Self> {
        #[cfg(unix)]
        {
            // CLOEXEC: the pipe must not leak into `xdg-open` and friends.
            // Non-blocking on both ends: `take` drains until empty, and a
            // `ping` into a full pipe is dropped because a wake is already
            // pending. That also keeps the SIGHUP handler's write from
            // blocking.
            let mut fds = [0i32; 2];
            // SAFETY: `fds` is a live `[c_int; 2]`, exactly what pipe2 writes into.
            if unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC | libc::O_NONBLOCK) } != 0 {
                return Err(io::Error::last_os_error());
            }
            use std::os::fd::FromRawFd;
            // SAFETY: pipe2 succeeded, so both fds are open and owned by no one
            // else yet; each `File` takes sole ownership of one and closes it.
            let (read, write) = unsafe {
                (
                    std::fs::File::from_raw_fd(fds[0]),
                    std::fs::File::from_raw_fd(fds[1]),
                )
            };
            Ok(Self {
                read: Mutex::new(read),
                write: Mutex::new(write),
            })
        }
        #[cfg(windows)]
        {
            use windows_sys::Win32::System::Threading::CreateEventW;
            // SAFETY: null security attributes and null name are the documented
            // defaults; the other arguments are flags (manual reset, unsignalled).
            let event = unsafe { CreateEventW(std::ptr::null(), 1, 0, std::ptr::null()) };
            if event.is_null() {
                return Err(io::Error::last_os_error());
            }
            Ok(Self { event })
        }
        #[cfg(not(any(unix, windows)))]
        {
            let _ = Mutex::new(());
            Err(io::Error::other("wake unsupported on this platform"))
        }
    }

    pub fn ping(&self) {
        #[cfg(unix)]
        {
            use std::io::Write;
            let _ = lock(&self.write).write_all(&[1u8]);
        }
        #[cfg(windows)]
        {
            use windows_sys::Win32::System::Threading::SetEvent;
            // SAFETY: `self.event` is the handle `new` created; it stays open until `Drop`.
            unsafe { SetEvent(self.event) };
        }
    }

    pub fn take(&self) {
        #[cfg(unix)]
        {
            use std::io::Read;
            let mut buf = [0u8; 32];
            let mut f = lock(&self.read);
            loop {
                match f.read(&mut buf) {
                    Ok(0) | Err(_) => break,
                    Ok(_) => {}
                }
            }
        }
        #[cfg(windows)]
        {
            use windows_sys::Win32::System::Threading::ResetEvent;
            // SAFETY: `self.event` is the handle `new` created; it stays open until `Drop`.
            unsafe { ResetEvent(self.event) };
        }
    }

    /// Blocks until a `ping` is pending (or, on Unix, a signal interrupts the
    /// wait). Does not consume it: call [`take`](Self::take) afterwards. For
    /// loops with nothing else to wait on, such as `--headless`; the surface
    /// loops poll the fd alongside their display connection instead.
    pub fn wait(&self) {
        #[cfg(unix)]
        {
            let mut pfd = libc::pollfd {
                fd: self.read_fd(),
                events: libc::POLLIN,
                revents: 0,
            };
            // EINTR is a wake in its own right: SIGHUP's handler has written
            // to the pipe, and `take` runs the reload.
            // SAFETY: `pfd` is one live pollfd and nfds is 1; the fd stays open
            // because `self.read` owns it for as long as `self` exists.
            unsafe { libc::poll(&mut pfd, 1, -1) };
        }
        #[cfg(windows)]
        {
            use windows_sys::Win32::System::Threading::{INFINITE, WaitForSingleObject};
            // SAFETY: `self.event` is the handle `new` created; it stays open until `Drop`.
            unsafe { WaitForSingleObject(self.event, INFINITE) };
        }
    }

    #[cfg(unix)]
    pub fn read_fd(&self) -> RawFd {
        use std::os::fd::AsRawFd;
        lock(&self.read).as_raw_fd()
    }

    /// For a signal handler, which may `write(2)` but not lock or allocate.
    /// The fd stays open for as long as `self` does; `app::catch_sighup`
    /// stores it in a static, so the `Handle` owning this `Wake` must live
    /// for the rest of the process (it does: the handler is never uninstalled).
    #[cfg(unix)]
    pub fn write_fd(&self) -> RawFd {
        use std::os::fd::AsRawFd;
        lock(&self.write).as_raw_fd()
    }

    #[cfg(windows)]
    pub fn event_handle(&self) -> HANDLE {
        self.event
    }
}

impl Drop for Wake {
    fn drop(&mut self) {
        #[cfg(windows)]
        {
            use windows_sys::Win32::Foundation::CloseHandle;
            if !self.event.is_null() {
                // SAFETY: the handle `new` created is closed exactly once, here,
                // after the last reference to `self` is gone.
                unsafe { CloseHandle(self.event) };
            }
        }
    }
}

pub struct Notify {
    flag: Mutex<bool>,
    cvar: Condvar,
}

impl Notify {
    pub fn new() -> Self {
        Self {
            flag: Mutex::new(false),
            cvar: Condvar::new(),
        }
    }

    pub fn ping(&self) {
        *lock(&self.flag) = true;
        self.cvar.notify_all();
    }

    pub fn wait(&self) {
        let mut g = lock(&self.flag);
        if *g {
            *g = false;
            return;
        }
        let mut g = self.cvar.wait(g).unwrap();
        *g = false;
    }

    pub fn wait_timeout(&self, d: Duration) {
        self.wait_pinged(d);
    }

    /// Like `wait_timeout`, telling a ping apart from the timeout running out.
    pub fn wait_pinged(&self, d: Duration) -> bool {
        let mut g = lock(&self.flag);
        if *g {
            *g = false;
            return true;
        }
        let (mut g, _) = self.cvar.wait_timeout(g, d).unwrap();
        let pinged = *g;
        *g = false;
        pinged
    }
}

impl Default for Notify {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wait_pinged_tells_a_ping_from_the_timeout() {
        let n = Notify::new();
        assert!(!n.wait_pinged(Duration::from_millis(1)));
        n.ping();
        assert!(n.wait_pinged(Duration::from_secs(5)));
        assert!(
            !n.wait_pinged(Duration::from_millis(1)),
            "a ping is consumed once"
        );
    }

    #[cfg(unix)]
    #[test]
    fn pipe_is_cloexec_and_nonblocking() {
        let w = Wake::new().unwrap();
        for fd in [w.read_fd(), w.write_fd()] {
            // SAFETY: F_GETFD/F_GETFL take no pointer argument; `w` keeps `fd` open.
            let (fd_flags, fl_flags) = unsafe {
                (
                    libc::fcntl(fd, libc::F_GETFD),
                    libc::fcntl(fd, libc::F_GETFL),
                )
            };
            assert_ne!(
                fd_flags & libc::FD_CLOEXEC,
                0,
                "fd {fd} would leak into children"
            );
            assert_ne!(fl_flags & libc::O_NONBLOCK, 0, "fd {fd} could block");
        }
    }

    #[test]
    fn wait_returns_once_pinged_and_take_clears_it() {
        use std::sync::Arc;
        let w = Arc::new(Wake::new().unwrap());
        w.ping();
        w.wait();
        w.take();
        let waiter = {
            let w = w.clone();
            std::thread::spawn(move || {
                let started = std::time::Instant::now();
                w.wait();
                w.take();
                started.elapsed()
            })
        };
        std::thread::sleep(Duration::from_millis(30));
        w.ping();
        let waited = waiter.join().unwrap();
        assert!(
            waited >= Duration::from_millis(20),
            "returned before the ping: {waited:?}"
        );
    }

    #[cfg(unix)]
    #[test]
    fn ping_then_take_drains_and_a_full_pipe_does_not_block() {
        let w = Wake::new().unwrap();
        for _ in 0..200_000 {
            w.ping();
        }
        w.take();
        let mut pfd = libc::pollfd {
            fd: w.read_fd(),
            events: libc::POLLIN,
            revents: 0,
        };
        // SAFETY: `pfd` is one live pollfd, nfds is 1, and `w` keeps the fd open.
        let drained = unsafe { libc::poll(&mut pfd, 1, 0) };
        assert_eq!(drained, 0, "drained");
        w.ping();
        // SAFETY: as above.
        let readable = unsafe { libc::poll(&mut pfd, 1, 0) };
        assert_eq!(readable, 1, "readable again");
    }
}
