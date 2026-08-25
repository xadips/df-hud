//! Cross-thread overlay wakeup: Linux pipe fd, Windows event HANDLE.
//! [`Notify`] is the condvar used by process and visibility watchers.

use std::io;
use std::sync::{Condvar, Mutex};
use std::time::Duration;

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

unsafe impl Send for Wake {}
unsafe impl Sync for Wake {}

impl Wake {
    pub fn new() -> io::Result<Self> {
        #[cfg(unix)]
        {
            let mut fds = [0i32; 2];
            if unsafe { libc::pipe(fds.as_mut_ptr()) } != 0 {
                return Err(io::Error::last_os_error());
            }
            unsafe {
                let flags = libc::fcntl(fds[0], libc::F_GETFL);
                libc::fcntl(fds[0], libc::F_SETFL, flags | libc::O_NONBLOCK);
            }
            use std::os::fd::FromRawFd;
            Ok(Self {
                read: Mutex::new(unsafe { std::fs::File::from_raw_fd(fds[0]) }),
                write: Mutex::new(unsafe { std::fs::File::from_raw_fd(fds[1]) }),
            })
        }
        #[cfg(windows)]
        {
            use windows_sys::Win32::System::Threading::CreateEventW;
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
            let _ = self.write.lock().unwrap().write_all(&[1u8]);
        }
        #[cfg(windows)]
        {
            use windows_sys::Win32::System::Threading::SetEvent;
            unsafe { SetEvent(self.event) };
        }
    }

    pub fn take(&self) {
        #[cfg(unix)]
        {
            use std::io::Read;
            let mut buf = [0u8; 32];
            let mut f = self.read.lock().unwrap();
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
            unsafe { ResetEvent(self.event) };
        }
    }

    #[cfg(unix)]
    pub fn read_fd(&self) -> RawFd {
        use std::os::fd::AsRawFd;
        self.read.lock().unwrap().as_raw_fd()
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
        *self.flag.lock().unwrap() = true;
        self.cvar.notify_all();
    }

    pub fn wait(&self) {
        let mut g = self.flag.lock().unwrap();
        if *g {
            *g = false;
            return;
        }
        let mut g = self.cvar.wait(g).unwrap();
        *g = false;
    }

    pub fn wait_timeout(&self, d: Duration) {
        let mut g = self.flag.lock().unwrap();
        if *g {
            *g = false;
            return;
        }
        let (mut g, _) = self.cvar.wait_timeout(g, d).unwrap();
        *g = false;
    }
}

impl Default for Notify {
    fn default() -> Self {
        Self::new()
    }
}
