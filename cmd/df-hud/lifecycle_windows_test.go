//go:build windows

package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformUILoopMarkerReportsPreviousCrashAndClearsCleanRun(t *testing.T) {
	t.Setenv("LocalAppData", t.TempDir())

	dir, err := windowsRuntimeDir()
	if err != nil {
		t.Fatalf("windowsRuntimeDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, uiCrashMarkerName)
	if err := os.WriteFile(path, []byte("started=previous pid=123\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	finish := beginPlatformUILoop()
	marker, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after begin: %v", err)
	}
	if got := string(marker); !strings.Contains(got, "pid=") || !strings.Contains(got, "version=") {
		t.Fatalf("new marker lacks launch details: %q", got)
	}
	if got := logs.String(); !strings.Contains(got, "previous Windows UI launch ended abnormally") ||
		!strings.Contains(got, "started=previous pid=123") {
		t.Fatalf("previous crash was not logged: %q", got)
	}

	finish()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clean UI shutdown left marker behind: %v", err)
	}
}

func TestPlatformUILoopCleanupPreservesAnotherLaunchMarker(t *testing.T) {
	t.Setenv("LocalAppData", t.TempDir())

	finish := beginPlatformUILoop()
	dir, err := windowsRuntimeDir()
	if err != nil {
		t.Fatalf("windowsRuntimeDir: %v", err)
	}
	path := filepath.Join(dir, uiCrashMarkerName)
	const replacement = "started=replacement pid=456\n"
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	finish()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cleanup removed another launch marker: %v", err)
	}
	if string(got) != replacement {
		t.Fatalf("another launch marker changed: %q", got)
	}
}
