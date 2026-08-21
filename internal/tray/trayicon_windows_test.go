//go:build windows

package tray

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWindowsTrayIconUsesICOContainer(t *testing.T) {
	data := trayIconBytes(trayIconActive, trayIconSize)
	if len(data) < 30 {
		t.Fatalf("icon is only %d bytes", len(data))
	}
	if got := binary.LittleEndian.Uint16(data[2:4]); got != 1 {
		t.Fatalf("ICO type = %d, want 1", got)
	}
	offset := binary.LittleEndian.Uint32(data[18:22])
	if int(offset)+8 > len(data) || !bytes.Equal(data[offset:offset+8], []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("ICO entry does not contain the rendered PNG")
	}
}
