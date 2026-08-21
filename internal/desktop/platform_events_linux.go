//go:build linux

package desktop

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"strings"
	"time"
)

var hyprWindowEvents = map[string]bool{
	"openwindow":  true,
	"closewindow": true,
	"fullscreen":  true,
}

var hyprPlacementEvents = map[string]bool{
	"workspace":       true,
	"workspacev2":     true,
	"focusedmon":      true,
	"focusedmonv2":    true,
	"movewindow":      true,
	"movewindowv2":    true,
	"moveworkspace":   true,
	"moveworkspacev2": true,
	"activespecial":   true,
	"monitoradded":    true,
	"monitorremoved":  true,
	"openwindow":      true,
	"closewindow":     true,
	"fullscreen":      true,
}

// watchPlatformEvents uses Hyprland events only as low-latency hints. Process
// scans and fresh placement queries remain authoritative.
func watchPlatformEvents(ctx context.Context, gameChanged, placementChanged func()) {
	path, err := hyprSocketPath(".socket2.sock")
	if err != nil {
		log.Printf("game: no Hyprland event stream (%v); falling back to periodic scans", err)
		return
	}

	onEvent := func(name string) {
		if hyprWindowEvents[name] {
			gameChanged()
		}
		if hyprPlacementEvents[name] {
			placementChanged()
		}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		if err := streamHyprEvents(ctx, path, onEvent); err != nil && ctx.Err() == nil {
			log.Printf("game: Hyprland event stream ended (%v); retrying in %s", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// WatchEvents streams desktop hints until ctx ends.
func WatchEvents(ctx context.Context, gameChanged, placementChanged func()) {
	watchPlatformEvents(ctx, gameChanged, placementChanged)
}

func streamHyprEvents(ctx context.Context, path string, onEvent func(name string)) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		name, _, ok := strings.Cut(scanner.Text(), ">>")
		if ok {
			onEvent(name)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("socket closed")
}
