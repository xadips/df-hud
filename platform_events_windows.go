//go:build windows

package main

import (
	"context"
	"time"

	"golang.org/x/sys/windows"
)

// watchPlatformEvents tracks foreground changes. The process and visibility
// watchers retain their periodic scans as the backstop for window creation and
// destruction, while this makes alt-tab visibility react promptly.
func watchPlatformEvents(ctx context.Context, gameChanged, placementChanged func()) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	foreground := windows.GetForegroundWindow()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			next := windows.GetForegroundWindow()
			if next == foreground {
				continue
			}
			foreground = next
			gameChanged()
			placementChanged()
		}
	}
}
