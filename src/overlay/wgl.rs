//! Bootstrap WGL 3.3 core with an 8-bit alpha pixel format.
//!
//! `wglCreateContext` + `ChoosePixelFormat` cannot request a core profile or
//! guarantee alpha. The dummy-context dance loads `wglChoosePixelFormatARB` and
//! `wglCreateContextAttribsARB` once per monitor. Dummy HWND uses
//! [`crate::overlay::win32::DUMMY_CLASS`], not the overlay class — `WM_DESTROY`
//! on a shared class ended the WGL spike.
//!
//! `opengl32` picks the ICD for a window by its monitor and styles, and
//! `wglMakeCurrent` refuses a context from one ICD on a DC of another, so the
//! dummy copies the overlay's ex-style and sits on its monitor.

use std::error::Error;
use std::ffi::{CString, c_void};
use std::mem::size_of;
use std::ptr;
use std::sync::Mutex;

use libloading::Library;
use windows_sys::Win32::Foundation::{HWND, RECT, SetLastError};
use windows_sys::Win32::Graphics::Gdi::{
    GetDC, HDC, MONITOR_DEFAULTTONEAREST, MonitorFromWindow, ReleaseDC,
};
use windows_sys::Win32::Graphics::OpenGL::{
    ChoosePixelFormat, DescribePixelFormat, GetPixelFormat, HGLRC, PFD_DOUBLEBUFFER,
    PFD_DRAW_TO_WINDOW, PFD_SUPPORT_COMPOSITION, PFD_SUPPORT_OPENGL, PFD_TYPE_RGBA,
    PIXELFORMATDESCRIPTOR, SetPixelFormat, SwapBuffers, wglCreateContext, wglDeleteContext,
    wglGetProcAddress, wglMakeCurrent,
};
use windows_sys::Win32::UI::WindowsAndMessaging::{
    CreateWindowExW, DestroyWindow, GWL_EXSTYLE, GetWindowLongPtrW, GetWindowRect, LWA_ALPHA,
    SetLayeredWindowAttributes, WS_EX_LAYERED, WS_OVERLAPPEDWINDOW, WS_POPUP,
};

use crate::overlay::win32::{DUMMY_CLASS, last_err, wide};
use crate::wake::lock;

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

        let procs = wgl_procs(instance, hwnd)?;
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
            warn!("alpha bits {alpha} < 8 — DWM may composite this as opaque (the hole)");
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
        if unsafe { make_current(hdc, rc) } == 0 {
            let err = last_err("wglMakeCurrent (new context)");
            // SAFETY: see above.
            unsafe {
                wglDeleteContext(rc);
                ReleaseDC(hwnd, hdc);
            }
            return Err(err);
        }

        if let Some(set_interval) = swap_interval_fn {
            // SAFETY: wglSwapIntervalEXT acts on the context current on this
            // thread, which `rc` now is.
            unsafe { set_interval(0) };
        } else {
            warn!("wglSwapIntervalEXT missing; hitch test is inconclusive");
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
        if unsafe { make_current(self.hdc, self.rc) } == 0 {
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
            // Intel fails a release with a null DC, and leaves error 6 behind.
            wglMakeCurrent(self.hdc, ptr::null_mut());
            if !self.rc.is_null() {
                wglDeleteContext(self.rc);
            }
            if !self.hdc.is_null() {
                ReleaseDC(self.hwnd, self.hdc);
            }
        }
    }
}

/// A driver may fail `wglMakeCurrent` without setting the last error; clearing
/// it first keeps a stale code from an earlier call out of the report.
///
/// # Safety
/// Same contract as `wglMakeCurrent`.
unsafe fn make_current(hdc: HDC, rc: HGLRC) -> i32 {
    // SAFETY: the caller upholds `wglMakeCurrent`'s contract; clearing this
    // thread's last-error slot takes no pointers.
    unsafe {
        SetLastError(0);
        wglMakeCurrent(hdc, rc)
    }
}

#[derive(Clone, Copy)]
struct WglProcs {
    choose_fmt: ChoosePixelFormatArb,
    create_ctx: CreateContextAttribsArb,
    get_attr: GetPixelFormatAttribivArb,
    swap_interval: Option<SwapIntervalExt>,
}

/// The extension pointers for `overlay`'s monitor, loaded through a dummy
/// shaped like `overlay`, or through a plain one if that dummy fails.
fn wgl_procs(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
    overlay: HWND,
) -> Result<WglProcs, Box<dyn Error>> {
    static PROCS: Mutex<Option<(usize, WglProcs)>> = Mutex::new(None);
    // SAFETY: `overlay` is the caller's live window; the flag is a constant.
    let monitor = unsafe { MonitorFromWindow(overlay, MONITOR_DEFAULTTONEAREST) } as usize;
    let mut cached = lock(&PROCS);
    if let Some((on, procs)) = *cached
        && on == monitor
    {
        return Ok(procs);
    }
    let procs = match load_wgl_extensions(instance, DummyShape::like(overlay)) {
        Ok(procs) => procs,
        Err(like) => {
            let procs = load_wgl_extensions(instance, DummyShape::PLAIN)
                .map_err(|plain| format!("{like}; with a plain dummy: {plain}"))?;
            info!(
                "WGL: a dummy shaped like the overlay failed ({like}); loaded through a plain one"
            );
            procs
        }
    };
    *cached = Some((monitor, procs));
    Ok(procs)
}

#[derive(Clone, Copy)]
struct DummyShape {
    ex_style: u32,
    style: u32,
    x: i32,
    y: i32,
}

impl DummyShape {
    const PLAIN: Self = Self {
        ex_style: 0,
        style: WS_OVERLAPPEDWINDOW,
        x: 0,
        y: 0,
    };

    fn like(overlay: HWND) -> Self {
        let mut rect = RECT::default();
        // SAFETY: `overlay` is the caller's live window; `rect` is a live
        // out-param, left at 0,0 if the call fails.
        let ex_style = unsafe {
            GetWindowRect(overlay, &mut rect);
            GetWindowLongPtrW(overlay, GWL_EXSTYLE) as u32
        };
        Self {
            ex_style,
            style: WS_POPUP,
            x: rect.left,
            y: rect.top,
        }
    }
}

/// The dummy window, DC and context, torn down in reverse on drop.
struct Dummy {
    hwnd: HWND,
    hdc: HDC,
    rc: HGLRC,
}

impl Drop for Dummy {
    fn drop(&mut self) {
        // SAFETY: each handle was created by `load_wgl_extensions` for this
        // dummy and is released once, context before DC before window.
        unsafe {
            if !self.rc.is_null() {
                wglMakeCurrent(self.hdc, ptr::null_mut());
                wglDeleteContext(self.rc);
            }
            if !self.hdc.is_null() {
                ReleaseDC(self.hwnd, self.hdc);
            }
            DestroyWindow(self.hwnd);
        }
    }
}

fn load_wgl_extensions(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
    shape: DummyShape,
) -> Result<WglProcs, Box<dyn Error>> {
    let class = wide(DUMMY_CLASS);
    let title = wide("df-hud-wgl-dummy");
    // SAFETY: `class`/`title` are NUL-terminated u16 buffers that outlive the
    // call; DUMMY_CLASS was registered for `instance` by `register_classes`.
    // The window is never shown.
    let hwnd = unsafe {
        CreateWindowExW(
            shape.ex_style,
            class.as_ptr(),
            title.as_ptr(),
            shape.style,
            shape.x,
            shape.y,
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
    let mut dummy = Dummy {
        hwnd,
        hdc: ptr::null_mut(),
        rc: ptr::null_mut(),
    };

    // A layered window has no drawing surface until its layered attributes are set.
    if shape.ex_style & WS_EX_LAYERED != 0 {
        // SAFETY: `hwnd` is the live dummy; the rest are a colour key, alpha and flags.
        if unsafe { SetLayeredWindowAttributes(hwnd, 0, 255, LWA_ALPHA) } == 0 {
            return Err(last_err("dummy SetLayeredWindowAttributes"));
        }
    }

    // SAFETY: `hwnd` is the live dummy, destroyed only by `Dummy::drop`.
    dummy.hdc = unsafe { GetDC(hwnd) };
    if dummy.hdc.is_null() {
        return Err(last_err("GetDC dummy"));
    }
    let hdc = dummy.hdc;

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
        return Err(last_err("dummy SetPixelFormat"));
    }
    // SAFETY: `hdc` has its pixel format set.
    dummy.rc = unsafe { wglCreateContext(hdc) };
    if dummy.rc.is_null() {
        return Err(last_err("dummy wglCreateContext"));
    }
    // SAFETY: `rc` was created on `hdc` just above; `Dummy::drop` releases both.
    if unsafe { make_current(hdc, dummy.rc) } == 0 {
        return Err(last_err("dummy wglMakeCurrent"));
    }

    let choose = load_wgl_symbol::<ChoosePixelFormatArb>("wglChoosePixelFormatARB")
        .ok_or("wglChoosePixelFormatARB missing — cannot request an alpha pixel format")?;
    let create = load_wgl_symbol::<CreateContextAttribsArb>("wglCreateContextAttribsARB")
        .ok_or("wglCreateContextAttribsARB missing — cannot create a GL 3.3 core context")?;
    let get_attr = load_wgl_symbol::<GetPixelFormatAttribivArb>("wglGetPixelFormatAttribivARB")
        .ok_or("wglGetPixelFormatAttribivARB missing")?;
    let swap = load_wgl_symbol::<SwapIntervalExt>("wglSwapIntervalEXT");

    // The extension pointers are process-wide for this ICD, so they outlive
    // the dummy context `dummy` tears down here.
    drop(dummy);
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
