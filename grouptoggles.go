package main

import "sync"

// Per-group show/hide, at runtime, from a keybind or the tray.
//
// Separate from the config's `enabled` on purpose. Those two answer different
// questions: enabled is "do I ever want this group", which belongs in a file and
// survives restarts, while this is "get the board off my screen for a minute" -
// something you do mid-fight and undo thirty seconds later. Writing the config
// file for the second one would mean an editor open during a run, and it would
// persist a decision that was never meant to last.
//
// It is deliberately NOT persisted for the same reason. A HUD that starts with a
// group missing, because of a keypress in a previous session, is indistinguishable
// from a broken one.
type groupToggles struct {
	mu     sync.RWMutex
	hidden map[string]bool
	// onChange is called after any change, so a keypress redraws immediately
	// rather than waiting for the next tick.
	onChange func()
}

func newGroupToggles() *groupToggles {
	t := &groupToggles{hidden: map[string]bool{}}
	for _, name := range hiddenAtStart() {
		t.hidden[name] = true
	}
	return t
}

// toggleableGroups is every group a key may hide.
//
// The status banner is absent on purpose: it is how df-hud reports that it cannot
// do its job, and a hidden one leaves the HUD looking simply broken. It is also
// invisible unless something is wrong, so there is nothing to hide.
func toggleableGroups() []string {
	return []string{"block", "bosses", "session", "xp", "challenges", "map"}
}

// hiddenAtStart is the groups that begin hidden even though they are enabled.
//
// Only the map, and it is the exception that proves the rule about not persisting
// these: the map is a thousand pixels of city, which is a thing you summon to decide where
// to walk and dismiss ten seconds later. Permanently over the game it would not be a
// HUD, it would be a wall. Every other group is small enough to earn its place by
// default, so `enabled` is the only question they need.
func hiddenAtStart() []string { return []string{"map"} }

func knownGroup(name string) bool {
	for _, g := range toggleableGroups() {
		if g == name {
			return true
		}
	}
	return false
}

func (t *groupToggles) SetOnChange(fn func()) {
	t.mu.Lock()
	t.onChange = fn
	t.mu.Unlock()
}

func (t *groupToggles) Hidden(name string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hidden[name]
}

// Toggle flips one group and reports its new hidden state.
func (t *groupToggles) Toggle(name string) bool {
	t.mu.Lock()
	t.hidden[name] = !t.hidden[name]
	now, fn := t.hidden[name], t.onChange
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
	return now
}

func (t *groupToggles) Set(name string, hidden bool) {
	t.mu.Lock()
	changed := t.hidden[name] != hidden
	t.hidden[name] = hidden
	fn := t.onChange
	t.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}
