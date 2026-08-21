//go:build windows

package game

import (
	"testing"
	"time"
)

func TestOldestMatchingWindowsProcess(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	processes := []windowsProcess{
		{pid: 10, exeName: "explorer.exe", startedAt: base},
		{pid: 20, exeName: "DeadFrontier.exe", startedAt: base.Add(2 * time.Minute)},
		{pid: 21, exeName: "deadfrontier.EXE", startedAt: base.Add(time.Minute)},
		{pid: 22, exeName: "DeadFrontier.exe"}, // inaccessible start time
	}
	got := oldestMatchingWindowsProcess(processes, "DeadFrontier.exe", 999)
	if got.pid != 21 {
		t.Fatalf("pid = %d, want oldest matching pid 21", got.pid)
	}
}

func TestOldestMatchingWindowsProcessSkipsSelf(t *testing.T) {
	now := time.Now()
	processes := []windowsProcess{{pid: 42, exeName: "df-hud.exe", startedAt: now}}
	if got := oldestMatchingWindowsProcess(processes, "df-hud.exe", 42); got.pid != 0 {
		t.Fatalf("selected self: %+v", got)
	}
}
