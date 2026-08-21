package poller

import (
	"context"
	"df-hud/internal/config"
	"df-hud/internal/creds"
	"df-hud/internal/game"
	"df-hud/internal/model"
	"df-hud/internal/rategate"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDF is a stand-in for the game server: it records every request and lets a
// test choose the reply per call, so pacing and failure handling can be checked
// without touching the real server.
type fakeDF struct {
	mu       sync.Mutex
	requests []time.Time
	bodies   []string
	// reply is consulted per request; return the body to send. Default is a
	// plausible player record.
	reply func(n int) (status int, body string)
	srv   *httptest.Server
}

func newFakeDF(t *testing.T) *fakeDF {
	t.Helper()
	f := &fakeDF{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, time.Now())
		f.bodies = append(f.bodies, string(raw))
		n := len(f.requests)
		reply := f.reply
		f.mu.Unlock()

		status, body := http.StatusOK, playerRecord(415, 8_112_884_433)
		if reply != nil {
			status, body = reply(n)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDF) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeDF) at(i int) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[i]
}

func (f *fakeDF) setReply(fn func(n int) (int, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply = fn
}

// playerRecord is a get_values response in the game's flsh format: leading &,
// no escaping, every value a string.
func playerRecord(level int, exp int64) string {
	return "&df_level=" + itoa(level) + "&df_exp=" + itoa64(exp) +
		"&df_positionx=1100&df_positiony=1100&df_positionz=0" +
		"&df_tradezone=5&df_inoutpost=0&df_dangerlevel=3&df_cash=1000"
}

func itoa(n int) string     { return itoa64(int64(n)) }
func itoa64(n int64) string { return strings.TrimSpace(fmtInt(n)) }
func fmtInt(n int64) string { return string(appendInt(nil, n)) }
func appendInt(b []byte, n int64) []byte {
	if n == 0 {
		return append(b, '0')
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits [24]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		b = append(b, '-')
	}
	return append(b, digits[i:]...)
}

// testPoller wires a poller against the fake server with fast intervals.
func testPoller(t *testing.T, f *fakeDF, tune func(*Config)) (*Poller, *creds.Store, *GameWatcher) {
	t.Helper()
	cfg := config.Default()
	cfg.Poll.ActiveInterval = config.Duration{Duration: 20 * time.Millisecond}
	cfg.Poll.IdleInterval = config.Duration{Duration: 20 * time.Millisecond}
	cfg.Poll.BackoffMax = config.Duration{Duration: 40 * time.Millisecond}
	cfg.Poll.Jitter = 0
	cfg.Poll.OnlyWhenGameRunning = false
	cfg.DF.Timeout = config.Duration{Duration: 2 * time.Second}
	if tune != nil {
		tune(cfg)
	}

	store := creds.NewStore("")
	if _, err := store.Set(creds.Credentials{UserID: "1234567", Password: "hash", SC: "sc"}, "salt"); err != nil {
		t.Fatal(err)
	}
	client := &Client{HTTP: f.srv.Client(), BaseURL: f.srv.URL, UserAgent: "df-hud-test"}
	// Pin the authenticated shape: these tests are about the poller's scheduling
	// and credential handling, and they assert the request body. Which HTTP form
	// get_values uses is dfclient_test.go's business.
	client.DisablePublicGetValues()
	game := game.NewWatcher("DeadFrontier.exe", time.Hour)
	p := New(client, store, game, func() *Config { return cfg })
	// Millisecond intervals with the production 5s floor would make every
	// scheduling test a five-second sleep. Tests that are ABOUT the floor set it
	// back to MinRequestGap explicitly.
	p.minGap = 20 * time.Millisecond
	return p, store, game
}

func TestPollerPollsAndReportsTicks(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, nil)

	ticks := make(chan Tick, 8)
	p.SetOnTick(func(tk Tick) { ticks <- tk })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	select {
	case tk := <-ticks:
		if tk.Err != nil {
			t.Fatalf("first tick failed: %v", tk.Err)
		}
		if tk.Vars["df_level"] != "415" {
			t.Errorf("df_level = %q, want 415", tk.Vars["df_level"])
		}
		if !tk.Scheduled {
			t.Error("a poll from Run must be marked scheduled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no tick arrived")
	}

	// The request must carry the credential triple, unhashed.
	f.mu.Lock()
	body := f.bodies[0]
	f.mu.Unlock()
	if !strings.HasPrefix(body, "userID=1234567&password=hash&sc=sc") {
		t.Errorf("request body = %q, want the unhashed triple in order", body)
	}
	if strings.Contains(body, "hash=") {
		t.Error("get_values is unhashed; no signature should be sent")
	}
}

// TestPollerFirstPollIsImmediate: starting df-hud with the game already running
// should not show an empty HUD for a whole interval.
func TestPollerFirstPollIsImmediate(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, func(c *Config) {
		c.Poll.ActiveInterval = config.Duration{Duration: time.Hour}
		c.Poll.IdleInterval = config.Duration{Duration: time.Hour}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	go p.Run(ctx)
	deadline := time.After(2 * time.Second)
	for f.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("the first poll did not happen promptly")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("first poll took %s, want it immediately", elapsed)
	}
}

// TestPollerNeverBurstsUnderWakeStorm is the politeness guarantee: whatever
// wakes the loop, and however often, requests stay at least MinRequestGap apart.
func TestPollerNeverBurstsUnderWakeStorm(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, func(c *Config) {
		c.Poll.ActiveInterval = config.Duration{Duration: time.Hour} // only wakes can drive polls
		c.Poll.IdleInterval = config.Duration{Duration: time.Hour}
	})
	p.minGap = MinRequestGap // the whole point of this test

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// Wait for the immediate first poll.
	for f.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	// Now hammer Wake as if credentials, the game and the compositor all fired
	// repeatedly - the shape of a real event storm.
	for i := 0; i < 500; i++ {
		p.Wake()
		time.Sleep(time.Millisecond)
	}

	if got := f.count(); got != 1 {
		t.Errorf("500 wakes produced %d requests within half a second; MinRequestGap is %s "+
			"so only the first poll should have happened", got, MinRequestGap)
	}
}

func TestPollerStopsDeadOnRejectedCredentials(t *testing.T) {
	f := newFakeDF(t)
	// The game's own rejection envelope.
	f.setReply(func(n int) (int, string) { return http.StatusOK, "status=value_mismatch" })

	p, store, _ := testPoller(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	for f.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	// Give the loop plenty of time to retry if it were going to. This is the
	// behaviour that protects the account: no retry storm on a bad session.
	time.Sleep(300 * time.Millisecond)
	if got := f.count(); got != 1 {
		t.Errorf("made %d requests after a credential rejection, want exactly 1: "+
			"retrying rejected credentials is what gets you temp banned", got)
	}
	st := p.Status()
	if !st.Stale {
		t.Error("status should record stale credentials")
	}
	if !st.Paused || st.PauseReason == "" {
		t.Errorf("the poller should be paused with a reason, got %+v", st)
	}

	// A fresh bridge payload must recover it without a restart.
	f.setReply(nil)
	if _, err := store.Set(creds.Credentials{UserID: "1234567", Password: "hash2", SC: "sc2"}, "salt"); err != nil {
		t.Fatal(err)
	}
	p.Resume()

	deadline := time.After(3 * time.Second)
	for f.count() < 2 {
		select {
		case <-deadline:
			t.Fatal("the poller did not resume after credentials were refreshed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if st := p.Status(); st.Stale {
		t.Error("stale should be cleared after a refresh")
	}
	// And the recovery poll still honoured the request gap, whatever it is set
	// to: a wake must never bypass it.
	if gap := f.at(1).Sub(f.at(0)); gap < p.minGap {
		t.Errorf("resume polled %s after the rejection, want at least %s", gap, p.minGap)
	}
}

func TestPollerWaitsForCredentials(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, nil)
	// Start with an empty store: this is a first run before the userscript has
	// ever posted.
	empty := creds.NewStore("")
	p.creds = empty

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	time.Sleep(150 * time.Millisecond)
	if got := f.count(); got != 0 {
		t.Errorf("made %d requests with no credentials, want 0", got)
	}
	st := p.Status()
	if !st.Paused || !strings.Contains(st.PauseReason, "bridge") {
		t.Errorf("status = %+v, want paused waiting for the bridge", st)
	}

	// Credentials arrive; the loop must pick them up on a wake.
	if _, err := empty.Set(creds.Credentials{UserID: "1", Password: "2", SC: "3"}, ""); err != nil {
		t.Fatal(err)
	}
	p.Wake()

	deadline := time.After(3 * time.Second)
	for f.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("the poller did not start after credentials arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestPollerRespectsOnlyWhenGameRunning(t *testing.T) {
	f := newFakeDF(t)
	p, _, game := testPoller(t, f, func(c *Config) { c.Poll.OnlyWhenGameRunning = true })

	// The watcher has never scanned, so the game reads as not running.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	time.Sleep(150 * time.Millisecond)
	if got := f.count(); got != 0 {
		t.Errorf("made %d requests with the game closed, want 0", got)
	}
	if st := p.Status(); !strings.Contains(st.PauseReason, "not running") {
		t.Errorf("pause reason = %q, want it to mention the game", st.PauseReason)
	}

	// Simulate the game launching, which is what GameWatcher's callback does.
	game.SetStateForTesting(model.GameState{Running: true, PID: 4242, StartedAt: time.Now().Add(-time.Minute)})
	p.Wake()

	deadline := time.After(3 * time.Second)
	for f.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("the poller did not start when the game launched")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestPollerBacksOffOnFailure(t *testing.T) {
	f := newFakeDF(t)
	f.setReply(func(n int) (int, string) { return http.StatusBadGateway, "nope" })

	p, _, _ := testPoller(t, f, func(c *Config) {
		c.Poll.ActiveInterval = config.Duration{Duration: 20 * time.Millisecond}
		c.Poll.IdleInterval = config.Duration{Duration: 20 * time.Millisecond}
		c.Poll.BackoffMax = config.Duration{Duration: 200 * time.Millisecond}
	})
	ticks := make(chan Tick, 16)
	p.SetOnTick(func(tk Tick) { ticks <- tk })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// Failures are reported as ticks, which is what the XP widget counts as
	// missed samples.
	for i := 0; i < 2; i++ {
		select {
		case tk := <-ticks:
			if tk.Err == nil {
				t.Fatal("expected a failing tick")
			}
			if tk.Vars != nil {
				t.Error("a failed tick must carry no vars")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no failing tick arrived")
		}
	}
	if st := p.Status(); st.Failures < 1 || st.LastError == "" {
		t.Errorf("status = %+v, want recorded failures", st)
	}

	// Backoff must grow, so the gap between later attempts exceeds the base
	// interval rather than hammering at 20ms.
	time.Sleep(400 * time.Millisecond)
	n := f.count()
	if n < 3 {
		t.Fatalf("only %d requests; expected several attempts", n)
	}
	first := f.at(1).Sub(f.at(0))
	last := f.at(n - 1).Sub(f.at(n - 2))
	if last <= first {
		t.Errorf("gap did not grow: first %s, last %s", first, last)
	}
}

func TestPollerRecoversAfterFailure(t *testing.T) {
	f := newFakeDF(t)
	f.setReply(func(n int) (int, string) {
		if n <= 2 {
			return http.StatusInternalServerError, "boom"
		}
		return http.StatusOK, playerRecord(200, 1234)
	})

	p, _, _ := testPoller(t, f, nil)
	ticks := make(chan Tick, 16)
	p.SetOnTick(func(tk Tick) { ticks <- tk })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case tk := <-ticks:
			if tk.Err == nil {
				if tk.Vars["df_level"] != "200" {
					t.Errorf("df_level = %q, want 200", tk.Vars["df_level"])
				}
				if st := p.Status(); st.Failures != 0 {
					t.Errorf("failures = %d after a success, want 0", st.Failures)
				}
				return
			}
		case <-deadline:
			t.Fatal("the poller never recovered")
		}
	}
}

func TestPollerOnceWithoutCredentials(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, nil)
	p.creds = creds.NewStore("")

	tick := p.Once(context.Background())
	if tick.Err == nil {
		t.Fatal("Once must fail without credentials")
	}
	if !strings.Contains(tick.Err.Error(), "bridge") {
		t.Errorf("the error should point at the bridge script, got: %v", tick.Err)
	}
	if f.count() != 0 {
		t.Error("no request should have been made")
	}
}

func TestPollerOnceIsNotScheduled(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, nil)

	tick := p.Once(context.Background())
	if tick.Err != nil {
		t.Fatal(tick.Err)
	}
	// -once and manual refreshes must not count as missed/hit ticks for the XP
	// stability colouring.
	if tick.Scheduled {
		t.Error("a one-off poll must not be marked scheduled")
	}
	if f.count() != 1 {
		t.Errorf("requests = %d, want 1", f.count())
	}
}

func TestPollerHTMLResponseIsAFailureNotData(t *testing.T) {
	f := newFakeDF(t)
	f.setReply(func(n int) (int, string) {
		return http.StatusOK, "<!DOCTYPE html><html><title>Just a moment...</title></html>"
	})
	p, _, _ := testPoller(t, f, nil)

	tick := p.Once(context.Background())
	if tick.Err == nil {
		t.Fatal("a Cloudflare page must not be treated as data")
	}
	if errors.Is(tick.Err, ErrStaleCredentials) {
		t.Error("an HTML page is not a credential problem and must not stop polling")
	}
}

func TestPollerIntervalFollowsGameState(t *testing.T) {
	f := newFakeDF(t)
	p, _, game := testPoller(t, f, func(c *Config) {
		c.Poll.ActiveInterval = config.Duration{Duration: 7 * time.Second}
		c.Poll.IdleInterval = config.Duration{Duration: 11 * time.Minute}
	})

	if got := p.interval(); got != 11*time.Minute {
		t.Errorf("interval with the game closed = %s, want the idle value", got)
	}
	game.SetStateForTesting(model.GameState{Running: true, PID: 1, StartedAt: time.Now()})
	if got := p.interval(); got != 7*time.Second {
		t.Errorf("interval with the game running = %s, want the active value", got)
	}
}

func TestPollerJitterStaysInRange(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, func(c *Config) {
		c.Poll.ActiveInterval = config.Duration{Duration: 10 * time.Second}
		c.Poll.Jitter = 0.1
	})
	base := 10 * time.Second
	sawSpread := false
	for i := 0; i < 200; i++ {
		got := p.jittered(base)
		if got < 9*time.Second || got > 11*time.Second {
			t.Fatalf("jittered = %s, outside 10s +/- 10%%", got)
		}
		if got != base {
			sawSpread = true
		}
	}
	if !sawSpread {
		t.Error("jitter never changed the interval; requests would be metronomic")
	}
	// Zero jitter must be exact, so a user who wants a fixed cadence gets one.
	p2, _, _ := testPoller(t, f, func(c *Config) { c.Poll.Jitter = 0 })
	if got := p2.jittered(base); got != base {
		t.Errorf("with jitter 0, got %s, want exactly %s", got, base)
	}
}

func TestPlayerAndChallengePollersShareGate(t *testing.T) {
	f := newFakeDF(t)
	p, credentials, watcher := testPoller(t, f, nil)
	gate := rategate.New(MinRequestGap)
	p.SetGate(gate)
	challenges := NewChallenge(p.client, credentials, watcher, gate, p.cfg, func() (int, bool) {
		return 415, false
	})
	if p.gate != challenges.gate {
		t.Fatal("player and challenge pollers must share one request gate")
	}
}

func TestPollerBackoffIsCapped(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, func(c *Config) {
		c.Poll.ActiveInterval = config.Duration{Duration: 10 * time.Second}
		c.Poll.IdleInterval = config.Duration{Duration: 10 * time.Second}
		c.Poll.BackoffMax = config.Duration{Duration: 2 * time.Minute}
		c.Poll.Jitter = 0
	})
	// Doubling from the base, then capped.
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 80 * time.Second},
		{5, 2 * time.Minute},
		{50, 2 * time.Minute},
	} {
		if got := p.backoff(tc.failures); got != tc.want {
			t.Errorf("backoff(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

func TestPollerStopsOnContextCancel(t *testing.T) {
	f := newFakeDF(t)
	p, _, _ := testPoller(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	for f.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}
