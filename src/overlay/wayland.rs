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
//! This module owns the EGL window and `eglSwapBuffers`. [`crate::overlay::gpu::Gpu`] is
//! shader + atlas + draw list and must not see `WlSurface`.

use std::collections::HashSet;
use std::error::Error;
use std::ffi::c_void;
use std::os::fd::{AsFd, AsRawFd};
use std::path::PathBuf;
use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::{Duration, Instant};

use glow::Context as Glow;
use wayland_backend::client::ObjectId;
use wayland_egl::WlEglSurface;

use crate::app;
use crate::cli::OverlayArgs;
use crate::config::{self, Config};
use crate::overlay::{self, egl::Egl, gpu::Gpu, present};

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

pub struct Args {
    output: Option<String>,
    namespace: String,
    duration: Duration,
    list_outputs: bool,
    requested: bool,
    config: Option<PathBuf>,
    print_hud: bool,
}

impl From<OverlayArgs> for Args {
    fn from(args: OverlayArgs) -> Self {
        Self {
            output: args.output,
            namespace: args.namespace,
            duration: args.duration,
            list_outputs: args.list_outputs,
            requested: args.requested,
            config: args.config,
            print_hud: args.print_hud,
        }
    }
}

/// EGL window + context. Present (`eglSwapBuffers`) lives here so Gpu stays
/// surface-agnostic (WGL uses the same Gpu).
struct GlWindow {
    egl: Egl,
    display: crate::overlay::egl::Display,
    context: crate::overlay::egl::Context,
    surface: crate::overlay::egl::Surface,
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
        let native = window.ptr().cast_mut();
        let surface = egl.create_window_surface(display, config, native)?;
        egl.make_current(display, surface, surface, context)?;
        egl.swap_interval(display, 0)?;

        let gl = unsafe { Glow::from_loader_function(|name| egl.get_proc_address(name)) };
        eprintln!(
            "EGL {major}.{minor} vendor={} version={}",
            egl.query_string(display, crate::overlay::egl::VENDOR),
            egl.query_string(display, crate::overlay::egl::VERSION)
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
    layer_shell: Option<ZwlrLayerShellV1>,
    surface: Option<WlSurface>,
    layer_surface: Option<ZwlrLayerSurfaceV1>,
    viewport: Option<WpViewport>,
    _frac: Option<WpFractionalScaleV1>,
    outputs: Vec<OutputInfo>,
    gpu: Option<Gpu>,
    gl_window: Option<GlWindow>,
    display_ptr: *mut c_void,
    handle: Option<Arc<app::Handle>>,
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
    cfg: Config,
    namespace: String,
    cli_output: Option<String>,
    pinned_output: String,
    warned_outputs: HashSet<String>,
}

impl App {
    fn current_cfg(&self) -> Config {
        self.handle
            .as_ref()
            .map_or_else(|| self.cfg.clone(), |h| h.cfg.lock().unwrap().clone())
    }

    fn sync_config(&mut self) {
        let Some(handle) = &self.handle else {
            return;
        };
        let cfg = handle.cfg.lock().unwrap().clone();
        let monitor_changed = cfg.hud.monitor != self.cfg.hud.monitor;
        self.cfg = cfg;
        if monitor_changed && self.mapped {
            self.unmap();
        }
    }

    fn buffer_size(&self) -> (i32, i32) {
        let w = ((self.logical_w as u64 * u64::from(self.frac_scale) + 60) / 120).max(1) as i32;
        let h = ((self.logical_h as u64 * u64::from(self.frac_scale) + 60) / 120).max(1) as i32;
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
        if !self.mapped || !self.configured {
            return Ok(());
        }
        if let Err(err) = self.ensure_gpu() {
            eprintln!("EGL: {err}");
            return Ok(());
        }
        self.apply_passthrough();
        self.apply_viewport();
        if let Some(serial) = self.pending_serial.take() {
            if let Some(layer) = &self.layer_surface {
                layer.ack_configure(serial);
            }
        }
        let cfg = self.current_cfg();
        let built = match &self.handle {
            Some(h) => overlay::scene(h, self.logical_w as f32, self.logical_h as f32),
            None => {
                present::empty_overlay_scene(&cfg, self.logical_w as f32, self.logical_h as f32)
            }
        };
        let (buf_w, buf_h) = self.buffer_size();
        self.gl_window.as_ref().expect("gl_window").make_current()?;
        let gpu = self.gpu.as_mut().expect("gpu");
        gpu.set_font(&cfg.hud.font);
        gpu.draw(buf_w, buf_h, self.logical_w, self.logical_h, &built)?;
        self.gl_window.as_ref().expect("gl_window").swap()?;
        self.swaps += 1;
        self.needs_present = false;
        Ok(())
    }

    fn ensure_gpu(&mut self) -> Result<(), Box<dyn Error>> {
        if self.gpu.is_some() && self.gl_window.is_some() {
            return Ok(());
        }
        let (buf_w, buf_h) = self.buffer_size();
        let surface_id = self
            .surface
            .as_ref()
            .ok_or("layer surface missing; cannot create EGL")?
            .id();
        let started = Instant::now();
        let (gl_window, gl) = GlWindow::new(self.display_ptr, surface_id, buf_w, buf_h)?;
        let cfg = self.current_cfg();
        let gpu = Gpu::new(gl, buf_w, buf_h, &cfg.hud.font)?;
        eprintln!(
            "EGL context ready in {:.0}ms",
            started.elapsed().as_secs_f64() * 1000.0
        );
        self.gl_window = Some(gl_window);
        self.gpu = Some(gpu);
        Ok(())
    }

    fn drop_gpu_before_window(&mut self) {
        if let Some(window) = &self.gl_window {
            let _ = window.make_current();
        }
        self.gpu.take();
        self.gl_window.take();
    }

    fn unmap(&mut self) {
        let had_gpu = self.gl_window.is_some();
        self.drop_gpu_before_window();
        if let Some(surface) = &self.surface {
            surface.attach(None, 0, 0);
            surface.commit();
        }
        self.mapped = false;
        self.configured = false;
        self.awaiting_remap = false;
        self.pending_serial = None;
        self.needs_present = false;
        if had_gpu {
            eprintln!("unmapped (EGL context dropped)");
        }
    }

    fn vis_monitor(&self) -> String {
        self.handle
            .as_ref()
            .map(|h| h.vis.state().monitor)
            .unwrap_or_default()
    }

    fn wanted_output(&self) -> String {
        config::overlay_monitor(
            self.cli_output.as_deref(),
            &self.cfg.hud.monitor,
            &self.vis_monitor(),
        )
    }

    fn lookup_output(&self, want: &str) -> Option<WlOutput> {
        if want.is_empty() {
            return None;
        }
        self.outputs
            .iter()
            .find(|o| o.name.eq_ignore_ascii_case(want))
            .map(|o| o.wl.clone())
    }

    fn pin_output(&mut self) {
        let want = self.wanted_output();
        let found = self.lookup_output(&want);
        if !want.is_empty() && found.is_none() && self.warned_outputs.insert(want.clone()) {
            let names: Vec<&str> = self
                .outputs
                .iter()
                .map(|o| o.name.as_str())
                .filter(|s| !s.is_empty())
                .collect();
            eprintln!(
                "hud: no output named {want:?} (have {}); using compositor default",
                names.join(", ")
            );
        }
        let pin_name = if found.is_some() { want } else { String::new() };
        if pin_name == self.pinned_output && self.layer_surface.is_some() {
            return;
        }
        self.rebind_layer(found.as_ref());
        self.pinned_output = pin_name.clone();
        self.output_name = if pin_name.is_empty() {
            "auto".into()
        } else {
            pin_name
        };
    }

    fn rebind_layer(&mut self, output: Option<&WlOutput>) {
        if let Some(old) = self.layer_surface.take() {
            old.destroy();
        }
        let Some(shell) = &self.layer_shell else {
            return;
        };
        let Some(surface) = &self.surface else {
            return;
        };
        let ls = shell.get_layer_surface(
            surface,
            output,
            zwlr_layer_shell_v1::Layer::Overlay,
            self.namespace.clone(),
            &self.qh,
            (),
        );
        self.layer_surface = Some(ls);
        self.configured = false;
        self.pending_serial = None;
        self.mapped = false;
    }

    fn request_remap(&mut self) {
        // wlr-layer-shell: after a null attach the surface is back to
        // post-get_layer_surface. Re-apply role, commit *without* a buffer,
        // wait for configure, then ack+swap. Swap first = Hyprland protocol error.
        self.pin_output();
        self.apply_layer_role();
        if let Some(surface) = &self.surface {
            surface.commit();
        }
        self.awaiting_remap = true;
        eprintln!(
            "remap: empty commit, waiting for configure (output={})",
            self.output_name
        );
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
        (): &(),
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
        (): &(),
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

    let mut watch = config::Watch::open(args.config.clone())?;

    let mut app = App {
        qh: qh.clone(),
        compositor: Some(compositor),
        layer_shell: Some(layer_shell),
        surface: None,
        layer_surface: None,
        viewport: None,
        _frac: None,
        outputs: Vec::new(),
        gpu: None,
        gl_window: None,
        display_ptr,
        handle: None,
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
        cfg: watch.cfg.clone(),
        namespace: args.namespace.clone(),
        cli_output: args.output.clone(),
        pinned_output: String::new(),
        warned_outputs: HashSet::new(),
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

    app.handle = Some(app::start_with(
        watch.cfg.clone(),
        app::PrintOpts {
            hud: args.print_hud,
        },
    )?);

    if viewporter.is_none() {
        eprintln!("warning: wp_viewporter missing; 125/150% will be blurry");
    }
    if frac_mgr.is_none() {
        eprintln!("warning: wp_fractional_scale_v1 missing; using integer wl_output.scale");
        if let Some(output) = app.outputs.first() {
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
    app.surface = Some(surface);
    app.pin_output();
    if frac_mgr.is_none() {
        if let Some(output) = app
            .outputs
            .iter()
            .find(|o| o.name.eq_ignore_ascii_case(&app.output_name))
            .or_else(|| app.outputs.first())
        {
            app.frac_scale = (output.scale.max(1) as u32) * 120;
        }
    }
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

    app.apply_viewport();
    let vis = app
        .handle
        .as_ref()
        .is_none_or(|h| h.visible.load(Ordering::SeqCst));
    if vis {
        app.ensure_gpu()?;
    }

    eprintln!(
        "namespace={} layer=overlay exclusive=-1 keyboard=none output={} swap-interval=0",
        args.namespace, app.output_name
    );
    eprintln!("check: hyprctl layers | grep -A5 {}", args.namespace);
    eprintln!("done-when: live HUD (block / XP / challenges / map) at 1 Hz over the game; clicks still pass through");

    app.present()?;

    let started = Instant::now();
    let mut next_tick = overlay::start_tick(started);

    loop {
        event_queue.dispatch_pending(&mut app)?;
        if app.closed {
            app.drop_gpu_before_window();
            return Err("compositor closed the layer surface".into());
        }
        if overlay::expired(started, args.duration) {
            eprintln!("clean shutdown after {} swaps", app.swaps);
            app.drop_gpu_before_window();
            return Ok(());
        }

        if let Some(h) = &app.handle {
            if h.stopped() {
                app.drop_gpu_before_window();
                return Ok(());
            }
        }
        let now = Instant::now();
        if overlay::due(now, &mut next_tick) {
            app.needs_present = true;
            if let Some(cfg) = overlay::take_reload(&mut watch) {
                if let Some(h) = app.handle.as_ref() {
                    h.note_config_watch(&watch, true);
                    let applied = match app.gpu.as_mut() {
                        Some(gpu) => overlay::push_config(h, gpu, &cfg),
                        None => h.replace_config(cfg),
                    };
                    let monitor_changed = applied.hud.monitor != app.cfg.hud.monitor;
                    app.cfg = applied;
                    if monitor_changed && app.mapped {
                        app.unmap();
                    }
                } else {
                    app.cfg = cfg;
                }
            } else if let Some(h) = app.handle.as_ref() {
                h.note_config_watch(&watch, false);
            }
        }
        let vis = app
            .handle
            .as_ref()
            .is_none_or(|h| h.visible.load(Ordering::SeqCst));
        if vis {
            if !app.mapped && !app.awaiting_remap {
                app.request_remap();
            }
        } else if app.mapped {
            app.unmap();
        } else if app.gl_window.is_some() {
            app.drop_gpu_before_window();
            eprintln!("unmapped (EGL context dropped)");
        }
        if app.needs_present {
            app.present()?;
        }

        event_queue.flush()?;
        let timeout_ms = overlay::wait_ms(now, started, args.duration, next_tick) as i32;
        if let Some(guard) = event_queue.prepare_read() {
            let fd = event_queue.as_fd().as_raw_fd();
            let wake_fd = app.handle.as_ref().map_or(-1, |h| h.wake.read_fd());
            let mut pfds = [
                libc::pollfd {
                    fd,
                    events: libc::POLLIN,
                    revents: 0,
                },
                libc::pollfd {
                    fd: wake_fd,
                    events: libc::POLLIN,
                    revents: 0,
                },
            ];
            let nfds = if wake_fd >= 0 { 2 } else { 1 };
            let n = unsafe { libc::poll(pfds.as_mut_ptr(), nfds, timeout_ms) };
            if n < 0 {
                let err = std::io::Error::last_os_error();
                if err.kind() == std::io::ErrorKind::Interrupted {
                    drop(guard);
                    continue;
                }
                return Err(err.into());
            }
            if n > 0 && pfds[0].revents != 0 {
                guard.read()?;
            } else {
                drop(guard);
            }
            if n > 0 && nfds > 1 && pfds[1].revents != 0 {
                if let Some(h) = &app.handle {
                    h.wake.take();
                }
                app.sync_config();
                app.needs_present = true;
            }
        }
    }
}
