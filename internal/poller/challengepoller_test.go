package poller

import (
	"df-hud/internal/config"
	internalcreds "df-hud/internal/creds"
	"df-hud/internal/game"
	"df-hud/internal/model"
	"df-hud/internal/rategate"
	"strings"
	"testing"
	"time"
)

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func testChallengePoller(t *testing.T, level int) (*ChallengePoller, *internalcreds.Store, *Config) {
	t.Helper()
	cfg := config.Default()
	cfg.Poll.OnlyWhenGameRunning = false

	store := internalcreds.NewStore("")
	if _, err := store.Set(internalcreds.Credentials{
		UserID: "1", Password: "2", SC: "3", Cookie: "DeadFrontierFairview=x",
	}, "y27bigaOAA1"); err != nil {
		t.Fatal(err)
	}
	game := game.NewWatcher("DeadFrontier.exe", time.Hour)
	p := NewChallenge(&Client{}, store, game, rategate.New(time.Millisecond),
		func() *Config { return cfg }, func() (int, bool) { return level, false })
	return p, store, cfg
}

// Each precondition unique to this endpoint has to name itself, because "no
// challenges" alone would never point anyone at a one-minute fix.
func TestChallengePollerPauseReasons(t *testing.T) {
	p, creds, cfg := testChallengePoller(t, 415)
	if reason := p.pauseReason(); reason != "" {
		t.Fatalf("a complete setup should poll, got %q", reason)
	}

	// No salt: hashed calls cannot be signed. It needs a store that never had
	// one, because credStore.Set deliberately will NOT erase a known salt when a
	// payload arrives without it - a the bridge userscript payload should not wipe a salt
	// the bridge reported.
	noSalt := internalcreds.NewStore("")
	if _, err := noSalt.Set(internalcreds.Credentials{UserID: "1", Password: "2", SC: "3", Cookie: "c"}, ""); err != nil {
		t.Fatal(err)
	}
	saltless := NewChallenge(&Client{}, noSalt, p.game, p.gate,
		func() *Config { return cfg }, func() (int, bool) { return 415, false })
	cfg.DF.SKeyGen = ""
	if reason := saltless.pauseReason(); !contains(reason, "salt") {
		t.Errorf("reason = %q, want it to name the salt", reason)
	}
	_ = creds

	// No cookie: the board redirects to the front page without one.
	p2, creds2, _ := testChallengePoller(t, 415)
	if _, err := creds2.Set(internalcreds.Credentials{UserID: "1", Password: "2", SC: "3"}, "salt"); err != nil {
		t.Fatal(err)
	}
	if reason := p2.pauseReason(); !contains(reason, "cookie") {
		t.Errorf("reason = %q, want it to name the cookie", reason)
	}

	// Widget off: no reason to spend a request at all.
	p3, _, cfg3 := testChallengePoller(t, 415)
	cfg3.Widget.Challenges.Enabled = false
	if reason := p3.pauseReason(); !contains(reason, "disabled") {
		t.Errorf("reason = %q, want it to say the widget is disabled", reason)
	}
}

// The board must not be parsed before the level is known: the level drives the
// game's own eligibility filter, so a board built at level 0 hides challenges
// that genuinely apply and leaves reward XP unscaled. Subtly wrong beats late is
// the wrong trade.
func TestChallengePollerWaitsForTheLevel(t *testing.T) {
	p, _, _ := testChallengePoller(t, 0)
	reason := p.pauseReason()
	if reason == "" {
		t.Fatal("with no player record yet the board must wait")
	}
	if !contains(reason, "level") {
		t.Errorf("reason = %q, want it to explain that the level is needed", reason)
	}

	// Once a level is known it proceeds.
	ready, _, _ := testChallengePoller(t, 415)
	if reason := ready.pauseReason(); reason != "" {
		t.Errorf("with a level known it should poll, got %q", reason)
	}
}

// A rejected credential stops the board rather than retrying, and a fresh bridge
// payload recovers it - the same discipline as the main poller.
func TestChallengePollerStaleStopAndResume(t *testing.T) {
	p, _, _ := testChallengePoller(t, 415)

	p.mu.Lock()
	p.stale = true
	p.mu.Unlock()
	if reason := p.pauseReason(); !contains(reason, "rejected") {
		t.Errorf("reason = %q, want the stale-credential stop", reason)
	}

	p.Resume()
	if reason := p.pauseReason(); reason != "" {
		t.Errorf("Resume should clear the stop, got %q", reason)
	}
	if failures, stale, _ := p.Status(); stale || failures != 0 {
		t.Errorf("Status after Resume = failures %d stale %v", failures, stale)
	}
}

func TestChallengePollerIntervalFollowsGameState(t *testing.T) {
	p, _, cfg := testChallengePoller(t, 415)
	cfg.Poll.ChallengeInterval = config.Duration{Duration: 30 * time.Second}
	cfg.Poll.IdleInterval = config.Duration{Duration: 5 * time.Minute}

	// Game closed: stretched to the idle cadence.
	if got := p.interval(); got != 5*time.Minute {
		t.Errorf("interval with the game closed = %s, want the 5m idle value", got)
	}
	p.game.SetStateForTesting(model.GameState{Running: true, PID: 1, StartedAt: time.Now()})
	if got := p.interval(); got != 30*time.Second {
		t.Errorf("interval while playing = %s, want the configured 30s", got)
	}
}
