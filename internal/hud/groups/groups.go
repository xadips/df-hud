// Package groups manages runtime show/hide state for HUD widget groups.
package groups

import "sync"

var toggleable = [...]string{"block", "bosses", "session", "xp", "challenges", "map"}

// Groups is the concurrency-safe runtime visibility state.
type Groups struct {
	mu       sync.RWMutex
	hidden   map[string]bool
	onChange func()
}

// New creates the default group state.
func New() *Groups {
	g := &Groups{hidden: map[string]bool{}}
	for _, name := range HiddenAtStart() {
		g.hidden[name] = true
	}
	return g
}

// Toggleable returns every group a key may hide.
func Toggleable() []string {
	return append([]string(nil), toggleable[:]...)
}

// HiddenAtStart returns groups enabled but initially hidden.
func HiddenAtStart() []string { return []string{"map"} }

// Known reports whether name is a toggleable group.
func Known(name string) bool {
	for _, group := range toggleable {
		if group == name {
			return true
		}
	}
	return false
}

// SetOnChange registers the callback invoked after a visibility change.
func (g *Groups) SetOnChange(fn func()) {
	g.mu.Lock()
	g.onChange = fn
	g.mu.Unlock()
}

// Hidden reports the current runtime visibility state.
func (g *Groups) Hidden(name string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.hidden[name]
}

// Toggle flips one group and reports its new hidden state.
func (g *Groups) Toggle(name string) bool {
	g.mu.Lock()
	g.hidden[name] = !g.hidden[name]
	now, fn := g.hidden[name], g.onChange
	g.mu.Unlock()
	if fn != nil {
		fn()
	}
	return now
}

// Set changes one group's hidden state.
func (g *Groups) Set(name string, hidden bool) {
	g.mu.Lock()
	changed := g.hidden[name] != hidden
	g.hidden[name] = hidden
	fn := g.onChange
	g.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}
