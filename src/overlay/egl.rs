//! Runtime-loaded EGL 1.5 / GLES 3.0. No compile-time link to libEGL.

use std::error::Error;
use std::ffi::{CStr, c_char, c_void};
use std::ptr;

use libloading::{Library, Symbol};

pub type Display = *mut c_void;
pub type Config = *mut c_void;
pub type Surface = *mut c_void;
pub type Context = *mut c_void;
pub type NativeDisplay = *mut c_void;
pub type NativeWindow = *mut c_void;
pub type Int = i32;
pub type Enum = u32;
pub type Attrib = isize;

pub const NONE: Int = 0x3038;
pub const ATTRIB_NONE: Attrib = 0x3038;
pub const SURFACE_TYPE: Int = 0x3033;
pub const WINDOW_BIT: Int = 0x0004;
#[cfg(test)]
pub const PBUFFER_BIT: Int = 0x0001;
#[cfg(test)]
pub const WIDTH: Int = 0x3057;
#[cfg(test)]
pub const HEIGHT: Int = 0x3056;
#[cfg(test)]
pub const PLATFORM_SURFACELESS_MESA: Enum = 0x31DD;
pub const RENDERABLE_TYPE: Int = 0x3040;
pub const OPENGL_ES3_BIT: Int = 0x0000_0040;
pub const RED_SIZE: Int = 0x3024;
pub const GREEN_SIZE: Int = 0x3023;
pub const BLUE_SIZE: Int = 0x3022;
pub const ALPHA_SIZE: Int = 0x3021;
pub const OPENGL_ES_API: Enum = 0x30A0;
pub const CONTEXT_MAJOR_VERSION: Int = 0x3098;
pub const CONTEXT_MINOR_VERSION: Int = 0x30FB;
pub const PLATFORM_WAYLAND_KHR: Enum = 0x31D8;
pub const VENDOR: Int = 0x3053;
pub const VERSION: Int = 0x3054;

type GetProcAddress = unsafe extern "C" fn(*const c_char) -> *mut c_void;
type GetPlatformDisplay = unsafe extern "C" fn(Enum, NativeDisplay, *const Attrib) -> Display;
type GetDisplay = unsafe extern "C" fn(NativeDisplay) -> Display;
type Initialize = unsafe extern "C" fn(Display, *mut Int, *mut Int) -> Int;
type BindApi = unsafe extern "C" fn(Enum) -> Int;
type ChooseConfig = unsafe extern "C" fn(Display, *const Int, *mut Config, Int, *mut Int) -> Int;
type CreateContext = unsafe extern "C" fn(Display, Config, Context, *const Int) -> Context;
type CreateWindowSurface =
    unsafe extern "C" fn(Display, Config, NativeWindow, *const Int) -> Surface;
#[cfg(test)]
type CreatePbufferSurface = unsafe extern "C" fn(Display, Config, *const Int) -> Surface;
type MakeCurrent = unsafe extern "C" fn(Display, Surface, Surface, Context) -> Int;
type SwapInterval = unsafe extern "C" fn(Display, Int) -> Int;
type SwapBuffers = unsafe extern "C" fn(Display, Surface) -> Int;
type DestroySurface = unsafe extern "C" fn(Display, Surface) -> Int;
type DestroyContext = unsafe extern "C" fn(Display, Context) -> Int;
type Terminate = unsafe extern "C" fn(Display) -> Int;
type QueryString = unsafe extern "C" fn(Display, Int) -> *const c_char;

pub struct Egl {
    _lib: Library,
    gles: Option<Library>,
    p_get_proc_address: GetProcAddress,
    p_get_platform_display: Option<GetPlatformDisplay>,
    p_get_display: GetDisplay,
    p_initialize: Initialize,
    p_bind_api: BindApi,
    p_choose_config: ChooseConfig,
    p_create_context: CreateContext,
    p_create_window_surface: CreateWindowSurface,
    #[cfg(test)]
    p_create_pbuffer_surface: CreatePbufferSurface,
    p_make_current: MakeCurrent,
    p_swap_interval: SwapInterval,
    p_swap_buffers: SwapBuffers,
    p_destroy_surface: DestroySurface,
    p_destroy_context: DestroyContext,
    p_terminate: Terminate,
    p_query_string: QueryString,
}

fn load<T: Copy>(lib: &Library, name: &[u8]) -> Result<T, Box<dyn Error>> {
    let symbol: Symbol<T> = unsafe { lib.get(name) }
        .map_err(|err| format!("{}: {err}", String::from_utf8_lossy(name)))?;
    Ok(*symbol)
}

impl Egl {
    pub fn load() -> Result<Self, Box<dyn Error>> {
        let lib = unsafe { Library::new("libEGL.so.1") }
            .map_err(|err| format!("load libEGL.so.1: {err}"))?;
        let gles = unsafe { Library::new("libGLESv2.so.2") }.ok();
        Ok(Self {
            gles,
            p_get_proc_address: load(&lib, b"eglGetProcAddress\0")?,
            p_get_platform_display: load(&lib, b"eglGetPlatformDisplay\0").ok(),
            p_get_display: load(&lib, b"eglGetDisplay\0")?,
            p_initialize: load(&lib, b"eglInitialize\0")?,
            p_bind_api: load(&lib, b"eglBindAPI\0")?,
            p_choose_config: load(&lib, b"eglChooseConfig\0")?,
            p_create_context: load(&lib, b"eglCreateContext\0")?,
            p_create_window_surface: load(&lib, b"eglCreateWindowSurface\0")?,
            #[cfg(test)]
            p_create_pbuffer_surface: load(&lib, b"eglCreatePbufferSurface\0")?,
            p_make_current: load(&lib, b"eglMakeCurrent\0")?,
            p_swap_interval: load(&lib, b"eglSwapInterval\0")?,
            p_swap_buffers: load(&lib, b"eglSwapBuffers\0")?,
            p_destroy_surface: load(&lib, b"eglDestroySurface\0")?,
            p_destroy_context: load(&lib, b"eglDestroyContext\0")?,
            p_terminate: load(&lib, b"eglTerminate\0")?,
            p_query_string: load(&lib, b"eglQueryString\0")?,
            _lib: lib,
        })
    }

    pub fn get_proc_address(&self, name: &str) -> *const c_void {
        let Ok(owned) = std::ffi::CString::new(name) else {
            return ptr::null();
        };
        let mut ptr = unsafe { (self.p_get_proc_address)(owned.as_ptr()).cast_const() };
        if ptr.is_null()
            && let Some(gles) = &self.gles
            && let Ok(sym) =
                unsafe { gles.get::<unsafe extern "C" fn()>(owned.as_bytes_with_nul()) }
        {
            ptr = (*sym) as *const c_void;
        }
        ptr
    }

    pub fn get_display(&self, native: NativeDisplay) -> Result<Display, Box<dyn Error>> {
        let attribs = [ATTRIB_NONE];
        let display = if let Some(get_platform) = self.p_get_platform_display {
            unsafe { get_platform(PLATFORM_WAYLAND_KHR, native, attribs.as_ptr()) }
        } else {
            ptr::null_mut()
        };
        let display = if display.is_null() {
            unsafe { (self.p_get_display)(native) }
        } else {
            display
        };
        if display.is_null() {
            Err("eglGetPlatformDisplay(WAYLAND) returned EGL_NO_DISPLAY".into())
        } else {
            Ok(display)
        }
    }

    pub fn initialize(&self, display: Display) -> Result<(Int, Int), Box<dyn Error>> {
        let mut major = 0;
        let mut minor = 0;
        if unsafe { (self.p_initialize)(display, &raw mut major, &raw mut minor) } == 0 {
            Err("eglInitialize failed".into())
        } else {
            Ok((major, minor))
        }
    }

    pub fn bind_es(&self) -> Result<(), Box<dyn Error>> {
        if unsafe { (self.p_bind_api)(OPENGL_ES_API) } == 0 {
            Err("eglBindAPI(OPENGL_ES) failed".into())
        } else {
            Ok(())
        }
    }

    pub fn choose_es3_alpha_config(&self, display: Display) -> Result<Config, Box<dyn Error>> {
        self.choose_es3_config(display, WINDOW_BIT)
    }

    /// Offscreen variant for the headless render test.
    #[cfg(test)]
    pub fn choose_es3_pbuffer_config(&self, display: Display) -> Result<Config, Box<dyn Error>> {
        self.choose_es3_config(display, PBUFFER_BIT)
    }

    fn choose_es3_config(&self, display: Display, surface: Int) -> Result<Config, Box<dyn Error>> {
        let attribs = [
            SURFACE_TYPE,
            surface,
            RENDERABLE_TYPE,
            OPENGL_ES3_BIT,
            RED_SIZE,
            8,
            GREEN_SIZE,
            8,
            BLUE_SIZE,
            8,
            ALPHA_SIZE,
            8,
            NONE,
        ];
        let mut config = ptr::null_mut();
        let mut count = 0;
        let ok = unsafe {
            (self.p_choose_config)(
                display,
                attribs.as_ptr(),
                &raw mut config,
                1,
                &raw mut count,
            )
        };
        if ok == 0 || count < 1 || config.is_null() {
            Err("no EGL config with ES3 + 8-bit alpha".into())
        } else {
            Ok(config)
        }
    }

    pub fn create_es3_context(
        &self,
        display: Display,
        config: Config,
    ) -> Result<Context, Box<dyn Error>> {
        let attribs = [CONTEXT_MAJOR_VERSION, 3, CONTEXT_MINOR_VERSION, 0, NONE];
        let ctx =
            unsafe { (self.p_create_context)(display, config, ptr::null_mut(), attribs.as_ptr()) };
        if ctx.is_null() {
            Err("eglCreateContext GLES 3.0 failed".into())
        } else {
            Ok(ctx)
        }
    }

    pub fn create_window_surface(
        &self,
        display: Display,
        config: Config,
        window: NativeWindow,
    ) -> Result<Surface, Box<dyn Error>> {
        let surface =
            unsafe { (self.p_create_window_surface)(display, config, window, ptr::null()) };
        if surface.is_null() {
            Err("eglCreateWindowSurface failed".into())
        } else {
            Ok(surface)
        }
    }

    /// Mesa's windowing-free platform (llvmpipe on CI); the render test's way
    /// to a real context.
    #[cfg(test)]
    pub fn get_surfaceless_display(&self) -> Result<Display, Box<dyn Error>> {
        let Some(get_platform) = self.p_get_platform_display else {
            return Err("eglGetPlatformDisplay is unavailable".into());
        };
        let attribs = [ATTRIB_NONE];
        let display =
            unsafe { get_platform(PLATFORM_SURFACELESS_MESA, ptr::null_mut(), attribs.as_ptr()) };
        if display.is_null() {
            Err("no EGL_MESA_platform_surfaceless display".into())
        } else {
            Ok(display)
        }
    }

    #[cfg(test)]
    pub fn create_pbuffer_surface(
        &self,
        display: Display,
        config: Config,
        width: Int,
        height: Int,
    ) -> Result<Surface, Box<dyn Error>> {
        let attribs = [WIDTH, width, HEIGHT, height, NONE];
        let surface = unsafe { (self.p_create_pbuffer_surface)(display, config, attribs.as_ptr()) };
        if surface.is_null() {
            Err("eglCreatePbufferSurface failed".into())
        } else {
            Ok(surface)
        }
    }

    pub fn make_current(
        &self,
        display: Display,
        draw: Surface,
        read: Surface,
        ctx: Context,
    ) -> Result<(), Box<dyn Error>> {
        if unsafe { (self.p_make_current)(display, draw, read, ctx) } == 0 {
            Err("eglMakeCurrent failed".into())
        } else {
            Ok(())
        }
    }

    pub fn unbind(&self, display: Display) {
        unsafe {
            (self.p_make_current)(display, ptr::null_mut(), ptr::null_mut(), ptr::null_mut());
        }
    }

    pub fn swap_interval(&self, display: Display, interval: Int) -> Result<(), Box<dyn Error>> {
        if unsafe { (self.p_swap_interval)(display, interval) } == 0 {
            Err("eglSwapInterval failed".into())
        } else {
            Ok(())
        }
    }

    pub fn swap_buffers(&self, display: Display, surface: Surface) -> Result<(), Box<dyn Error>> {
        if unsafe { (self.p_swap_buffers)(display, surface) } == 0 {
            Err("eglSwapBuffers failed".into())
        } else {
            Ok(())
        }
    }

    pub fn destroy_surface(&self, display: Display, surface: Surface) {
        unsafe {
            (self.p_destroy_surface)(display, surface);
        }
    }

    pub fn destroy_context(&self, display: Display, ctx: Context) {
        unsafe {
            (self.p_destroy_context)(display, ctx);
        }
    }

    pub fn terminate(&self, display: Display) {
        unsafe {
            (self.p_terminate)(display);
        }
    }

    pub fn query_string(&self, display: Display, name: Int) -> String {
        let ptr = unsafe { (self.p_query_string)(display, name) };
        if ptr.is_null() {
            return String::new();
        }
        unsafe { CStr::from_ptr(ptr) }
            .to_string_lossy()
            .into_owned()
    }
}
