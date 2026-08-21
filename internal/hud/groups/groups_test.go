package groups

import "testing"

func TestGroups(t *testing.T) {
	g := New()
	changes := 0
	g.SetOnChange(func() { changes++ })
	if !g.Hidden("map") {
		t.Error("map should start hidden")
	}
	if g.Hidden("challenges") || !g.Toggle("challenges") || !g.Hidden("challenges") {
		t.Error("first toggle should hide challenges")
	}
	if g.Toggle("challenges") {
		t.Error("second toggle should show challenges")
	}
	g.Set("challenges", false)
	if changes != 2 {
		t.Errorf("onChange fired %d times", changes)
	}
}

func TestKnownAndToggleable(t *testing.T) {
	for _, name := range Toggleable() {
		if !Known(name) {
			t.Errorf("%q is listed but unknown", name)
		}
	}
	if Known("challenge") || Known("status") {
		t.Error("unknown or status group accepted")
	}
}

func TestToggleableReturnsCopy(t *testing.T) {
	names := Toggleable()
	names[0] = "changed"
	if Known("changed") || !Known("block") {
		t.Error("caller mutated the package group list")
	}
}
