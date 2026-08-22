//! `zwlr_layer_shell_v1` overlay + EGL window surface.
//!
//! Product client is `wayland-client` + `wayland-protocols-wlr`. Mesa still wants
//! a libwayland `wl_surface*` (`wayland-backend` feature `client_system`).
//!
//! Remap after `attach(null)`: Hyprland sets `m_configured = false`. Empty commit
//! (re-apply anchor / exclusive / keyboard) → wait for `configure` →
//! `ack_configure` in the same commit as `eglSwapBuffers`. Never ack during
//! `roundtrip` with no buffer. Swap first → `layerSurface was not configured, but
//! a buffer was attached`.
//!
//! This module owns the EGL window and `eglSwapBuffers`. [`crate::gpu::Gpu`] is
//! shader + atlas + draw list and must not see `WlSurface`.

use std::error::Error;
use std::ffi::c_void;
use std::os::fd::{AsFd, AsRawFd};
use std::time::{Duration, Instant};

use glow::Context as Glow;
use wayland_backend::client::ObjectId;
use wayland_egl::WlEglSurface;

use crate::dummy;
use crate::egl::Egl;

use wayland_client::globals::{registry_queue_init, GlobalListContents};
use wayland_client::protocol::wl_compositor::WlCompositor;
use wayland_client::protocol::wl_output::{self, WlOutput};
use wayland_client::protocol::wl_region::WlRegion;
use wayland_client::protocol::wl_registry::{self, WlRegistry};
use wayland_client::protocol::wl_surface::WlSurface;
use wayland_client::{delegate_noop, Connection, Dispatch, Proxy, QueueHandle};
use wayland_protocols::wp::fractional_scale::v1::client::wp_fractional_scale_manager_v1::WpFractionalScaleManagerV1;
use wayland_protocols::wp::fractional_scale::v1::client::wp_fractional_scale_v1::{
    self, WpFractionalScaleV1,
};
use wayland_protocols::wp::viewporter::client::wp_viewport::WpViewport;
use wayland_protocols::wp::viewporter::client::wp_viewporter::WpViewporter;
use wayland_protocols::xdg::xdg_output::zv1::client::zxdg_output_manager_v1::ZxdgOutputManagerV1;
use wayland_protocols::xdg::xdg_output::zv1::client::zxdg_output_v1::{self, ZxdgOutputV1};
use wayland_protocols_wlr::layer_shell::v1::client::zwlr_layer_shell_v1::{self, ZwlrLayerShellV1};
use wayland_protocols_wlr::layer_shell::v1::client::zwlr_layer_surface_v1::{
    self, ZwlrLayerSurfaceV1,
};

use crate::gpu::Gpu;

pub struct Args {
    output: Option<String>,
    namespace: String,
    duration: Duration,
    unmap_at: Option<Duration>,
    list_outputs: bool,
    requested: bool,
}

impl Args {
    pub fn parse() -> Result<Self, Box<dyn Error>> {
        let mut output = None;
        let mut namespace = "df-hud".to_string();
        let mut duration = Duration::ZERO;
        let mut unmap_at = None;
        let mut list_outputs = false;
        let mut requested = false;

        let mut args = std::env::args().skip(1);
        while let Some(arg) = args.next() {
            let mut next = |name: &str| -> Result<String, Box<dyn Error>> {
                args.next()
                    .ok_or_else(|| format!("{name} needs a value").into())
            };
            match arg.as_str() {
                "-h" | "--help" => {
                    print_help();
                    std::process::exit(0);
                }
                "--list-outputs" => {
                    requested = true;
                    list_outputs = true;
                }
                "--output" => {
                    requested = true;
                    output = Some(next("--output")?);
                }
                "--namespace" => {
                    requested = true;
                    namespace = next("--namespace")?;
                }
                "--duration" => {
                    requested = true;
                    let secs: f32 = next("--duration")?.parse()?;
                    duration = if secs <= 0.0 {
                        Duration::ZERO
                    } else {
                        Duration::from_secs_f32(secs)
                    };
                }
                "--unmap-at" => {
                    requested = true;
                    unmap_at = Some(Duration::from_secs_f32(next("--unmap-at")?.parse()?));
                }
                other => return Err(format!("unknown flag {other} (see --help)").into()),
            }
        }
        Ok(Self {
            output,
            namespace,
            duration,
            unmap_at,
            list_outputs,
            requested,
        })
    }
}

fn print_help() {
    eprintln!(
        "\
df-hud — Phase 2 Wayland + GLES text (Go HUD remains the installed product)

  --output NAME         pin to this connector (DP-1). default: compositor chooses
  --namespace NAME      layer-shell namespace (default df-hud)
  --duration SECS       exit after SECS; 0 runs until Ctrl-C (default 0)
  --unmap-at SECS       unmap at T, remap 5s later (EGL context must survive)
  --list-outputs        print connector names and exit
"
    );
}

/// EGL window + context. Present (`eglSwapBuffers`) lives here so Gpu stays
/// surface-agnostic for Phase 3 WGL.
struct GlWindow {
    egl: Egl,
    display: crate::egl::Display,
    context: crate::egl::Context,
    surface: crate::egl::Surface,
    window: WlEglSurface,
}

impl GlWindow {
    fn new(
        display_ptr: *mut c_void,
        surface: ObjectId,
        buf_w: i32,
        buf_h: i32,
    ) -> Result<(Self, Glow), Box<dyn Error>> {
        if display_ptr.is_null() {
            return Err(
                "wl_display pointer is null; Mesa EGL needs the libwayland (client_system) backend"
                    .into(),
            );
        }
        if !wayland_egl::is_available() {
            return Err("libwayland-egl.so is not available".into());
        }

        let egl = Egl::load()?;
        let display = egl.get_display(display_ptr)?;
        let (major, minor) = egl.initialize(display)?;
        egl.bind_es()?;
        let config = egl.choose_es3_alpha_config(display)?;
        let context = egl.create_es3_context(display, config)?;
        let window = WlEglSurface::new(surface, buf_w, buf_h)?;
        let native = window.ptr() as crate::egl::NativeWindow;
        let surface = egl.create_window_surface(display, config, native)?;
        egl.make_current(display, surface, surface, context)?;
        egl.swap_interval(display, 0)?;

        let gl = unsafe { Glow::from_loader_function(|name| egl.get_proc_address(name)) };
        eprintln!(
            "EGL {major}.{minor} vendor={} version={}",
            egl.query_string(display, crate::egl::VENDOR),
            egl.query_string(display, crate::egl::VERSION)
        );

        Ok((
            Self {
                egl,
                display,
                context,
                surface,
                window,
            },
            gl,
        ))
    }

    fn make_current(&self) -> Result<(), Box<dyn Error>> {
        self.egl
            .make_current(self.display, self.surface, self.surface, self.context)
    }

    fn swap(&self) -> Result<(), Box<dyn Error>> {
        self.egl.swap_buffers(self.display, self.surface)
    }

    fn resize(&self, buf_w: i32, buf_h: i32) {
        self.window.resize(buf_w, buf_h, 0, 0);
    }
}

impl Drop for GlWindow {
    fn drop(&mut self) {
        self.egl.unbind(self.display);
        self.egl.destroy_surface(self.display, self.surface);
        self.egl.destroy_context(self.display, self.context);
        self.egl.terminate(self.display);
    }
}

struct OutputInfo {
    wl: WlOutput,
    xdg: Option<ZxdgOutputV1>,
    name: String,
    description: String,
    scale: i32,
}

struct App {
    qh: QueueHandle<App>,
    compositor: Option<WlCompositor>,
    surface: Option<WlSurface>,
    layer_surface: Option<ZwlrLayerSurfaceV1>,
    viewport: Option<WpViewport>,
    _frac: Option<WpFractionalScaleV1>,
    outputs: Vec<OutputInfo>,
    gpu: Option<Gpu>,
    gl_window: Option<GlWindow>,
    clock: String,
    logical_w: i32,
    logical_h: i32,
    frac_scale: u32,
    mapped: bool,
    configured: bool,
    awaiting_remap: bool,
    closed: bool,
    swaps: u32,
    output_name: String,
    pending_serial: Option<u32>,
    needs_present: bool,
}

impl App {
    fn buffer_size(&self) -> (i32, i32) {
        let w = ((self.logical_w as u64 * self.frac_scale as u64 + 60) / 120).max(1) as i32;
        let h = ((self.logical_h as u64 * self.frac_scale as u64 + 60) / 120).max(1) as i32;
        (w, h)
    }

    fn apply_layer_role(&self) {
        let Some(layer) = &self.layer_surface else {
            return;
        };
        layer.set_anchor(
            zwlr_layer_surface_v1::Anchor::Top
                | zwlr_layer_surface_v1::Anchor::Bottom
                | zwlr_layer_surface_v1::Anchor::Left
                | zwlr_layer_surface_v1::Anchor::Right,
        );
        layer.set_exclusive_zone(-1);
        layer.set_keyboard_interactivity(zwlr_layer_surface_v1::KeyboardInteractivity::None);
        layer.set_size(0, 0);
        self.apply_passthrough();
    }

    fn apply_passthrough(&self) {
        let (Some(compositor), Some(surface)) = (&self.compositor, &self.surface) else {
            return;
        };
        let region = compositor.create_region(&self.qh, ());
        surface.set_input_region(Some(&region));
        surface.set_opaque_region(None);
        region.destroy();
    }

    fn apply_viewport(&self) {
        if let Some(viewport) = &self.viewport {
            if self.logical_w > 0 && self.logical_h > 0 {
                viewport.set_destination(self.logical_w, self.logical_h);
            }
        } else if let Some(surface) = &self.surface {
            let scale = (self.frac_scale / 120).max(1) as i32;
            surface.set_buffer_scale(scale);
        }
        let (w, h) = self.buffer_size();
        if let Some(window) = &self.gl_window {
            window.resize(w, h);
        }
        if let Some(gpu) = &self.gpu {
            gpu.resize(w, h);
        }
    }

    fn present(&mut self) -> Result<(), Box<dyn Error>> {
        if !self.mapped || !self.configured || self.gpu.is_none() || self.gl_window.is_none() {
            return Ok(());
        }
        self.apply_passthrough();
        self.apply_viewport();
        if let Some(serial) = self.pending_serial.take() {
            if let Some(layer) = &self.layer_surface {
                layer.ack_configure(serial);
            }
        }
        let lines = dummy::lines(&self.clock);
        let (buf_w, buf_h) = self.buffer_size();
        self.gl_window.as_ref().expect("gl_window").make_current()?;
        self.gpu.as_mut().expect("gpu").draw(
            buf_w,
            buf_h,
            self.logical_w,
            self.logical_h,
            &lines,
        )?;
        self.gl_window.as_ref().expect("gl_window").swap()?;
        self.swaps += 1;
        self.needs_present = false;
        Ok(())
    }

    fn drop_gpu_before_window(&mut self) {
        self.gpu.take();
        self.gl_window.take();
    }

    fn unmap(&mut self) {
        if let Some(surface) = &self.surface {
            surface.attach(None, 0, 0);
            surface.commit();
        }
        self.mapped = false;
        self.configured = false;
        self.awaiting_remap = false;
        self.pending_serial = None;
        self.needs_present = false;
        eprintln!("unmapped (EGL context kept)");
    }

    fn request_remap(&mut self) {
        // wlr-layer-shell: after a null attach the surface is back to
        // post-get_layer_surface. Re-apply role, commit *without* a buffer,
        // wait for configure, then ack+swap. Swap first = Hyprland protocol error.
        self.apply_layer_role();
        if let Some(surface) = &self.surface {
            surface.commit();
        }
        self.awaiting_remap = true;
        eprintln!("remap: empty commit, waiting for configure");
    }
}

delegate_noop!(App: WlCompositor);
delegate_noop!(App: WlRegion);
delegate_noop!(App: ZwlrLayerShellV1);
delegate_noop!(App: WpViewporter);
delegate_noop!(App: WpViewport);
delegate_noop!(App: WpFractionalScaleManagerV1);
delegate_noop!(App: ZxdgOutputManagerV1);
delegate_noop!(App: ignore WlSurface);

impl Drop for App {
    fn drop(&mut self) {
        self.drop_gpu_before_window();
    }
}

impl Dispatch<WlRegistry, GlobalListContents> for App {
    fn event(
        _: &mut Self,
        _: &WlRegistry,
        _: wl_registry::Event,
        _: &GlobalListContents,
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
    }
}

impl Dispatch<WlOutput, usize> for App {
    fn event(
        state: &mut Self,
        _: &WlOutput,
        event: wl_output::Event,
        index: &usize,
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        let Some(output) = state.outputs.get_mut(*index) else {
            return;
        };
        match event {
            wl_output::Event::Name { name } => {
                if output.name.is_empty() {
                    output.name = name;
                }
            }
            wl_output::Event::Description { description } => output.description = description,
            wl_output::Event::Scale { factor } => output.scale = factor,
            _ => {}
        }
    }
}

impl Dispatch<ZxdgOutputV1, usize> for App {
    fn event(
        state: &mut Self,
        _: &ZxdgOutputV1,
        event: zxdg_output_v1::Event,
        index: &usize,
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        let Some(output) = state.outputs.get_mut(*index) else {
            return;
        };
        if let zxdg_output_v1::Event::Name { name } = event {
            output.name = name;
        }
    }
}

impl Dispatch<ZwlrLayerSurfaceV1, ()> for App {
    fn event(
        state: &mut Self,
        _: &ZwlrLayerSurfaceV1,
        event: zwlr_layer_surface_v1::Event,
        _: &(),
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        match event {
            zwlr_layer_surface_v1::Event::Configure {
                serial,
                width,
                height,
            } => {
                // Ack in present(), in the same commit as eglSwapBuffers. Acking here
                // flushes a configure without a buffer and Hyprland maps an empty surface.
                state.pending_serial = Some(serial);
                if width > 0 {
                    state.logical_w = width as i32;
                }
                if height > 0 {
                    state.logical_h = height as i32;
                }
                state.configured = true;
                if state.awaiting_remap {
                    state.mapped = true;
                    state.awaiting_remap = false;
                    eprintln!("remap configure — attaching buffer next");
                }
                state.needs_present = true;
                eprintln!(
                    "configure {}x{}  scale {}/120 serial {serial}",
                    state.logical_w, state.logical_h, state.frac_scale
                );
            }
            zwlr_layer_surface_v1::Event::Closed => {
                eprintln!("layer surface closed by compositor");
                state.closed = true;
            }
            _ => {}
        }
    }
}

impl Dispatch<WpFractionalScaleV1, ()> for App {
    fn event(
        state: &mut Self,
        _: &WpFractionalScaleV1,
        event: wp_fractional_scale_v1::Event,
        _: &(),
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        if let wp_fractional_scale_v1::Event::PreferredScale { scale } = event {
            state.frac_scale = scale;
            state.apply_viewport();
            eprintln!("fractional scale {scale}/120 ({:.0}%)", scale as f32 / 1.2);
            state.needs_present = true;
        }
    }
}

pub fn run(args: Args) -> Result<(), Box<dyn Error>> {
    let conn = match Connection::connect_to_env() {
        Ok(conn) => conn,
        Err(err) => {
            if args.requested {
                return Err(format!("wayland: {err}").into());
            }
            eprintln!("no Wayland display ({err}); overlay skipped");
            return Ok(());
        }
    };
    run_connected(conn, args)
}

fn run_connected(conn: Connection, args: Args) -> Result<(), Box<dyn Error>> {
    let display_ptr = conn.display().id().as_ptr().cast();
    let (globals, mut event_queue) = registry_queue_init::<App>(&conn)?;
    let qh = event_queue.handle();

    let compositor: WlCompositor = globals.bind(&qh, 4..=6, ())?;
    let layer_shell: ZwlrLayerShellV1 = globals.bind(&qh, 1..=4, ())?;
    let viewporter = globals.bind::<WpViewporter, _, _>(&qh, 1..=1, ()).ok();
    let frac_mgr = globals
        .bind::<WpFractionalScaleManagerV1, _, _>(&qh, 1..=1, ())
        .ok();
    let xdg_output_mgr = globals
        .bind::<ZxdgOutputManagerV1, _, _>(&qh, 1..=3, ())
        .ok();

    let mut app = App {
        qh: qh.clone(),
        compositor: Some(compositor),
        surface: None,
        layer_surface: None,
        viewport: None,
        _frac: None,
        outputs: Vec::new(),
        gpu: None,
        gl_window: None,
        clock: dummy::clock_hms(),
        logical_w: 0,
        logical_h: 0,
        frac_scale: 120,
        mapped: true,
        configured: false,
        awaiting_remap: false,
        closed: false,
        swaps: 0,
        output_name: args.output.clone().unwrap_or_else(|| "auto".into()),
        pending_serial: None,
        needs_present: false,
    };

    let output_globals: Vec<_> = globals
        .contents()
        .clone_list()
        .into_iter()
        .filter(|global| global.interface == "wl_output")
        .collect();
    for (index, global) in output_globals.iter().enumerate() {
        let version = global.version.clamp(1, 4);
        let wl = globals
            .registry()
            .bind::<WlOutput, _, _>(global.name, version, &qh, index);
        app.outputs.push(OutputInfo {
            wl,
            xdg: None,
            name: String::new(),
            description: String::new(),
            scale: 1,
        });
    }
    if let Some(mgr) = &xdg_output_mgr {
        let wls: Vec<WlOutput> = app.outputs.iter().map(|output| output.wl.clone()).collect();
        for (index, wl) in wls.iter().enumerate() {
            app.outputs[index].xdg = Some(mgr.get_xdg_output(wl, &qh, index));
        }
    }
    event_queue.roundtrip(&mut app)?;

    for output in &app.outputs {
        eprintln!(
            "output: {} ({}) scale {}",
            if output.name.is_empty() {
                "<unnamed>"
            } else {
                &output.name
            },
            output.description,
            output.scale
        );
    }
    if args.list_outputs {
        return Ok(());
    }

    let pinned = if let Some(want) = &args.output {
        let found = app
            .outputs
            .iter()
            .find(|output| output.name.eq_ignore_ascii_case(want))
            .ok_or_else(|| format!("output {want} was not found (see --list-outputs)"))?;
        app.output_name = found.name.clone();
        Some(found.wl.clone())
    } else {
        None
    };

    if viewporter.is_none() {
        eprintln!("warning: wp_viewporter missing; 125/150% will be blurry");
    }
    if frac_mgr.is_none() {
        eprintln!("warning: wp_fractional_scale_v1 missing; using integer wl_output.scale");
        if let Some(output) = pinned
            .as_ref()
            .and_then(|wl| app.outputs.iter().find(|o| o.wl.id() == wl.id()))
            .or_else(|| app.outputs.first())
        {
            app.frac_scale = (output.scale.max(1) as u32) * 120;
        }
    }

    let compositor = app.compositor.as_ref().expect("compositor").clone();
    let surface = compositor.create_surface(&qh, ());
    if let Some(mgr) = &frac_mgr {
        app._frac = Some(mgr.get_fractional_scale(&surface, &qh, ()));
    }
    if let Some(viewporter) = &viewporter {
        app.viewport = Some(viewporter.get_viewport(&surface, &qh, ()));
    }
    let layer_surface = layer_shell.get_layer_surface(
        &surface,
        pinned.as_ref(),
        zwlr_layer_shell_v1::Layer::Overlay,
        args.namespace.clone(),
        &qh,
        (),
    );
    app.surface = Some(surface);
    app.layer_surface = Some(layer_surface);
    app.apply_layer_role();
    app.surface.as_ref().expect("surface").commit();

    for _ in 0..32 {
        event_queue.roundtrip(&mut app)?;
        if app.logical_w > 0 && app.logical_h > 0 {
            break;
        }
    }
    if app.logical_w <= 0 || app.logical_h <= 0 {
        return Err("layer surface never got a non-zero configure".into());
    }

    let (buf_w, buf_h) = app.buffer_size();
    app.apply_viewport();
    let surface_id = app.surface.as_ref().expect("surface").id();
    let (gl_window, gl) = GlWindow::new(display_ptr, surface_id, buf_w, buf_h)?;
    app.gpu = Some(Gpu::new(gl, buf_w, buf_h)?);
    app.gl_window = Some(gl_window);

    eprintln!(
        "namespace={} layer=overlay exclusive=-1 keyboard=none output={} swap-interval=0",
        args.namespace, app.output_name
    );
    eprintln!("check: hyprctl layers | grep -A5 {}", args.namespace);
    eprintln!("done-when: outlined dummy text at 1 Hz over the game; clicks still hit it");

    app.present()?;

    let started = Instant::now();
    let remap_after_unmap = Duration::from_secs(5);
    let mut hide_at = args.unmap_at.map(|d| started + d);
    let mut show_at: Option<Instant> = None;
    let mut next_tick = started + Duration::from_secs(1);
    if hide_at.is_some() {
        eprintln!(
            "unmap schedule: hide at {:?}, show 5s later, then stay up",
            args.unmap_at
        );
    }

    loop {
        event_queue.dispatch_pending(&mut app)?;
        if app.closed {
            app.drop_gpu_before_window();
            return Err("compositor closed the layer surface".into());
        }
        if args.duration > Duration::ZERO && started.elapsed() >= args.duration {
            eprintln!("clean shutdown after {} swaps", app.swaps);
            app.drop_gpu_before_window();
            return Ok(());
        }

        let now = Instant::now();
        if now >= next_tick {
            next_tick = now + Duration::from_secs(1);
            let clock = dummy::clock_hms();
            if clock != app.clock {
                app.clock = clock;
                app.needs_present = true;
            }
        }
        if let Some(at) = hide_at {
            if now >= at && app.mapped {
                app.unmap();
                hide_at = None;
                show_at = Some(now + remap_after_unmap);
            }
        }
        if let Some(at) = show_at {
            if now >= at && !app.mapped && !app.awaiting_remap {
                show_at = None;
                app.request_remap();
            }
        }
        if app.needs_present {
            app.present()?;
        }

        event_queue.flush()?;
        let timeout_ms = poll_timeout_ms(now, started, args.duration, hide_at, show_at, next_tick);
        match event_queue.prepare_read() {
            None => continue,
            Some(guard) => {
                let fd = event_queue.as_fd().as_raw_fd();
                let mut pfd = libc::pollfd {
                    fd,
                    events: libc::POLLIN,
                    revents: 0,
                };
                let n = unsafe { libc::poll(&mut pfd, 1, timeout_ms) };
                if n < 0 {
                    let err = std::io::Error::last_os_error();
                    if err.kind() == std::io::ErrorKind::Interrupted {
                        drop(guard);
                        continue;
                    }
                    return Err(err.into());
                }
                if n > 0 && pfd.revents != 0 {
                    guard.read()?;
                } else {
                    drop(guard);
                }
            }
        }
    }
}

fn poll_timeout_ms(
    now: Instant,
    started: Instant,
    duration: Duration,
    hide_at: Option<Instant>,
    show_at: Option<Instant>,
    next_tick: Instant,
) -> i32 {
    let mut deadline: Option<Instant> = Some(next_tick);
    let consider = |acc: &mut Option<Instant>, at: Instant| {
        *acc = Some(match *acc {
            Some(prev) => prev.min(at),
            None => at,
        });
    };
    if duration > Duration::ZERO {
        consider(&mut deadline, started + duration);
    }
    if let Some(at) = hide_at {
        consider(&mut deadline, at);
    }
    if let Some(at) = show_at {
        consider(&mut deadline, at);
    }
    match deadline {
        None => -1,
        Some(at) => at
            .saturating_duration_since(now)
            .as_millis()
            .min(i32::MAX as u128) as i32,
    }
}
