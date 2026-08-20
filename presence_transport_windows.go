//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const windowsPresencePipe = `\\.\pipe\discord-ipc-0`

func defaultPresenceSocket() string {
	return windowsPresencePipe
}

func listenPresenceEndpoint(path string) (net.Listener, error) {
	if !strings.HasPrefix(strings.ToLower(path), `\\.\pipe\`) {
		return nil, fmt.Errorf("Windows presence endpoint %q must start with \\\\.\\pipe\\", path)
	}
	first, err := createPresencePipe(path, true)
	if err != nil {
		return nil, fmt.Errorf("%s is already served or cannot be created: %w", path, err)
	}
	return &windowsPipeListener{path: path, pending: first}, nil
}

func cleanupPresenceEndpoint(string) {}

func createPresencePipe(path string, first bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	openMode := uint32(windows.PIPE_ACCESS_DUPLEX)
	if first {
		openMode |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(
		name,
		openMode,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES,
		presenceMaxFrame+8,
		presenceMaxFrame+8,
		0,
		nil,
	)
}

type windowsPipeListener struct {
	path string

	mu        sync.Mutex
	pending   windows.Handle
	accepting bool
	closed    bool
	acceptErr error
}

func (l *windowsPipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, net.ErrClosed
	}
	if l.acceptErr != nil {
		err := l.acceptErr
		l.mu.Unlock()
		return nil, err
	}
	handle := l.pending
	l.accepting = true
	l.mu.Unlock()

	err := windows.ConnectNamedPipe(handle, nil)
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		l.mu.Lock()
		l.accepting = false
		closed := l.closed
		l.mu.Unlock()
		windows.CloseHandle(handle)
		if closed {
			return nil, net.ErrClosed
		}
		return nil, err
	}

	l.mu.Lock()
	l.accepting = false
	if l.closed {
		l.pending = 0
		l.mu.Unlock()
		windows.CloseHandle(handle)
		return nil, net.ErrClosed
	}
	next, nextErr := createPresencePipe(l.path, false)
	if nextErr == nil {
		l.pending = next
	} else {
		l.pending = 0
	}
	l.acceptErr = nextErr
	l.mu.Unlock()
	return &windowsPipeConn{handle: handle, path: l.path}, nil
}

func (l *windowsPipeListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	handle := l.pending
	accepting := l.accepting
	if !accepting {
		l.pending = 0
	}
	l.mu.Unlock()

	if !accepting {
		if handle == 0 {
			return nil
		}
		return windows.CloseHandle(handle)
	}

	// Connecting a short-lived client releases a blocking ConnectNamedPipe.
	name, err := windows.UTF16PtrFromString(l.path)
	if err != nil {
		return err
	}
	wake, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_PIPE_BUSY) {
			// The accept completed between the state check and this wake-up.
			return nil
		}
		return err
	}
	return windows.CloseHandle(wake)
}

func (l *windowsPipeListener) Addr() net.Addr {
	return windowsPipeAddr(l.path)
}

type windowsPipeConn struct {
	mu     sync.Mutex
	handle windows.Handle
	path   string
}

func (c *windowsPipeConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	handle := c.handle
	c.mu.Unlock()
	if handle == 0 {
		return 0, net.ErrClosed
	}
	var done uint32
	err := windows.ReadFile(handle, p, &done, nil)
	if done > 0 {
		return int(done), nil
	}
	return 0, err
}

func (c *windowsPipeConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	handle := c.handle
	c.mu.Unlock()
	if handle == 0 {
		return 0, net.ErrClosed
	}
	var done uint32
	err := windows.WriteFile(handle, p, &done, nil)
	return int(done), err
}

func (c *windowsPipeConn) Close() error {
	c.mu.Lock()
	handle := c.handle
	c.handle = 0
	c.mu.Unlock()
	if handle == 0 {
		return nil
	}
	return windows.CloseHandle(handle)
}

func (c *windowsPipeConn) LocalAddr() net.Addr  { return windowsPipeAddr(c.path) }
func (c *windowsPipeConn) RemoteAddr() net.Addr { return windowsPipeAddr(c.path) }
func (*windowsPipeConn) SetDeadline(time.Time) error {
	return errors.New("Windows named pipes do not support deadlines")
}
func (*windowsPipeConn) SetReadDeadline(time.Time) error {
	return errors.New("Windows named pipes do not support deadlines")
}
func (*windowsPipeConn) SetWriteDeadline(time.Time) error {
	return errors.New("Windows named pipes do not support deadlines")
}

type windowsPipeAddr string

func (windowsPipeAddr) Network() string  { return "npipe" }
func (a windowsPipeAddr) String() string { return string(a) }
