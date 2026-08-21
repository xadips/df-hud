package hotkeys

import "context"

// Config is the platform-neutral subset needed by the Windows registrar.
type Config struct {
	Enabled bool

	Map        string
	Challenges string
	RunStart   string
	XPReset    string
	Overlay    string
}

// Actions are deliberately the same corrections and temporary visibility
// switches exposed by the loopback bridge and tray.
type Actions struct {
	ToggleMap        func()
	ToggleChallenges func()
	StartRun         func()
	ResetXP          func()
	ToggleOverlay    func()
}

// Hotkeys owns the platform registration loop.
type Hotkeys struct {
	config  func() Config
	focused func() bool
	actions Actions
}

func New(config func() Config, focused func() bool, actions Actions) *Hotkeys {
	return &Hotkeys{config: config, focused: focused, actions: actions}
}

type registration struct {
	id      int
	name    string
	binding Binding
	action  func()
}

func (h *Hotkeys) registrations(cfg Config) []registration {
	values := []struct {
		id      int
		name    string
		binding string
		action  func()
	}{
		{1, "map", cfg.Map, h.actions.ToggleMap},
		{2, "challenges", cfg.Challenges, h.actions.ToggleChallenges},
		{3, "run clock", cfg.RunStart, h.actions.StartRun},
		{4, "XP rate", cfg.XPReset, h.actions.ResetXP},
		{5, "overlay", cfg.Overlay, h.actions.ToggleOverlay},
	}
	result := make([]registration, 0, len(values))
	for _, value := range values {
		if value.binding == "" || value.action == nil {
			continue
		}
		binding, err := ParseBinding(value.binding)
		if err != nil {
			// Config validation already rejects this. Treat a caller bypassing
			// validation as an unbound action rather than registering a guess.
			continue
		}
		result = append(result, registration{
			id: value.id, name: value.name, binding: binding, action: value.action,
		})
	}
	return result
}

// Run is implemented per platform. Linux's compositor remains the owner of
// global input; Windows uses RegisterHotKey.
func (h *Hotkeys) Run(ctx context.Context) {
	h.run(ctx)
}
