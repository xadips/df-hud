//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsPresenceNamedPipeRoundTrip(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\df-hud-presence-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := listenPresenceEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	client := &windowsPipeConn{handle: handle, path: path}
	defer client.Close()

	var server net.Conn
	select {
	case server = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out accepting named-pipe client")
	}
	defer server.Close()

	if err := writePresenceFrame(client, presenceOpPing, map[string]string{"ping": "ok"}); err != nil {
		t.Fatal(err)
	}
	op, body, err := readPresenceFrame(server)
	if err != nil {
		t.Fatal(err)
	}
	if op != presenceOpPing || string(body) != `{"ping":"ok"}` {
		t.Fatalf("round trip = op %d, body %s", op, body)
	}
}
