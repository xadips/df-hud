package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Talking to Hyprland's command socket, for the two questions /proc cannot
// answer: which workspace the game's window is on, and which monitor it is on.
//
// This is a strict addition to the /proc detection in game.go, never a
// replacement for it. /proc still decides whether the game is running, because
// that decision drives network traffic and the session clock and must work on any
// compositor. What lives here only decides where the HUD is DRAWN, so when it
// fails the honest fallback is "draw it everywhere", which is what df-hud did
// before any of this existed.
//
// Everything here therefore fails open, and says so in the log rather than
// silently leaving the HUD off.

// hyprSocketPath locates one of Hyprland's two sockets:
//
//	.socket.sock  - request/response commands (this file)
//	.socket2.sock - the event stream (game.go)
//
// Hyprland moved these from /tmp/hypr to $XDG_RUNTIME_DIR/hypr, so both are
// checked, the runtime dir first.
// hyprDirs is a variable so tests can point it at a temporary directory. Without
// that they would find the real compositor on the developer's own machine, which is
// exactly the thing this function is now allowed to do.
var hyprDirs = hyprInstanceDirs

func hyprSocketPath(name string) (string, error) {
	var candidates []string
	dirs := hyprDirs()
	if sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"); sig != "" {
		for _, dir := range dirs {
			candidates = append(candidates, filepath.Join(dir, sig, name))
		}
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	// The environment was absent or stale, so look for the running compositor
	// instead. This is what makes df-hud work as a systemd user service: the
	// signature changes every time Hyprland starts, and a service inherits
	// whatever was imported into the session environment, which after a compositor
	// restart is last time's value.
	//
	// ONLY when exactly one instance is running. With two, there is no way to tell
	// which one asked for the HUD, and picking one would mean reading the wrong
	// compositor's window list - which is worse than failing open, because it looks
	// like an answer.
	if sock, found, err := loneHyprSocket(dirs, name); err == nil && found {
		return sock, nil
	}
	if len(candidates) == 0 {
		return "", errors.New("HYPRLAND_INSTANCE_SIGNATURE is unset and no single running " +
			"Hyprland instance was found")
	}
	return "", fmt.Errorf("no Hyprland socket %s (looked in %s)", name, strings.Join(candidates, ", "))
}

// hyprInstanceDirs is where Hyprland keeps its per-instance sockets, runtime dir
// first. It moved these from /tmp/hypr to $XDG_RUNTIME_DIR/hypr, so both are
// checked.
func hyprInstanceDirs() []string {
	var dirs []string
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		dirs = append(dirs, filepath.Join(runtimeDir, "hypr"))
	}
	return append(dirs, filepath.Join("/tmp", "hypr"))
}

// loneHyprSocket finds the named socket when exactly one instance directory has
// one. found is false when there is none or more than one.
func loneHyprSocket(dirs []string, name string) (path string, found bool, err error) {
	for _, dir := range dirs {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			continue
		}
		var hits []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			candidate := filepath.Join(dir, e.Name(), name)
			if _, statErr := os.Stat(candidate); statErr == nil {
				hits = append(hits, candidate)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], true, nil
		default:
			return "", false, fmt.Errorf("%d Hyprland instances are running, so which one to ask is ambiguous", len(hits))
		}
	}
	return "", false, nil
}

// hyprRequest runs one command and returns the raw reply. The protocol is as
// simple as it looks: write the command, read until the server closes.
//
// The socket is spoken to directly rather than through the hyprctl binary. Not
// for speed - it is to avoid a fork per query and a dependency on a binary being
// on PATH inside whatever session df-hud was started from.
func hyprRequest(ctx context.Context, cmd string) ([]byte, error) {
	path, err := hyprSocketPath(".socket.sock")
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// The context has to be able to interrupt a stuck read: a compositor busy
	// enough to stall here is exactly when the HUD must not freeze with it.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil, err
	}
	reply, err := io.ReadAll(conn)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	return reply, nil
}

// hyprWindow is the subset of `hyprctl -j clients` df-hud reads. Field names
// verified against a live Hyprland; note that "monitor" is a numeric monitor ID,
// not a connector name, which is why the monitor list has to be fetched too.
type hyprWindow struct {
	Class        string `json:"class"`
	InitialClass string `json:"initialClass"`
	Title        string `json:"title"`
	PID          int    `json:"pid"`
	Monitor      int    `json:"monitor"`
	Mapped       bool   `json:"mapped"`
	Workspace    struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"workspace"`
}

// hyprMonitor is the subset of `hyprctl -j monitors`.
type hyprMonitor struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	Focused         bool   `json:"focused"`
	ActiveWorkspace struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"activeWorkspace"`
	SpecialWorkspace struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"specialWorkspace"`
}

// windowPlacement is where the compositor says the game's window is. Known is
// false when no window could be matched, which callers must treat as "no
// information" rather than as "not visible".
type windowPlacement struct {
	Known bool

	Class         string
	Title         string
	Workspace     int
	WorkspaceName string
	Monitor       string

	// OnActiveWorkspace is whether that workspace is the one currently shown on
	// its monitor. This is computed rather than read from the window's own
	// "visible" field, which does not mean this: a live check found windows on
	// inactive workspaces reporting visible = true.
	OnActiveWorkspace bool

	// MatchedBy records which strategy found the window, so the log can say how
	// it was identified rather than leaving it a mystery when it goes wrong.
	MatchedBy string

	// LauncherOnly is the game's class or pid appearing ONLY on windows that were
	// ignored by title - which is to say the launcher is open and the game has not
	// started.
	//
	// It exists because "we could not find the window" and "we found only the
	// launcher" are different answers and Known cannot tell them apart. Known false
	// fails OPEN, on the grounds that a window may not be mapped yet and a wrongly
	// hidden HUD is indistinguishable from a broken one - so excluding the launcher
	// without this made the HUD show on every workspace instead of just the
	// launcher's, which is worse than the bug it fixed.
	//
	// This is positive evidence, so it can be acted on.
	LauncherOnly bool
}

// windowMatch is how to recognise the game's window.
//
// A class alone is not enough, which took a launcher to notice: Dead Frontier's
// configuration dialogs are the same executable and report the same class, so the
// class matched a 464x406 settings box and the HUD was drawn over it. IgnoreTitles is
// what rules those out - see GameConfig.WindowTitleIgnore for why it is an exclusion
// rather than a positive match on the game's own title.
type windowMatch struct {
	Class        string
	IgnoreTitles []string
}

// ignored reports whether a title says "this is not the game".
func (m windowMatch) ignored(title string) bool {
	low := strings.ToLower(title)
	for _, skip := range m.IgnoreTitles {
		skip = strings.ToLower(strings.TrimSpace(skip))
		if skip != "" && strings.Contains(low, skip) {
			return true
		}
	}
	return false
}

// findGameWindow decides which window is the game's.
//
// Ordered by how much the strategy can be trusted:
//
//  1. The process ID. Exact, and the only one that cannot match another program.
//  2. The window class, normalised. Needed because a window's PID under
//     Proton/XWayland is not guaranteed to be the process /proc found.
//
// Both are filtered by title first, and the process id needs it as much as the class
// does: the launcher is not another program, it is the same one, so its window carries
// the game's pid AND the game's class. Nothing else separates them - it is the title
// or it is a size heuristic, and a settings box is not always 464x406.
//
// There is deliberately NO fall back to matching the window title against
// "Dead Frontier". A browser tab on the game's site would match it, and the
// bridge userscript means such a tab is usually open - so the one heuristic that
// looks most convenient is the one guaranteed to pick the wrong window. Excluding
// titles is safe in a way requiring one is not: the worst case is that df-hud looks
// for a window it cannot find, which it already handles by failing open.
func findGameWindow(windows []hyprWindow, monitors []hyprMonitor, pid int, match windowMatch) windowPlacement {
	byID := make(map[int]hyprMonitor, len(monitors))
	for _, m := range monitors {
		byID[m.ID] = m
	}

	place := func(w hyprWindow, how string) windowPlacement {
		p := windowPlacement{
			Known:         true,
			Class:         w.Class,
			Title:         w.Title,
			Workspace:     w.Workspace.ID,
			WorkspaceName: w.Workspace.Name,
			MatchedBy:     how,
		}
		if m, ok := byID[w.Monitor]; ok {
			p.Monitor = m.Name
			// A special workspace is identified by a negative ID and is tracked
			// separately by the compositor, so which field to compare against
			// depends on which kind of workspace the window is on.
			if w.Workspace.ID < 0 {
				p.OnActiveWorkspace = m.SpecialWorkspace.ID == w.Workspace.ID
			} else {
				p.OnActiveWorkspace = m.ActiveWorkspace.ID == w.Workspace.ID
			}
		}
		return p
	}

	// Whether anything looked like the game but was ruled out by its title. Tracked
	// rather than discarded: it is the difference between "no window yet" and "the
	// launcher is up", and the caller treats those oppositely.
	launcher := false
	want := normaliseClass(match.Class)

	if pid > 0 {
		for _, w := range windows {
			if w.PID != pid {
				continue
			}
			if match.ignored(w.Title) {
				launcher = true
				continue
			}
			return place(w, "process id")
		}
	}
	if want != "" {
		for _, w := range windows {
			if normaliseClass(w.Class) != want && normaliseClass(w.InitialClass) != want {
				continue
			}
			if match.ignored(w.Title) {
				launcher = true
				continue
			}
			return place(w, "window class")
		}
	}
	return windowPlacement{LauncherOnly: launcher}
}

// normaliseClass makes "DeadFrontier.exe" and "deadfrontier.exe" the same thing,
// because Wine lowercases the class it reports and the config carries the
// executable's real name.
func normaliseClass(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ".exe")
}

// hyprClient queries the live compositor. The two decode steps are separate from
// findGameWindow so the matching logic can be tested against canned JSON with no
// compositor involved.
type hyprClient struct{}

func (hyprClient) GameWindow(ctx context.Context, pid int, match windowMatch) (windowPlacement, error) {
	clients, err := hyprRequest(ctx, "j/clients")
	if err != nil {
		return windowPlacement{}, err
	}
	monitorsRaw, err := hyprRequest(ctx, "j/monitors")
	if err != nil {
		return windowPlacement{}, err
	}
	var windows []hyprWindow
	if err := json.Unmarshal(clients, &windows); err != nil {
		return windowPlacement{}, fmt.Errorf("hyprland: could not read the window list: %w", err)
	}
	var monitors []hyprMonitor
	if err := json.Unmarshal(monitorsRaw, &monitors); err != nil {
		return windowPlacement{}, fmt.Errorf("hyprland: could not read the monitor list: %w", err)
	}
	return findGameWindow(windows, monitors, pid, match), nil
}

// pointerAt is where the cursor is, in coordinates relative to the game's window.
//
// Relative rather than absolute on purpose: hyprctl reports the cursor in
// compositor space, which is offset by the monitor's position, so a region
// measured on one screen would be wrong on another. Subtracting the window's own
// origin makes a configured rectangle mean the same thing wherever the game is.
//
// ok is false when there is no focused window, when it is not the game, or when
// the compositor cannot be reached - all of which mean "do not act".
func (hyprClient) PointerInWindow(ctx context.Context, match windowMatch) (x, y int, ok bool) {
	rawWindow, err := hyprRequest(ctx, "j/activewindow")
	if err != nil {
		return 0, 0, false
	}
	var window struct {
		Class string `json:"class"`
		Title string `json:"title"`
		At    []int  `json:"at"`
	}
	if err := json.Unmarshal(rawWindow, &window); err != nil || len(window.At) < 2 {
		return 0, 0, false
	}
	// The launcher carries the game's class, and a click in a settings dialog is not
	// a click on the Start button.
	if match.ignored(window.Title) {
		return 0, 0, false
	}
	if want := normaliseClass(match.Class); want != "" && normaliseClass(window.Class) != want {
		return 0, 0, false
	}

	rawCursor, err := hyprRequest(ctx, "j/cursorpos")
	if err != nil {
		return 0, 0, false
	}
	var cursor struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if err := json.Unmarshal(rawCursor, &cursor); err != nil {
		return 0, 0, false
	}
	return cursor.X - window.At[0], cursor.Y - window.At[1], true
}

// Windows lists every window, for the -check-game diagnostic. When the game's
// window cannot be matched, seeing the actual classes is the whole answer.
func (hyprClient) Windows(ctx context.Context) ([]hyprWindow, error) {
	raw, err := hyprRequest(ctx, "j/clients")
	if err != nil {
		return nil, err
	}
	var windows []hyprWindow
	if err := json.Unmarshal(raw, &windows); err != nil {
		return nil, err
	}
	return windows, nil
}
