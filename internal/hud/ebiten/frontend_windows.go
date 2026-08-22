//go:build windows && !nolayershell

package ebitenhud

import (
	"context"
	"df-hud/internal/config"
	"df-hud/internal/hud/scene"
	"df-hud/internal/model"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

type game struct {
	ctx            context.Context
	deps           Dependencies
	renderer       *renderer
	monitors       []desktopMonitor
	engineMonitors []*ebiten.MonitorType
	monitorIndexes map[string]int
	unityDisplay   unityDisplayConfig

	target              desktopMonitor
	positioned          bool
	placementCandidates []int
	placementAttempt    int
	placementSized      bool
	placementRequested  bool
	warned              map[string]bool

	changed chan struct{}
	stopped atomic.Bool

	frame       scene.Scene
	visible     bool
	drawPending bool
	lastSecond  int64
	backendSeen bool
}

// Run owns the native Ebitengine UI loop until ctx is cancelled.
func Run(ctx context.Context, deps Dependencies) error {
	if !deps.valid() {
		return errors.New("incomplete Ebitengine HUD dependencies")
	}
	monitors, err := desktopMonitors()
	if err != nil {
		return fmt.Errorf("enumerate monitors: %w", err)
	}
	target, ok := initialMonitor(monitors, deps.Config(), deps.Visibility())
	if !ok {
		return errors.New("Windows reported no usable monitor")
	}
	engineMonitors := ebiten.AppendMonitors(nil)
	if len(engineMonitors) == 0 {
		return errors.New("Ebitengine reported no usable monitor")
	}
	drawer, err := newRenderer()
	if err != nil {
		return err
	}

	unityDisplay := readUnityDisplayConfig()
	g := &game{
		ctx:            ctx,
		deps:           deps,
		renderer:       drawer,
		monitors:       monitors,
		engineMonitors: engineMonitors,
		monitorIndexes: map[string]int{},
		unityDisplay:   unityDisplay,
		target:         target,
		warned:         map[string]bool{},
		changed:        make(chan struct{}, 1),
		drawPending:    true,
	}
	g.resetPlacement()
	deps.OnVisibilityChange(func(model.Visibility) { g.wake() })
	deps.OnGroupsChange(g.wake)
	deps.OnConfigReload(func(*config.Config) { g.wake() })
	go func() {
		<-ctx.Done()
		g.stopped.Store(true)
		g.wake()
	}()

	cfg := deps.Config()
	if cfg.HUD.CSS != "" {
		log.Printf("hud: %s is Linux/GTK-only; the native Windows HUD ignores hud.css", cfg.HUD.CSS)
	}
	if unityDisplay.Width > 0 && unityDisplay.Height > 0 {
		log.Printf("hud: Unity display config %dx%d fullscreen=%d monitor=%d",
			unityDisplay.Width, unityDisplay.Height,
			unityDisplay.Fullscreen, unityDisplay.Monitor)
	}

	ebiten.SetWindowTitle(windowTitle)
	ebiten.SetWindowDecorated(false)
	ebiten.SetWindowFloating(true)
	// Before creation is required for transparent GLFW windows; Update keeps
	// reasserting it after monitor and native-surface changes.
	ebiten.SetWindowMousePassthrough(true)
	width, height := monitorWindowSize(target)
	ebiten.SetWindowSize(width, height)
	ebiten.SetMonitor(g.engineMonitors[g.placementCandidates[0]])
	g.placementSized = true
	g.placementRequested = true
	// Ten updates per second keeps tray/hotkey changes responsive. With screen
	// clearing disabled, unchanged Draw calls do no GPU work.
	ebiten.SetTPS(10)
	ebiten.SetScreenClearedEveryFrame(false)

	log.Printf("hud: starting native Windows frontend with explicit OpenGL on %s", target.name)
	return ebiten.RunGameWithOptions(g, &ebiten.RunGameOptions{
		ScreenTransparent: true,
		InitUnfocused:     true,
		SkipTaskbar:       true,
		GraphicsLibrary:   ebiten.GraphicsLibraryOpenGL,
	})
}

func (g *game) wake() {
	select {
	case g.changed <- struct{}{}:
	default:
	}
}

func (g *game) Update() error {
	if g.stopped.Load() || g.ctx.Err() != nil {
		return ebiten.Termination
	}
	if !g.backendSeen {
		var info ebiten.DebugInfo
		ebiten.ReadDebugInfo(&info)
		log.Printf("hud: graphics backend active: %s", info.GraphicsLibrary)
		g.backendSeen = true
	}

	changed := false
	select {
	case <-g.changed:
		changed = true
	default:
	}
	if g.updateWindowTarget() {
		changed = true
	}
	ebiten.SetWindowMousePassthrough(true)

	now := time.Now()
	second := now.Unix()
	if second != g.lastSecond {
		g.lastSecond = second
		changed = true
		if err := g.deps.MaybeSave(); err != nil {
			log.Printf("state: could not save: %v", err)
		}
	}
	visibility := g.deps.Visibility()
	// A monitor transition takes multiple updates by design: size first,
	// SetMonitor next, then Win32 verification. Keep the frame transparent on
	// the old monitor until verification so HUD content never flashes there.
	visible := visibility.Visible && g.positioned
	if visible != g.visible {
		g.visible = visible
		changed = true
	}
	if !changed {
		return nil
	}

	if !g.visible {
		g.frame = scene.Scene{}
		g.drawPending = true
		return nil
	}
	cfg := g.deps.Config()
	if cfg.HUD.CSS != "" && !g.warned["css:"+cfg.HUD.CSS] {
		log.Printf("hud: %s is Linux/GTK-only; ignoring it on Windows", cfg.HUD.CSS)
		g.warned["css:"+cfg.HUD.CSS] = true
	}
	if display := readUnityDisplayConfig(); display.Width > 0 && display.Height > 0 {
		g.unityDisplay = display
	}
	width, height := monitorWindowSize(g.target)
	g.frame = scene.Build(g.deps.Derive(now), cfg,
		scene.Viewport{
			Width: width, Height: height,
			GameWidth: g.unityDisplay.Width, GameHeight: g.unityDisplay.Height,
		}, g.deps.GroupHidden)
	g.drawPending = true
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	if !g.drawPending {
		return
	}
	screen.Clear()
	if g.visible {
		g.renderer.draw(screen, g.frame)
	}
	g.drawPending = false
}

func (g *game) Layout(_, _ int) (int, int) {
	return monitorWindowSize(g.target)
}

func (g *game) updateWindowTarget() bool {
	wanted := g.selectMonitor(g.deps.Config(), g.deps.Visibility())
	targetChanged := !strings.EqualFold(wanted.name, g.target.name)
	if targetChanged {
		g.target = wanted
		g.resetPlacement()
	}
	if g.positioned {
		return targetChanged
	}

	if !g.placementSized {
		width, height := monitorWindowSize(g.target)
		ebiten.SetWindowSize(width, height)
		g.placementSized = true
		return true
	}
	if !g.placementRequested {
		if g.placementAttempt >= len(g.placementCandidates) {
			key := "placement:" + strings.ToLower(g.target.name)
			if !g.warned[key] {
				log.Printf("hud: no Ebitengine monitor mapped to %s", g.target.name)
				g.warned[key] = true
			}
			return targetChanged
		}
		index := g.placementCandidates[g.placementAttempt]
		// SetMonitor must complete and Update must return before position/size
		// are touched. Calling all three in one tick moves the window back to its
		// previous monitor on Windows.
		ebiten.SetMonitor(g.engineMonitors[index])
		g.placementRequested = true
		return true
	}

	actual, err := hudWindowMonitor()
	if err != nil {
		return true
	}
	if !strings.EqualFold(actual.name, g.target.name) {
		g.placementAttempt++
		g.placementRequested = false
		return true
	}

	width, height := monitorWindowSize(g.target)
	g.positioned = true
	index := g.placementCandidates[g.placementAttempt]
	g.monitorIndexes[strings.ToLower(g.target.name)] = index
	log.Printf("hud: native window verified on %s at %d,%d (%dx%d, Ebitengine monitor %d)",
		g.target.name, g.target.left+windowInset, g.target.top+windowInset,
		width, height, index)
	return true
}

func (g *game) resetPlacement() {
	g.positioned = false
	g.placementAttempt = 0
	g.placementSized = false
	g.placementRequested = false

	preferred, ok := g.monitorIndexes[strings.ToLower(g.target.name)]
	if !ok {
		preferred = desktopMonitorIndex(g.monitors, g.target)
	}
	if preferred < 0 || preferred >= len(g.engineMonitors) {
		preferred = 0
	}
	g.placementCandidates = []int{preferred}
	for i := range g.engineMonitors {
		if i != preferred {
			g.placementCandidates = append(g.placementCandidates, i)
		}
	}
}

func (g *game) selectMonitor(cfg *config.Config, visibility model.Visibility) desktopMonitor {
	name := strings.TrimSpace(cfg.HUD.Monitor)
	if name == "" || strings.EqualFold(name, "auto") {
		name = strings.TrimSpace(visibility.Monitor)
	}
	if name == "" {
		// During browser -> launcher -> game transitions there are brief periods
		// with no matched game HWND. Keep the last game target: following the
		// HUD's current/cursor monitor here makes it visibly jump away and back.
		return g.target
	}
	if monitor, ok := monitorByName(g.monitors, name); ok {
		return monitor
	}
	key := "monitor:" + strings.ToLower(name)
	if !g.warned[key] {
		log.Printf("hud: monitor %q was not found; keeping %s", name, g.target.name)
		g.warned[key] = true
	}
	return g.target
}

func initialMonitor(monitors []desktopMonitor, cfg *config.Config,
	visibility model.Visibility) (desktopMonitor, bool) {
	name := strings.TrimSpace(cfg.HUD.Monitor)
	if name == "" || strings.EqualFold(name, "auto") {
		name = strings.TrimSpace(visibility.Monitor)
	}
	if name != "" {
		if monitor, ok := monitorByName(monitors, name); ok {
			return monitor, true
		}
	}
	return primaryMonitor(monitors)
}
