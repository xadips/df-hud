//go:build linux

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
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
	// Address is the compositor's own handle for the window, "0x" and hex. The
	// only identifier that names ONE window: the game and its launcher share a
	// process and a class, so a pid or a class selector can hit either.
	Address   string `json:"address"`
	PID       int    `json:"pid"`
	Monitor   int    `json:"monitor"`
	Mapped    bool   `json:"mapped"`
	Workspace struct {
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
			Address:       w.Address,
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
	launcherAddr := ""
	// noteLauncher records an ignored window, and keeps the address of the one
	// that is the dialog itself rather than one of its sub-windows.
	noteLauncher := func(w hyprWindow) {
		launcher = true
		if launcherAddr == "" && match.isLauncherDialog(w.Title) {
			launcherAddr = w.Address
		}
	}
	want := normaliseClass(match.Class)

	if pid > 0 {
		for _, w := range windows {
			if w.PID != pid {
				continue
			}
			if match.ignored(w.Title) {
				noteLauncher(w)
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
				noteLauncher(w)
				continue
			}
			return place(w, "window class")
		}
	}
	return windowPlacement{LauncherOnly: launcher, LauncherAddress: launcherAddr}
}

// hyprClient queries the live compositor. The two decode steps are separate from
// findGameWindow so the matching logic can be tested against canned JSON with no
// compositor involved.
type hyprClient struct{}

// NewClient returns the Hyprland desktop integration.
func NewClient() Client { return hyprClient{} }

// CanStartRun reports whether a desktop placement is positive evidence that
// play started. Hyprland placement is not used for this fallback.
func CanStartRun(windowPlacement) bool { return false }

func desktopCanStartRun(place windowPlacement) bool { return CanStartRun(place) }

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

// SendKey presses and releases one key inside a specific window.
//
// Addressed by ADDRESS, not class or pid: the game and its configuration dialogs
// are one process reporting one class, so every other selector can hit either.
// The address comes from findGameWindow, which has already ruled out the
// launcher by title. It also means the key does not follow focus, so nothing is
// stolen from whatever you were doing.
func (hyprClient) SendKey(ctx context.Context, key, address string) error {
	if !hyprKeyName.MatchString(key) {
		// Refused rather than escaped: the command is Lua source, so a value
		// needing an escape could close the string and run something.
		return fmt.Errorf("hyprland: %q is not a plain key name", key)
	}
	if !hyprAddress.MatchString(address) {
		return fmt.Errorf("hyprland: %q is not a window address", address)
	}
	cmd := fmt.Sprintf("dispatch hl.dsp.send_shortcut{mods=%q,key=%q,window=%q}", "", key, "address:"+address)
	reply, err := hyprRequest(ctx, cmd)
	if err != nil {
		return err
	}
	// "ok" on success; everything else, including a window that vanished between
	// the lookup and the send, comes back as a warning in the body.
	if text := strings.TrimSpace(string(reply)); !strings.HasPrefix(text, "ok") {
		return fmt.Errorf("hyprland: %s", firstLine(text))
	}
	return nil
}

// The two values interpolated into Lua source.
var hyprAddress = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)

// firstLine keeps a compositor error to one log line: Hyprland appends a note
// about Lua syntax to every dispatch reply.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// ActiveAddress is the focused window's address, false when nothing is focused
// or the compositor cannot be reached. Asked only when a key is about to be
// sent, never on a timer.
func (hyprClient) ActiveAddress(ctx context.Context) (string, bool) {
	raw, err := hyprRequest(ctx, "j/activewindow")
	if err != nil {
		return "", false
	}
	var window struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(raw, &window); err != nil || window.Address == "" {
		return "", false
	}
	return window.Address, true
}

// Windows lists every window, for the -check-game diagnostic. When the game's
// window cannot be matched, seeing the actual classes is the whole answer.
func (hyprClient) Windows(ctx context.Context) ([]desktopWindow, error) {
	raw, err := hyprRequest(ctx, "j/clients")
	if err != nil {
		return nil, err
	}
	var windows []hyprWindow
	if err := json.Unmarshal(raw, &windows); err != nil {
		return nil, err
	}
	out := make([]desktopWindow, 0, len(windows))
	for _, window := range windows {
		out = append(out, desktopWindow{Class: window.Class, Title: window.Title, PID: window.PID})
	}
	return out, nil
}
