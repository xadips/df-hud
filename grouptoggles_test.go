package main

import "testing"

// The runtime toggle is deliberately not the config's `enabled`: this is "get the
// board off my screen for a minute", not "I never want this group".
func TestGroupToggles(t *testing.T) {
	g := newGroupToggles()
	changes := 0
	g.SetOnChange(func() { changes++ })

	if g.Hidden("challenges") {
		t.Error("nothing is hidden until something hides it")
	}
	if !g.Toggle("challenges") {
		t.Error("the first toggle should hide it")
	}
	if !g.Hidden("challenges") {
		t.Error("Hidden should agree with what Toggle returned")
	}
	// Groups are independent: hiding the board must not touch the boss list.
	if g.Hidden("bosses") {
		t.Error("toggling one group changed another")
	}
	if g.Toggle("challenges") {
		t.Error("the second toggle should show it again")
	}

	// Every change redraws, because a group that takes a second to disappear reads
	// as the key not having worked.
	if changes != 2 {
		t.Errorf("onChange fired %d times, want one per change", changes)
	}
	// Set to the value it already has does not.
	g.Set("challenges", false)
	if changes != 2 {
		t.Errorf("onChange fired %d times; setting an unchanged value must not redraw", changes)
	}
}

// A typo'd group name in somebody's compositor config must be an error rather than
// a key that silently does nothing.
func TestKnownGroup(t *testing.T) {
	for _, name := range toggleableGroups() {
		if !knownGroup(name) {
			t.Errorf("%q is listed as toggleable but not recognised", name)
		}
	}
	if knownGroup("challenge") {
		t.Error("a near-miss name must not be accepted")
	}
	// The status banner is how df-hud reports that it cannot do its job, so it is
	// deliberately not hideable.
	if knownGroup("status") {
		t.Error("the status banner must not be hideable")
	}
}
