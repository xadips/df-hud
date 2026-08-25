//! Bootstrap WGL 3.3 core with an 8-bit alpha pixel format.
//!
//! `wglCreateContext` + `ChoosePixelFormat` cannot request a core profile or
//! guarantee alpha. The dummy-context dance loads `wglChoosePixelFormatARB` and
//! `wglCreateContextAttribsARB`. Dummy HWND uses [`crate::overlay::win32::DUMMY_CLASS`],
//! not the overlay class — `WM_DESTROY` on a shared class ended the WGL spike.

use std::error::Error;
use std::ffi::{CString, c_void};
use std::mem::{size_of, zeroed};
use std::ptr;

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
        let hdc = unsafe { GetDC(hwnd) };
        if hdc.is_null() {
            return Err(last_err("GetDC"));
        }

        let (choose_fmt, create_ctx, get_attr, swap_interval_fn, opengl32) =
            load_wgl_extensions(instance)?;

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
                unsafe { ReleaseDC(hwnd, hdc) };
                return Err(
                    "wglChoosePixelFormatARB found no 32-bit RGBA + 8-bit alpha format".into(),
                );
            }

            let mut pfd: PIXELFORMATDESCRIPTOR = unsafe { zeroed() };
            pfd.nSize = size_of::<PIXELFORMATDESCRIPTOR>() as u16;
            pfd.nVersion = 1;
            unsafe { DescribePixelFormat(hdc, format, pfd.nSize as u32, &mut pfd) };
            if unsafe { SetPixelFormat(hdc, format, &pfd) } == 0 {
                unsafe { ReleaseDC(hwnd, hdc) };
                return Err(last_err("SetPixelFormat"));
            }
            format
        };

        let mut pfd: PIXELFORMATDESCRIPTOR = unsafe { zeroed() };
        pfd.nSize = size_of::<PIXELFORMATDESCRIPTOR>() as u16;
        pfd.nVersion = 1;
        unsafe { DescribePixelFormat(hdc, format, pfd.nSize as u32, &mut pfd) };

        let mut alpha = 0i32;
        let alpha_attr = WGL_ALPHA_BITS_ARB;
        unsafe { get_attr(hdc, format, 0, 1, &alpha_attr, &mut alpha) };
        eprintln!(
            "pixel format {format}  color {}  alpha {alpha}  flags=0x{:x}",
            pfd.cColorBits, pfd.dwFlags
        );
        if alpha < 8 {
            eprintln!(
                "WARNING: alpha bits {alpha} < 8 — DWM may composite this as opaque (the hole)"
            );
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
        let rc = unsafe { create_ctx(hdc, ptr::null_mut(), ctx_attribs.as_ptr()) };
        if rc.is_null() {
            unsafe { ReleaseDC(hwnd, hdc) };
            return Err(last_err("wglCreateContextAttribsARB (GL 3.3 core)"));
        }
        if unsafe { wglMakeCurrent(hdc, rc) } == 0 {
            unsafe {
                wglDeleteContext(rc);
                ReleaseDC(hwnd, hdc);
            }
            return Err(last_err("wglMakeCurrent"));
        }

        if let Some(set_interval) = swap_interval_fn {
            unsafe { set_interval(0) };
        } else {
            eprintln!("warning: wglSwapIntervalEXT missing; hitch test is inconclusive");
        }

        Ok(Self {
            hwnd,
            hdc,
            rc,
            _opengl32: opengl32,
        })
    }

    pub fn make_current(&self) -> Result<(), Box<dyn Error>> {
        if unsafe { wglMakeCurrent(self.hdc, self.rc) } == 0 {
            return Err(last_err("wglMakeCurrent"));
        }
        Ok(())
    }

    pub fn swap(&self) -> Result<(), Box<dyn Error>> {
        if unsafe { SwapBuffers(self.hdc) } == 0 {
            return Err(last_err("SwapBuffers"));
        }
        Ok(())
    }

    pub fn load_proc(&self, name: &str) -> *const c_void {
        let Ok(c) = CString::new(name) else {
            return ptr::null();
        };
        unsafe {
            let p = wglGetProcAddress(c.as_ptr().cast());
            if let Some(f) = p {
                let addr = f as usize;
                if addr > 3 {
                    return f as *const c_void;
                }
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

type WglExtensions = (
    ChoosePixelFormatArb,
    CreateContextAttribsArb,
    GetPixelFormatAttribivArb,
    Option<SwapIntervalExt>,
    Library,
);

fn load_wgl_extensions(
    instance: windows_sys::Win32::Foundation::HINSTANCE,
) -> Result<WglExtensions, Box<dyn Error>> {
    let class = wide(DUMMY_CLASS);
    let title = wide("df-hud-wgl-dummy");
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

    let hdc = unsafe { GetDC(hwnd) };
    if hdc.is_null() {
        unsafe { DestroyWindow(hwnd) };
        return Err(last_err("GetDC dummy"));
    }

    let mut pfd: PIXELFORMATDESCRIPTOR = unsafe { zeroed() };
    pfd.nSize = size_of::<PIXELFORMATDESCRIPTOR>() as u16;
    pfd.nVersion = 1;
    pfd.dwFlags =
        PFD_DRAW_TO_WINDOW | PFD_SUPPORT_OPENGL | PFD_DOUBLEBUFFER | PFD_SUPPORT_COMPOSITION;
    pfd.iPixelType = PFD_TYPE_RGBA;
    pfd.cColorBits = 32;
    pfd.cAlphaBits = 8;
    pfd.iLayerType = 0; // PFD_MAIN_PLANE
    let format = unsafe { ChoosePixelFormat(hdc, &pfd) };
    if format == 0 || unsafe { SetPixelFormat(hdc, format, &pfd) } == 0 {
        unsafe {
            ReleaseDC(hwnd, hdc);
            DestroyWindow(hwnd);
        }
        return Err(last_err("dummy SetPixelFormat"));
    }
    let rc = unsafe { wglCreateContext(hdc) };
    if rc.is_null() || unsafe { wglMakeCurrent(hdc, rc) } == 0 {
        unsafe {
            if !rc.is_null() {
                wglDeleteContext(rc);
            }
            ReleaseDC(hwnd, hdc);
            DestroyWindow(hwnd);
        }
        return Err(last_err("dummy wglCreateContext"));
    }

    let opengl32 = unsafe { Library::new("opengl32.dll") }
        .map_err(|err| format!("load opengl32.dll: {err}"))?;

    let choose = load_wgl_symbol::<ChoosePixelFormatArb>("wglChoosePixelFormatARB")
        .ok_or("wglChoosePixelFormatARB missing — cannot request an alpha pixel format")?;
    let create = load_wgl_symbol::<CreateContextAttribsArb>("wglCreateContextAttribsARB")
        .ok_or("wglCreateContextAttribsARB missing — cannot create a GL 3.3 core context")?;
    let get_attr = load_wgl_symbol::<GetPixelFormatAttribivArb>("wglGetPixelFormatAttribivARB")
        .ok_or("wglGetPixelFormatAttribivARB missing")?;
    let swap = load_wgl_symbol::<SwapIntervalExt>("wglSwapIntervalEXT");

    unsafe {
        wglMakeCurrent(ptr::null_mut(), ptr::null_mut());
        wglDeleteContext(rc);
        ReleaseDC(hwnd, hdc);
        DestroyWindow(hwnd);
    }

    Ok((choose, create, get_attr, swap, opengl32))
}

fn load_wgl_symbol<T>(name: &str) -> Option<T> {
    let c = CString::new(name).ok()?;
    unsafe {
        let p = wglGetProcAddress(c.as_ptr().cast())?;
        let addr = p as usize;
        if addr <= 3 {
            return None;
        }
        Some(std::mem::transmute_copy(&p))
    }
}
