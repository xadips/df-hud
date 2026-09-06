//! Bootstrap WGL 3.3 core with an 8-bit alpha pixel format.
//!
//! `wglCreateContext` + `ChoosePixelFormat` cannot request a core profile or
//! guarantee alpha. The dummy-context dance loads `wglChoosePixelFormatARB` and
//! `wglCreateContextAttribsARB` once per process. Dummy HWND uses
//! [`crate::overlay::win32::DUMMY_CLASS`], not the overlay class — `WM_DESTROY`
//! on a shared class ended the WGL spike.

use std::error::Error;
use std::ffi::{CString, c_void};
use std::mem::size_of;
use std::ptr;
use std::sync::OnceLock;

use libloading::Library;
use windows_sys::Win32::Foundation::HWND;
use windows_sys::Win32::Graphics::Gdi::{GetDC, HDC, ReleaseDC};
use windows_sys::Win32::Graphics::OpenGL::{
    ChoosePixelFormat, DescribePixelFormat, GetPixelFormat, HGLRC, PFD_DOUBLEBUFFER,
    PFD_DRAW_TO_WINDOW, PFD_SUPPORT_COMPOSITION, PFD_SUPPORT_OPENGL, PFD_TYPE_RGBA,
    PIXELFORMATDESCRIPTOR, SetPixelFormat, SwapBuffers, wglCreateContext, wglDeleteContext,
    wglGetProcAddress, wglMakeCurrent,
};
use windows_sys::Win32::UI::WindowsAndMessaging::{
    CreateWindowExW, DestroyWindow, WS_OVERLAPPEDWINDOW,
};

use crate::overlay::win32::{DUMMY_CLASS, last_err, wide};

const WGL_DRAW_TO_WINDOW_ARB: i32 = 0x2001;
const WGL_ACCELERATION_ARB: i32 = 0x2003;
const WGL_SUPPORT_OPENGL_ARB: i32 = 0x2010;
const WGL_DOUBLE_BUFFER_ARB: i32 = 0x2011;
const WGL_PIXEL_TYPE_ARB: i32 = 0x2013;
const WGL_COLOR_BITS_ARB: i32 = 0x2014;
const WGL_ALPHA_BITS_ARB: i32 = 0x201B;
const WGL_DEPTH_BITS_ARB: i32 = 0x2022;
const WGL_STENCIL_BITS_ARB: i32 = 0x2023;
const WGL_FULL_ACCELERATION_ARB: i32 = 0x2027;
const WGL_TYPE_RGBA_ARB: i32 = 0x202B;
const WGL_SAMPLE_BUFFERS_ARB: i32 = 0x2041;
const WGL_CONTEXT_MAJOR_VERSION_ARB: i32 = 0x2091;
const WGL_CONTEXT_MINOR_VERSION_ARB: i32 = 0x2092;
const WGL_CONTEXT_PROFILE_MASK_ARB: i32 = 0x9126;
const WGL_CONTEXT_CORE_PROFILE_BIT_ARB: i32 = 0x0000_0001;

type ChoosePixelFormatArb =
    unsafe extern "system" fn(HDC, *const i32, *const f32, u32, *mut i32, *mut u32) -> i32;
type CreateContextAttribsArb = unsafe extern "system" fn(HDC, HGLRC, *const i32) -> HGLRC;
type SwapIntervalExt = unsafe extern "system" fn(i32) -> i32;
type GetPixelFormatAttribivArb =
    unsafe extern "system" fn(HDC, i32, i32, u32, *const i32, *mut i32) -> i32;

/// A WGL context on the overlay HWND. The caller (`win32::Surface`) keeps that
/// window alive for as long as this exists and drops the `Gpu` first.
pub struct GlSurface {
    hwnd: HWND,
    hdc: HDC,
    rc: HGLRC,
    _opengl32: Library,
}

impl GlSurface {
    pub fn create(
        instance: windows_sys::Win32::Foundation::HINSTANCE,
        hwnd: HWND,
    ) -> Result<Self, Box<dyn Error>> {
        // SAFETY: `hwnd` is the caller's live overlay window (CS_OWNDC, so the
        // DC is the window's own and stays valid until ReleaseDC in `Drop`).
        let hdc = unsafe { GetDC(hwnd) };
        if hdc.is_null() {
            return Err(last_err("GetDC"));
        }

        let procs = wgl_procs(instance)?;
        // SAFETY: opengl32.dll is a system library already mapped into this
        // process by windows-sys's imports; loading it again runs no new code.
        let opengl32 = unsafe { Library::new("opengl32.dll") }
            .map_err(|err| format!("load opengl32.dll: {err}"))?;
        let (choose_fmt, create_ctx, get_attr, swap_interval_fn) = (
            procs.choose_fmt,
            procs.create_ctx,
            procs.get_attr,
            procs.swap_interval,
        );

        // SAFETY: `hdc` is the live DC obtained above.
        let existing = unsafe { GetPixelFormat(hdc) };
        let format = if existing != 0 {
            existing
        } else {
            let attribs = [
                WGL_DRAW_TO_WINDOW_ARB,
                1,
                WGL_SUPPORT_OPENGL_ARB,
                1,
                WGL_DOUBLE_BUFFER_ARB,
                1,
                WGL_PIXEL_TYPE_ARB,
                WGL_TYPE_RGBA_ARB,
                WGL_COLOR_BITS_ARB,
                32,
                WGL_ALPHA_BITS_ARB,
                8,
                WGL_ACCELERATION_ARB,
                WGL_FULL_ACCELERATION_ARB,
                WGL_DEPTH_BITS_ARB,
                0,
                WGL_STENCIL_BITS_ARB,
                0,
                WGL_SAMPLE_BUFFERS_ARB,
                0,
                0,
            ];
            let mut format = 0i32;
            let mut count = 0u32;
            // SAFETY: `choose_fmt` is the ICD's wglChoosePixelFormatARB with the
            // signature the ARB spec gives it; `attribs` is 0-terminated, the
            // float list is null (none), and `format`/`count` are live
            // out-params sized for the one format requested.
            let ok = unsafe {
                choose_fmt(
                    hdc,
                    attribs.as_ptr(),
                    ptr::null(),
                    1,
                    &mut format,
                    &mut count,
                )
            };
            if ok == 0 || count == 0 || format == 0 {
                // SAFETY: releasing the DC obtained above from the same `hwnd`, once.
                unsafe { ReleaseDC(hwnd, hdc) };
                return Err(
                    "wglChoosePixelFormatARB found no 32-bit RGBA + 8-bit alpha format".into(),
                );
            }

            let mut pfd = PIXELFORMATDESCRIPTOR {
                nSize: size_of::<PIXELFORMATDESCRIPTOR>() as u16,
                nVersion: 1,
                ..Default::default()
            };
            // SAFETY: `hdc` is live; `pfd` is a full descriptor and `nSize` says so.
            let set = unsafe {
                DescribePixelFormat(hdc, format, pfd.nSize as u32, &mut pfd);
                SetPixelFormat(hdc, format, &pfd)
            };
            if set == 0 {
                // SAFETY: releasing the DC obtained above from the same `hwnd`, once.
                unsafe { ReleaseDC(hwnd, hdc) };
                return Err(last_err("SetPixelFormat"));
            }
            format
        };

        let mut pfd = PIXELFORMATDESCRIPTOR {
            nSize: size_of::<PIXELFORMATDESCRIPTOR>() as u16,
            nVersion: 1,
            ..Default::default()
        };
        let mut alpha = 0i32;
        let alpha_attr = WGL_ALPHA_BITS_ARB;
        // SAFETY: `hdc` is live and `format` is set on it; `pfd` is a full
        // descriptor; `get_attr` queries the one attribute in `alpha_attr`
        // into the one int `alpha` (count 1).
        unsafe {
            DescribePixelFormat(hdc, format, pfd.nSize as u32, &mut pfd);
            get_attr(hdc, format, 0, 1, &alpha_attr, &mut alpha);
        }
        debug!(
            "pixel format {format}  color {}  alpha {alpha}  flags=0x{:x}",
            pfd.cColorBits, pfd.dwFlags
        );
        if alpha < 8 {
            warn!("WARNING: alpha bits {alpha} < 8 — DWM may composite this as opaque (the hole)");
        }

        let ctx_attribs = [
            WGL_CONTEXT_MAJOR_VERSION_ARB,
            3,
            WGL_CONTEXT_MINOR_VERSION_ARB,
            3,
            WGL_CONTEXT_PROFILE_MASK_ARB,
            WGL_CONTEXT_CORE_PROFILE_BIT_ARB,
            0,
        ];
        // SAFETY: `create_ctx` is the ICD's wglCreateContextAttribsARB; `hdc`
        // has its pixel format set, no share context (null), 0-terminated attribs.
        let rc = unsafe { create_ctx(hdc, ptr::null_mut(), ctx_attribs.as_ptr()) };
        if rc.is_null() {
            // SAFETY: releasing the DC obtained above from the same `hwnd`, once.
            unsafe { ReleaseDC(hwnd, hdc) };
            return Err(last_err("wglCreateContextAttribsARB (GL 3.3 core)"));
        }
        // SAFETY: `rc` was created on `hdc` just above; on failure both are
        // released exactly once and never used again.
        if unsafe { wglMakeCurrent(hdc, rc) } == 0 {
            // SAFETY: see above.
            unsafe {
                wglDeleteContext(rc);
                ReleaseDC(hwnd, hdc);
            }
            return Err(last_err("wglMakeCurrent"));
        }

        if let Some(set_interval) = swap_interval_fn {
            // SAFETY: wglSwapIntervalEXT acts on the context current on this
            // thread, which `rc` now is.
            unsafe { set_interval(0) };
        } else {
            warn!("warning: wglSwapIntervalEXT missing; hitch test is inconclusive");
        }

        Ok(Self {
            hwnd,
            hdc,
            rc,
            _opengl32: opengl32,
        })
    }

    pub fn make_current(&self) -> Result<(), Box<dyn Error>> {
        // SAFETY: `hdc`/`rc` are owned by `self` and live until `Drop`.
        if unsafe { wglMakeCurrent(self.hdc, self.rc) } == 0 {
            return Err(last_err("wglMakeCurrent"));
        }
        Ok(())
    }

    pub fn swap(&self) -> Result<(), Box<dyn Error>> {
        // SAFETY: `hdc` is owned by `self` and live until `Drop`.
        if unsafe { SwapBuffers(self.hdc) } == 0 {
            return Err(last_err("SwapBuffers"));
        }
        Ok(())
    }

    pub fn load_proc(&self, name: &str) -> *const c_void {
        let Ok(c) = CString::new(name) else {
            return ptr::null();
        };
        // SAFETY: `c` is NUL-terminated and alive for both lookups; `rc` is
        // current on this thread (glow loads right after `create`), which is
        // what makes wglGetProcAddress's answers valid for it; the opengl32
        // fallback is kept mapped by `_opengl32`. Only the address is taken,
        // so the placeholder `fn()` type is never called as such.
        unsafe {
            let p = wglGetProcAddress(c.as_ptr().cast());
            if let Some(f) = p
                && real_proc(f as usize)
            {
                return f as *const c_void;
            }
            match self
                ._opengl32
                .get::<unsafe extern "system" fn()>(c.as_bytes_with_nul())
            {
                Ok(sym) => *sym as *const c_void,
                Err(_) => ptr::null(),
            }
        }
    }
}

impl Drop for GlSurface {
    fn drop(&mut self) {
        // SAFETY: `rc` and `hdc` are the handles `create` obtained for `hwnd`,
        // released here exactly once; the owner dropped the `Gpu` before this
        // and keeps `hwnd` alive until after.
        unsafe {
            wglMakeCurrent(ptr::null_mut(), ptr::null_mut());
            if !self.rc.is_null() {
                wglDeleteContext(self.rc);
            }
            if !self.hdc.is_null() {
                ReleaseDC(self.hwnd, self.hdc);
            }
        }
    }
}

struct WglProcs {
    choose_fmt: ChoosePixelFormatArb,
    create_ctx: CreateContextAttribsArb,
    get_attr: GetPixelFormatAttribivArb,
    swap_interval: Option<SwapIntervalExt>,
}

fn wgl_procs(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
) -> Result<&'static WglProcs, Box<dyn Error>> {
    static PROCS: OnceLock<WglProcs> = OnceLock::new();
    if let Some(procs) = PROCS.get() {
        return Ok(procs);
    }
    let loaded = load_wgl_extensions(instance)?;
    Ok(PROCS.get_or_init(|| loaded))
}

fn load_wgl_extensions(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
) -> Result<WglProcs, Box<dyn Error>> {
    let class = wide(DUMMY_CLASS);
    let title = wide("df-hud-wgl-dummy");
    // SAFETY: `class`/`title` are NUL-terminated u16 buffers that outlive the
    // call; DUMMY_CLASS was registered for `instance` by `register_classes`.
    let hwnd = unsafe {
        CreateWindowExW(
            0,
            class.as_ptr(),
            title.as_ptr(),
            WS_OVERLAPPEDWINDOW,
            0,
            0,
            16,
            16,
            ptr::null_mut(),
            ptr::null_mut(),
            instance,
            ptr::null(),
        )
    };
    if hwnd.is_null() {
        return Err(last_err("CreateWindowExW dummy"));
    }

    // SAFETY: `hwnd` was just created and is destroyed only below.
    let hdc = unsafe { GetDC(hwnd) };
    if hdc.is_null() {
        // SAFETY: destroying the window created above, once.
        unsafe { DestroyWindow(hwnd) };
        return Err(last_err("GetDC dummy"));
    }

    let pfd = PIXELFORMATDESCRIPTOR {
        nSize: size_of::<PIXELFORMATDESCRIPTOR>() as u16,
        nVersion: 1,
        dwFlags: PFD_DRAW_TO_WINDOW
            | PFD_SUPPORT_OPENGL
            | PFD_DOUBLEBUFFER
            | PFD_SUPPORT_COMPOSITION,
        iPixelType: PFD_TYPE_RGBA,
        cColorBits: 32,
        cAlphaBits: 8,
        iLayerType: 0, // PFD_MAIN_PLANE
        ..Default::default()
    };
    // SAFETY: `hdc` is the live dummy DC and `pfd` a full descriptor.
    let format_set = unsafe {
        let format = ChoosePixelFormat(hdc, &pfd);
        format != 0 && SetPixelFormat(hdc, format, &pfd) != 0
    };
    if !format_set {
        // SAFETY: the dummy DC and window are released once, in that order.
        unsafe {
            ReleaseDC(hwnd, hdc);
            DestroyWindow(hwnd);
        }
        return Err(last_err("dummy SetPixelFormat"));
    }
    // SAFETY: `hdc` has its pixel format set; `rc` is only made current when non-null.
    let rc = unsafe {
        let rc = wglCreateContext(hdc);
        if !rc.is_null() && wglMakeCurrent(hdc, rc) == 0 {
            wglDeleteContext(rc);
            ptr::null_mut()
        } else {
            rc
        }
    };
    if rc.is_null() {
        // SAFETY: the dummy DC and window are released once, in that order.
        unsafe {
            ReleaseDC(hwnd, hdc);
            DestroyWindow(hwnd);
        }
        return Err(last_err("dummy wglCreateContext"));
    }

    let choose = load_wgl_symbol::<ChoosePixelFormatArb>("wglChoosePixelFormatARB")
        .ok_or("wglChoosePixelFormatARB missing — cannot request an alpha pixel format")?;
    let create = load_wgl_symbol::<CreateContextAttribsArb>("wglCreateContextAttribsARB")
        .ok_or("wglCreateContextAttribsARB missing — cannot create a GL 3.3 core context")?;
    let get_attr = load_wgl_symbol::<GetPixelFormatAttribivArb>("wglGetPixelFormatAttribivARB")
        .ok_or("wglGetPixelFormatAttribivARB missing")?;
    let swap = load_wgl_symbol::<SwapIntervalExt>("wglSwapIntervalEXT");

    // SAFETY: the dummy context, DC and window are torn down once, in
    // reverse creation order; the extension pointers loaded above are
    // process-wide for this ICD, so they outlive the dummy context.
    unsafe {
        wglMakeCurrent(ptr::null_mut(), ptr::null_mut());
        wglDeleteContext(rc);
        ReleaseDC(hwnd, hdc);
        DestroyWindow(hwnd);
    }

    Ok(WglProcs {
        choose_fmt: choose,
        create_ctx: create,
        get_attr,
        swap_interval: swap,
    })
}

/// `T` must be the `unsafe extern "system" fn` type matching `name`'s WGL
/// extension signature; it is only called through that type.
fn load_wgl_symbol<T>(name: &str) -> Option<T> {
    let c = CString::new(name).ok()?;
    // SAFETY: `c` is NUL-terminated and alive for the call; a dummy context
    // is current on this thread (the caller's contract), so the address is the
    // ICD's real entry point once the documented failure sentinels (null,
    // 1..=3, -1) are excluded. Every `T` used is a fn pointer, the same size
    // as PROC, so `transmute_copy` reinterprets exactly the pointer.
    unsafe {
        let p = wglGetProcAddress(c.as_ptr().cast())?;
        if !real_proc(p as usize) {
            return None;
        }
        Some(std::mem::transmute_copy(&p))
    }
}

/// wglGetProcAddress signals failure with null (already an `Option::None`),
/// 1, 2, 3 or -1, depending on the ICD.
fn real_proc(addr: usize) -> bool {
    addr > 3 && addr != usize::MAX
}
