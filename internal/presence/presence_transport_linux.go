//go:build linux

package presence

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// defaultPresenceSocket is where Discord's own client listens on Linux.
func defaultPresenceSocket() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "discord-ipc-0")
}

// DefaultSocket returns the platform's standard Discord IPC endpoint.
func DefaultSocket() string { return defaultPresenceSocket() }

// listenPresenceEndpoint binds the Unix socket, clearing one left by a crash.
// A live peer owns its socket and is never displaced.
func listenPresenceEndpoint(path string) (net.Listener, error) {
	if _, err := os.Stat(path); err == nil {
		conn, dialErr := net.DialTimeout("unix", path, 300*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("%s is already served by something else", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing the stale socket at %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}

func cleanupPresenceEndpoint(path string) {
	_ = os.Remove(path)
}
