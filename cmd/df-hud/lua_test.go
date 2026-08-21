package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// contrib/df-hud.lua is the one part of df-hud that Go cannot exercise: it runs
// inside the compositor, against Hyprland's own API. A mistake in it is invisible
// until a config reload, and the symptom - "I pressed Start and nothing happened" -
// looks exactly like a bug in the Go half.
//
// So the snippet is loaded against a stubbed hl by contrib/df-hud_spec.lua, and
// that is run from here to keep it in the same `go test` everything else is in.
// Skipped rather than failed when no interpreter is installed, because a Lua
// runtime is not a build dependency of df-hud - the compositor supplies it.
func TestHyprlandLuaSnippet(t *testing.T) {
	lua := ""
	for _, name := range []string{"lua", "lua5.4", "luajit"} {
		if path, err := exec.LookPath(name); err == nil {
			lua = path
			break
		}
	}
	if lua == "" {
		t.Skip("no lua interpreter installed; skipping the Hyprland snippet check")
	}

	spec := filepath.Join("..", "..", "contrib", "df-hud_spec.lua")
	snippet := filepath.Join("..", "..", "contrib", "df-hud.lua")
	out, err := exec.Command(lua, spec, snippet).CombinedOutput()
	if err != nil {
		t.Fatalf("contrib/df-hud.lua failed its checks: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "all checks passed") {
		t.Errorf("unexpected output from the snippet check:\n%s", out)
	}
}
