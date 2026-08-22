//! Throwaway Linux overlay spike for the df-hud rewrite.
//!
//! This is Rust on purpose: the product talks Wayland through `wayland-client` +
//! `wayland-protocols-wlr` and attaches GLES via `wl_egl_window`. A C libwayland
//! sample would prove Mesa can composite *a* layer surface, not *this* client.
//! Mesa's Wayland EGL platform still wants a libwayland `wl_surface*`, so
//! `wayland-backend/client_system` is on — that is FFI for EGL, not GTK.
//!
//! Must-pass (over Dead Frontier, not a terminal):
//!   cargo run --release -- --hz 60 --duration 30
//!   hyprctl layers | grep -A5 df-hud-spike
//!   tint sits on the fullscreen game; mouse-look still hits the game
//!
//! Hitch test (swap-interval 0 at 1 Hz should feel like no overlay):
//!   cargo run --release -- --hz 1 --swap-interval 0
//!   cargo run --release -- --hz 1 --swap-interval 1   # allowed to fail
//!
//! Should-pass extras: --grid  --unmap-at 8  --output DP-1  --namespace df-hud
//!
//! Default namespace is df-hud-spike so existing Hyprland blur rules do not apply.
//! Re-test with --namespace df-hud after the compositor-rules-off run.

mod egl;
mod font;
mod gpu;

use std::error::Error;
use std::os::fd::{AsFd, AsRawFd};
use std::time::{Duration, Instant};

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

use crate::gpu::{Frame, Gpu};

struct Args {
    output: Option<String>,
    namespace: String,
    duration: Duration,
    swap_interval: i32,
    hz: f32,
    grid: bool,
    unmap_at: Option<Duration>,
    list_outputs: bool,
    layer: zwlr_layer_shell_v1::Layer,
    solid: bool,
    text_only: bool,
}

impl Args {
    fn parse() -> Result<Self, Box<dyn Error>> {
        let mut output = None;
        let mut namespace = "df-hud-spike".to_string();
        let mut duration = Duration::from_secs(30);
        let mut swap_interval = 0;
        let mut hz = 60.0;
        let mut grid = false;
        let mut unmap_at = None;
        let mut list_outputs = false;
        let mut layer = zwlr_layer_shell_v1::Layer::Overlay;
        let mut solid = false;
        let mut text_only = false;

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
                "--list-outputs" => list_outputs = true,
                "--grid" => grid = true,
                "--solid" => solid = true,
                "--text-only" => text_only = true,
                "--output" => output = Some(next("--output")?),
                "--namespace" => namespace = next("--namespace")?,
                "--duration" => {
                    let secs: f32 = next("--duration")?.parse()?;
                    duration = if secs <= 0.0 {
                        Duration::ZERO
                    } else {
                        Duration::from_secs_f32(secs)
                    };
                }
                "--swap-interval" => swap_interval = next("--swap-interval")?.parse()?,
                "--hz" => hz = next("--hz")?.parse()?,
                "--unmap-at" => {
                    unmap_at = Some(Duration::from_secs_f32(next("--unmap-at")?.parse()?));
                }
                "--layer" => {
                    layer = match next("--layer")?.as_str() {
                        "overlay" => zwlr_layer_shell_v1::Layer::Overlay,
                        "top" => zwlr_layer_shell_v1::Layer::Top,
                        "bottom" => zwlr_layer_shell_v1::Layer::Bottom,
                        "background" => zwlr_layer_shell_v1::Layer::Background,
                        other => return Err(format!("unknown --layer {other}").into()),
                    };
                }
                other => return Err(format!("unknown flag {other} (see --help)").into()),
            }
        }
        if hz <= 0.0 {
            return Err("--hz must be positive".into());
        }
        Ok(Self {
            output,
            namespace,
            duration,
            swap_interval,
            hz,
            grid,
            unmap_at,
            list_outputs,
            layer,
            solid,
            text_only,
        })
    }
}

fn print_help() {
    eprintln!(
        "\
linux-overlay-spike — EGL on zwlr_layer_shell_v1, over the real game

  --output NAME         pin to this connector (DP-1). default: compositor chooses
  --namespace NAME      layer-shell namespace (default df-hud-spike)
  --duration SECS       exit after SECS; 0 runs until Ctrl-C (default 30)
  --hz N                present rate (default 60; use 1 for the hitch test)
  --swap-interval 0|1   EGL swap interval (default 0)
  --layer overlay|top   overlay is required over fullscreen; top is a negative test
  --grid                1px lines every 32 logical px (fractional-scale check)
  --solid               opaque magenta fullscreen (proves pixels land)
  --text-only           outlined white text only (premult / fringe check)
  --unmap-at SECS       unmap at T, remap 5s later (EGL context must survive)
  --list-outputs        print connector names and exit
"
    );
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
    logical_w: i32,
    logical_h: i32,
    frac_scale: u32,
    mapped: bool,
    configured: bool,
    awaiting_remap: bool,
    closed: bool,
    swaps: u32,
    output_name: String,
    swap_interval: i32,
    hz: f32,
    grid: bool,
    solid: bool,
    text_only: bool,
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
        if self.solid {
            surface.set_opaque_region(None);
        } else if self.text_only {
            surface.set_opaque_region(Some(&region));
        } else {
            let opaque = compositor.create_region(&self.qh, ());
            let pw = 640;
            let ph = 220;
            let x = (self.logical_w - pw).max(0) / 2;
            let y = (self.logical_h - ph).max(0) / 2;
            opaque.add(x, y, pw, ph);
            surface.set_opaque_region(Some(&opaque));
            opaque.destroy();
        }
        region.destroy();
    }

    fn apply_viewport(&self) {
        if let Some(viewport) = &self.viewport {
            if self.logical_w > 0 && self.logical_h > 0 {
                viewport.set_destination(self.logical_w, self.logical_h);
            }
        }
        if let Some(gpu) = &self.gpu {
            let (w, h) = self.buffer_size();
            gpu.resize(w, h);
        }
    }

    fn present(&mut self) -> Result<(), Box<dyn Error>> {
        if !self.mapped || !self.configured || self.gpu.is_none() {
            return Ok(());
        }
        self.apply_passthrough();
        self.apply_viewport();
        if let Some(serial) = self.pending_serial.take() {
            if let Some(layer) = &self.layer_surface {
                layer.ack_configure(serial);
            }
        }
        let gpu = self.gpu.as_ref().expect("gpu");
        let (buf_w, buf_h) = self.buffer_size();
        gpu.draw(&Frame {
            buf_w,
            buf_h,
            logical_w: self.logical_w,
            logical_h: self.logical_h,
            frac_scale: self.frac_scale,
            swaps: self.swaps,
            hz: self.hz,
            swap_interval: self.swap_interval,
            output_name: &self.output_name,
            grid: self.grid,
            solid: self.solid,
            text_only: self.text_only,
        })?;
        self.swaps += 1;
        self.needs_present = false;
        Ok(())
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
        }
    }
}

fn main() {
    if let Err(err) = run() {
        eprintln!("FAILED: {err}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), Box<dyn Error>> {
    let args = Args::parse()?;
    let conn = Connection::connect_to_env()?;
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
        logical_w: 0,
        logical_h: 0,
        frac_scale: 120,
        mapped: true,
        configured: false,
        awaiting_remap: false,
        closed: false,
        swaps: 0,
        output_name: args.output.clone().unwrap_or_else(|| "auto".into()),
        swap_interval: args.swap_interval,
        hz: args.hz,
        grid: args.grid,
        solid: args.solid,
        text_only: args.text_only,
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
        let version = global.version.min(4).max(1);
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
        if !args.solid {
            app.viewport = Some(viewporter.get_viewport(&surface, &qh, ()));
        }
    }
    let layer_surface = layer_shell.get_layer_surface(
        &surface,
        pinned.as_ref(),
        args.layer,
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
    app.gpu = Some(Gpu::new(
        display_ptr,
        surface_id,
        buf_w,
        buf_h,
        args.swap_interval,
    )?);

    let layer_name = match args.layer {
        zwlr_layer_shell_v1::Layer::Overlay => "overlay",
        zwlr_layer_shell_v1::Layer::Top => "top",
        zwlr_layer_shell_v1::Layer::Bottom => "bottom",
        zwlr_layer_shell_v1::Layer::Background => "background",
        _ => "?",
    };
    eprintln!(
        "namespace={} layer={} exclusive=-1 keyboard=none output={} swap-interval={} {:.0} Hz",
        args.namespace, layer_name, app.output_name, args.swap_interval, args.hz
    );
    eprintln!("check: hyprctl layers | grep -A5 {}", args.namespace);
    eprintln!(
        "done-when: tint over the running fullscreen game, clicks and mouse-look still hit it"
    );
    if args.namespace == "df-hud" {
        eprintln!(
            "note: namespace df-hud matches contrib/df-hud.lua layer rules (test blur off first)"
        );
    }

    app.present()?;

    let started = Instant::now();
    let period = Duration::from_secs_f32(1.0 / args.hz);
    let mut next_present = Instant::now() + period;
    let remap_after_unmap = Duration::from_secs(5);
    let mut hide_at = args.unmap_at.map(|d| started + d);
    let mut show_at: Option<Instant> = None;
    if hide_at.is_some() {
        eprintln!(
            "unmap schedule: hide at {:?}, show 5s later, then stay up",
            args.unmap_at
        );
    }

    loop {
        event_queue.dispatch_pending(&mut app)?;
        if app.closed {
            return Err("compositor closed the layer surface".into());
        }
        if args.duration > Duration::ZERO && started.elapsed() >= args.duration {
            eprintln!("clean shutdown after {} swaps", app.swaps);
            return Ok(());
        }

        let now = Instant::now();
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
        if now >= next_present || app.needs_present {
            app.present()?;
            next_present = now + period;
        }

        event_queue.flush()?;
        let timeout = next_present.saturating_duration_since(Instant::now());
        let timeout_ms = timeout.as_millis().min(i32::MAX as u128) as i32;
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
