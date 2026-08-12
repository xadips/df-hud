package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// The browser bridge. The game's credential triple (userID + password hash +
// sc) only exists in a logged-in page's JavaScript context, so a userscript
// POSTs it here and everything else in df-hud works from that.
//
// SECURITY, non-negotiable:
//   - loopback bind only, enforced twice (config validation and again here)
//   - request bodies are NEVER logged, at any level, not even truncated
//   - the payload is account-equivalent, so errors describe shape only, never
//     content
//
// Wire compatibility: we accept the payload shape SilverOverlays established
// (silverscripts.js:2790-2834), which is {"userVars": {...}, "cookies": "..."}
// posted to /api/userData. That means an existing the bridge userscript/the bridge userscript
// install feeds us with no extra userscript at all. Our own bridge script sends
// the same shape plus "skeygen", the signing salt read from page context, which
// is what lets hashed calls survive the game rotating it.
const bridgeMaxBody = 1 << 20 // userVars is ~316 keys / ~7KB; 1MB is generous

type bridgePayload struct {
	// map[string]any rather than map[string]string: the sender is JavaScript,
	// and while the game's initData makes every value a string, a future
	// sender might not. Coerced below rather than failing the whole payload.
	UserVars map[string]any `json:"userVars"`
	SKeyGen  string         `json:"skeygen"`

	// Cookies is the browser session, in the SilverOverlays payload's own field
	// name.
	//
	// This was originally discarded on the reasoning that the API authenticates
	// on userID+password+sc, so a cookie added a secret without adding
	// capability. That was wrong for endpoints under hotrods/: load_challenge
	// redirects to the site's front page without one. It is stored now under the
	// same discipline as the rest, which is also presumably why the bridge userscript
	// sends it in the first place.
	Cookies string `json:"cookies"`
}

// coerce turns a JSON value into the string the game's own parser would have
// produced. Numbers are formatted without exponent so "1054" does not arrive
// as "1.054e+03".
func coerce(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "1"
		}
		return "0"
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	default:
		return fmt.Sprint(t)
	}
}

type bridgeServer struct {
	creds *credStore
	// onCredentials fires when a payload actually changes something, so the
	// pollers can wake immediately instead of waiting out their interval. Nil
	// is fine.
	onCredentials func()
	// consoleToggle fires on POST /api/console/toggle, bound to a Hyprland key.
	consoleToggle func()
	// runStart and xpReset are the same two corrections the tray menu offers,
	// exposed so a key can do them instead of a mouse.
	//
	// This is the layer the "detect the Start button being pressed" idea belongs
	// at: df-hud publishes the action, the compositor decides what triggers it. A
	// Hyprland bindn on a mouse button passes the click through and can check the
	// cursor position before calling this, so the whole idea is configuration -
	// and df-hud never needs to watch global input, which is a capability worth
	// not having.
	runStart func()
	xpReset  func()
	// runClick is the passed-through click on the game's Start button. Separate
	// from runStart because it is a CANDIDATE rather than a command: df-hud checks
	// the cursor position, the focused window and whether a run is already going
	// before it acts on one.
	runClick func()
	// overlayToggle is the same switch as the tray checkbox.
	overlayToggle func()
	started       time.Time
}

// startBridge listens synchronously so a bad address is a startup failure
// rather than a goroutine that quietly never serves, then serves in the
// background. Mirrors df-allstats-watcher's startUI discipline.
func startBridge(addr string, creds *credStore, onCredentials func()) (*bridgeServer, *http.Server, error) {
	if err := validateLoopback(addr); err != nil {
		return nil, nil, err
	}
	bs := &bridgeServer{creds: creds, onCredentials: onCredentials, started: time.Now()}
	srv := &http.Server{
		Handler:           bs.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("bridge cannot listen on %s: %w", addr, err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("bridge: serve stopped: %v", err)
		}
	}()
	log.Printf("bridge: listening on %s (waiting for a browser payload)", ln.Addr())
	return bs, srv, nil
}

// validateLoopback refuses any address that would expose the credential intake
// beyond this machine. Checked here as well as in config so no future caller
// can bypass it.
func validateLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bridge listen address %q must be host:port: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("bridge listen address %q has no host: use 127.0.0.1:PORT, "+
			"because this endpoint receives account credentials", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("bridge listen host %q is not an IP or localhost", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("bridge listen host %q is not loopback: this endpoint receives "+
			"account-equivalent credentials and must never be reachable off this machine", host)
	}
	return nil
}

func (b *bridgeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/userData", b.handleUserData)
	mux.HandleFunc("GET /healthz", b.handleHealth)
	mux.HandleFunc("POST /api/console/toggle", b.handleConsoleToggle)
	mux.HandleFunc("POST /api/run/start", b.hook(func() func() { return b.runStart }, "run clock"))
	mux.HandleFunc("POST /api/xp/reset", b.hook(func() func() { return b.xpReset }, "xp rate"))
	mux.HandleFunc("POST /api/run/click", b.hook(func() func() { return b.runClick }, "run clock"))
	mux.HandleFunc("POST /api/overlay/toggle", b.hook(func() func() { return b.overlayToggle }, "overlay"))
	return mux
}

func (b *bridgeServer) handleUserData(w http.ResponseWriter, r *http.Request) {
	// Content-Type is checked leniently: GM.xmlHttpRequest sends
	// application/json, but a curl-based smoke test should not be rejected for
	// omitting it.
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
		return
	}

	var p bridgePayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, bridgeMaxBody))
	if err := dec.Decode(&p); err != nil {
		// Deliberately does not echo the body or the decoder's excerpt of it.
		log.Printf("bridge: rejected a payload: malformed JSON")
		http.Error(w, "malformed JSON", http.StatusBadRequest)
		return
	}

	cr := Credentials{
		UserID:   coerce(p.UserVars["userID"]),
		Password: coerce(p.UserVars["password"]),
		SC:       coerce(p.UserVars["sc"]),
		Cookie:   strings.TrimSpace(p.Cookies),
	}
	if !cr.Valid() {
		// Name only the missing FIELDS, never any value.
		var missing []string
		if cr.UserID == "" {
			missing = append(missing, "userID")
		}
		if cr.Password == "" {
			missing = append(missing, "password")
		}
		if cr.SC == "" {
			missing = append(missing, "sc")
		}
		log.Printf("bridge: payload missing %s (are you on a logged-in page?)", strings.Join(missing, ", "))
		http.Error(w, "userVars missing "+strings.Join(missing, ", "), http.StatusBadRequest)
		return
	}

	changed, err := b.creds.Set(cr, p.SKeyGen)
	if err != nil {
		log.Printf("bridge: could not store credentials: %v", err)
		http.Error(w, "could not store credentials", http.StatusInternalServerError)
		return
	}
	// The userscript re-posts on every page load, so only speak up when
	// something actually changed - otherwise this logs all day.
	if changed {
		salt := ""
		if p.SKeyGen != "" {
			salt = ", signing salt reported"
		}
		log.Printf("bridge: credentials updated from browser%s", salt)
		if b.onCredentials != nil {
			b.onCredentials()
		}
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

// handleHealth lets the userscript (and a human with curl) check we are up
// without sending secrets. It reports whether credentials are present and how
// old they are, but never any part of them.
func (b *bridgeServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, _, ok := b.creds.Get()
	resp := struct {
		OK          bool   `json:"ok"`
		HaveCreds   bool   `json:"have_credentials"`
		CredsAgeSec int64  `json:"credentials_age_seconds,omitempty"`
		HaveSalt    bool   `json:"have_signing_salt"`
		UptimeSec   int64  `json:"uptime_seconds"`
		Version     string `json:"version"`
	}{
		OK:        true,
		HaveCreds: ok,
		HaveSalt:  b.creds.Salt() != "",
		UptimeSec: int64(time.Since(b.started).Seconds()),
		Version:   version,
	}
	if t := b.creds.UpdatedAt(); !t.IsZero() {
		resp.CredsAgeSec = int64(time.Since(t).Seconds())
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// hook turns an optional callback into a handler, so an endpoint whose feature is
// not wired says so with a 503 rather than a silent 200 that did nothing.
func (b *bridgeServer) hook(get func() func(), what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fn := get()
		if fn == nil {
			http.Error(w, what+" not available", http.StatusServiceUnavailable)
			return
		}
		fn()
		w.WriteHeader(http.StatusOK)
	}
}

func (b *bridgeServer) handleConsoleToggle(w http.ResponseWriter, r *http.Request) {
	if b.consoleToggle == nil {
		http.Error(w, "console not available", http.StatusServiceUnavailable)
		return
	}
	b.consoleToggle()
	w.WriteHeader(http.StatusOK)
}
