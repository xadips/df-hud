//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const uiCrashMarkerName = "hud-ui.running"

func windowsRuntimeDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "df-hud"), nil
}

func configurePlatformLogging() func() {
	dir, err := windowsRuntimeDir()
	if err != nil {
		return func() {}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}
	path := filepath.Join(dir, "df-hud.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return func() {}
	}
	// File first: a windowsgui executable may have an invalid stderr handle, and
	// MultiWriter stops at the first failed destination.
	log.SetOutput(io.MultiWriter(file, os.Stderr))
	log.Printf("log: %s", path)
	return func() { _ = file.Close() }
}

func beginPlatformUILoop() func() {
	dir, err := windowsRuntimeDir()
	if err != nil {
		log.Printf("hud: could not locate the UI crash marker directory: %v", err)
		return func() {}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("hud: could not create the UI crash marker directory: %v", err)
		return func() {}
	}

	path := filepath.Join(dir, uiCrashMarkerName)
	if previous, err := os.ReadFile(path); err == nil {
		detail := strings.TrimSpace(string(previous))
		if detail == "" {
			detail = "no startup details recorded"
		}
		log.Printf("hud: the previous Windows UI launch ended abnormally; "+
			"its startup marker remained (%s)", detail)
	} else if !os.IsNotExist(err) {
		log.Printf("hud: could not inspect the previous UI crash marker: %v", err)
	}

	detail := fmt.Sprintf("started=%s pid=%d version=%s",
		time.Now().Format(time.RFC3339), os.Getpid(), version)
	if err := os.WriteFile(path, []byte(detail+"\n"), 0o600); err != nil {
		log.Printf("hud: could not write the UI crash marker: %v", err)
		return func() {}
	}

	return func() {
		current, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(current)) != detail {
			log.Print("hud: leaving a UI crash marker owned by another launch")
			return
		}
		if err != nil && !os.IsNotExist(err) {
			log.Printf("hud: could not verify the UI crash marker before clearing it: %v", err)
			return
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("hud: could not clear the UI crash marker: %v", err)
		}
	}
}

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func watchReloadSignal(ctx context.Context, _ func()) {
	<-ctx.Done()
}
