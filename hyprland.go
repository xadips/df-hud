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
func hyprSocketPath(name string) (string, error) {
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	if sig == "" {
		return "", errors.New("HYPRLAND_INSTANCE_SIGNATURE is unset (not running under Hyprland)")
	}
	var candidates []string
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		candidates = append(candidates, filepath.Join(runtimeDir, "hypr", sig, name))
	}
	candidates = append(candidates, filepath.Join("/tmp", "hypr", sig, name))
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Hyprland socket %s (looked in %s)", name, strings.Join(candidates, ", "))
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
}

// findGameWindow decides which window is the game's.
//
// Ordered by how much the strategy can be trusted:
//
//  1. The process ID. Exact, and the only one that cannot match something else.
//  2. The window class, normalised. Needed because a window's PID under
//     Proton/XWayland is not guaranteed to be the process /proc found.
//
// There is deliberately NO fall back to matching the window title against
// "Dead Frontier". A browser tab on the game's site would match it, and the
// bridge userscript means such a tab is usually open - so the one heuristic that
// looks most convenient is the one guaranteed to pick the wrong window.
func findGameWindow(windows []hyprWindow, monitors []hyprMonitor, pid int, class string) windowPlacement {
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

	if pid > 0 {
		for _, w := range windows {
			if w.PID == pid {
				return place(w, "process id")
			}
		}
	}
	if want := normaliseClass(class); want != "" {
		for _, w := range windows {
			if normaliseClass(w.Class) == want || normaliseClass(w.InitialClass) == want {
				return place(w, "window class")
			}
		}
	}
	return windowPlacement{}
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

func (hyprClient) GameWindow(ctx context.Context, pid int, class string) (windowPlacement, error) {
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
	return findGameWindow(windows, monitors, pid, class), nil
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
func (hyprClient) PointerInWindow(ctx context.Context, class string) (x, y int, ok bool) {
	rawWindow, err := hyprRequest(ctx, "j/activewindow")
	if err != nil {
		return 0, 0, false
	}
	var window struct {
		Class string `json:"class"`
		At    []int  `json:"at"`
	}
	if err := json.Unmarshal(rawWindow, &window); err != nil || len(window.At) < 2 {
		return 0, 0, false
	}
	if want := normaliseClass(class); want != "" && normaliseClass(window.Class) != want {
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
