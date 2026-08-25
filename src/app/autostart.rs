//! Windows login launch (HKCU Run) and opening the config in the shell.
//!
//! Linux has no tray equivalents: the user unit covers login, and the editor
//! is one `xdg-open` away. Stubs keep `Handle` methods identical.

use std::path::Path;
#[cfg(any(test, windows))]
use std::path::PathBuf;

pub fn available() -> bool {
    cfg!(windows)
}

#[cfg(any(test, windows))]
pub fn quote_executable(path: &str) -> String {
    format!("\"{path}\"")
}

#[cfg(windows)]
fn legacy_shortcut_path() -> PathBuf {
    legacy_shortcut_under(app_data_dir())
}

#[cfg(windows)]
fn app_data_dir() -> PathBuf {
    std::env::var("APPDATA")
        .ok()
        .filter(|s| !s.is_empty())
        .map(PathBuf::from)
        .unwrap_or_default()
}

#[cfg(any(test, windows))]
fn legacy_shortcut_under(app_data: impl AsRef<Path>) -> PathBuf {
    app_data
        .as_ref()
        .join("Microsoft")
        .join("Windows")
        .join("Start Menu")
        .join("Programs")
        .join("Startup")
        .join("df-hud.lnk")
}

#[cfg(not(windows))]
pub fn enabled() -> Result<bool, String> {
    Ok(false)
}

#[cfg(not(windows))]
pub fn set_enabled(_on: bool) -> Result<(), String> {
    Ok(())
}

#[cfg(not(windows))]
pub fn reconcile() -> Result<(), String> {
    Ok(())
}

#[cfg(not(windows))]
pub fn open_file(_path: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(windows)]
mod win {
    use super::{legacy_shortcut_path, quote_executable};

    const VALUE_NAME: &str = "df-hud";
    use std::path::{Path, PathBuf};
    use std::ptr;
    use windows_sys::Win32::Foundation::{ERROR_FILE_NOT_FOUND, ERROR_MORE_DATA, ERROR_SUCCESS};
    use windows_sys::Win32::System::Registry::{
        HKEY, HKEY_CURRENT_USER, KEY_QUERY_VALUE, KEY_SET_VALUE, REG_SZ, RegCloseKey,
        RegCreateKeyExW, RegDeleteValueW, RegOpenKeyExW, RegQueryValueExW, RegSetValueExW,
    };
    use windows_sys::Win32::UI::Shell::ShellExecuteW;
    use windows_sys::Win32::UI::WindowsAndMessaging::SW_SHOWNORMAL;

    const RUN_KEY: &str = r"Software\Microsoft\Windows\CurrentVersion\Run";

    fn wide(s: &str) -> Vec<u16> {
        s.encode_utf16().chain(std::iter::once(0)).collect()
    }

    fn win_err(op: &str, code: u32) -> String {
        format!("{op}: Win32 error {code}")
    }

    fn current_executable() -> Result<PathBuf, String> {
        let exe = std::env::current_exe().map_err(|err| err.to_string())?;
        std::path::absolute(exe).map_err(|err| err.to_string())
    }

    fn run_key_has_value() -> Result<bool, String> {
        let subkey = wide(RUN_KEY);
        let name = wide(VALUE_NAME);
        let mut key: HKEY = ptr::null_mut();
        let status = unsafe {
            RegOpenKeyExW(
                HKEY_CURRENT_USER,
                subkey.as_ptr(),
                0,
                KEY_QUERY_VALUE,
                &mut key,
            )
        };
        if status == ERROR_FILE_NOT_FOUND {
            return Ok(false);
        }
        if status != ERROR_SUCCESS {
            return Err(win_err("RegOpenKeyExW", status));
        }
        let mut ty = 0u32;
        let mut size = 0u32;
        let query = unsafe {
            RegQueryValueExW(
                key,
                name.as_ptr(),
                ptr::null(),
                &mut ty,
                ptr::null_mut(),
                &mut size,
            )
        };
        unsafe {
            RegCloseKey(key);
        }
        match query {
            ERROR_SUCCESS | ERROR_MORE_DATA => Ok(true),
            ERROR_FILE_NOT_FOUND => Ok(false),
            other => Err(win_err("RegQueryValueExW", other)),
        }
    }

    fn delete_run_value() -> Result<(), String> {
        let subkey = wide(RUN_KEY);
        let name = wide(VALUE_NAME);
        let mut key: HKEY = ptr::null_mut();
        let status = unsafe {
            RegOpenKeyExW(
                HKEY_CURRENT_USER,
                subkey.as_ptr(),
                0,
                KEY_SET_VALUE,
                &mut key,
            )
        };
        if status == ERROR_FILE_NOT_FOUND {
            return Ok(());
        }
        if status != ERROR_SUCCESS {
            return Err(win_err("RegOpenKeyExW", status));
        }
        let delete = unsafe { RegDeleteValueW(key, name.as_ptr()) };
        unsafe {
            RegCloseKey(key);
        }
        if delete == ERROR_SUCCESS || delete == ERROR_FILE_NOT_FOUND {
            Ok(())
        } else {
            Err(win_err("RegDeleteValueW", delete))
        }
    }

    fn write_run_value(exe: &Path) -> Result<(), String> {
        let subkey = wide(RUN_KEY);
        let name = wide(VALUE_NAME);
        let value = wide(&quote_executable(&exe.to_string_lossy()));
        let mut key: HKEY = ptr::null_mut();
        let mut disposition = 0u32;
        let status = unsafe {
            RegCreateKeyExW(
                HKEY_CURRENT_USER,
                subkey.as_ptr(),
                0,
                ptr::null(),
                0,
                KEY_SET_VALUE,
                ptr::null(),
                &mut key,
                &mut disposition,
            )
        };
        if status != ERROR_SUCCESS {
            return Err(win_err("RegCreateKeyExW", status));
        }
        let bytes = (value.len() * 2) as u32;
        let set =
            unsafe { RegSetValueExW(key, name.as_ptr(), 0, REG_SZ, value.as_ptr().cast(), bytes) };
        unsafe {
            RegCloseKey(key);
        }
        if set == ERROR_SUCCESS {
            Ok(())
        } else {
            Err(win_err("RegSetValueExW", set))
        }
    }

    fn remove_legacy_shortcut() -> Result<(), String> {
        match std::fs::remove_file(legacy_shortcut_path()) {
            Ok(()) => Ok(()),
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(err) => Err(err.to_string()),
        }
    }

    pub fn enabled() -> Result<bool, String> {
        if run_key_has_value()? {
            return Ok(true);
        }
        match std::fs::metadata(legacy_shortcut_path()) {
            Ok(_) => Ok(true),
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(false),
            Err(err) => Err(err.to_string()),
        }
    }

    pub fn set_enabled(on: bool) -> Result<(), String> {
        if !on {
            delete_run_value()?;
            return remove_legacy_shortcut();
        }
        write_run_value(&current_executable()?)?;
        remove_legacy_shortcut()
    }

    /// Refresh an existing Run entry to this executable, and drop the old
    /// Startup-folder shortcut so a newly extracted release is what logs in.
    pub fn reconcile() -> Result<(), String> {
        if enabled()? {
            set_enabled(true)
        } else {
            Ok(())
        }
    }

    pub fn open_file(path: &Path) -> Result<(), String> {
        let file = wide(&path.to_string_lossy());
        let open = wide("open");
        let result = unsafe {
            ShellExecuteW(
                ptr::null_mut(),
                open.as_ptr(),
                file.as_ptr(),
                ptr::null(),
                ptr::null(),
                SW_SHOWNORMAL,
            )
        };
        if (result as isize) <= 32 {
            Err(format!(
                "ShellExecuteW({}) failed with code {}",
                path.display(),
                result as isize
            ))
        } else {
            Ok(())
        }
    }
}

#[cfg(windows)]
pub use win::{enabled, open_file, reconcile, set_enabled};

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::Path;

    #[test]
    fn quote_preserves_a_windows_path() {
        let path = r"C:\Program Files\df-hud\df-hud.exe";
        assert_eq!(quote_executable(path), format!("\"{path}\""));
    }

    #[test]
    fn legacy_shortcut_uses_appdata() {
        let got = legacy_shortcut_under(r"C:\Users\tester\AppData\Roaming");
        assert_eq!(
            got,
            Path::new(r"C:\Users\tester\AppData\Roaming")
                .join("Microsoft")
                .join("Windows")
                .join("Start Menu")
                .join("Programs")
                .join("Startup")
                .join("df-hud.lnk")
        );
    }

    #[test]
    fn linux_stubs_are_inert() {
        if cfg!(windows) {
            return;
        }
        assert!(!available());
        assert!(!enabled().unwrap());
        set_enabled(true).unwrap();
        reconcile().unwrap();
        open_file(Path::new("config.toml")).unwrap();
    }
}
