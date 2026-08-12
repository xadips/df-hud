package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Values that look like the real thing so the leak tests have something
// distinctive to grep for. Not real credentials.
const (
	fakeUserID   = "1234567"
	fakePassword = "3a7bd3e2360a3d29eea436fcfb7e44c735d117c4"
	fakeSC       = "0f9a1c4e8b2d6f3a7c5e9b1d4f8a2c6e"
	fakeSalt     = "y27bigaOAA1"
)

func testBridge(t *testing.T) (*bridgeServer, *httptest.Server, *credStore) {
	t.Helper()
	store := newCredStore(filepath.Join(t.TempDir(), "credentials.json"))
	bs := &bridgeServer{creds: store}
	srv := httptest.NewServer(bs.handler())
	t.Cleanup(srv.Close)
	return bs, srv, store
}

func payloadJSON(userVars map[string]any, salt string, cookies string) []byte {
	b, _ := json.Marshal(map[string]any{
		"userVars": userVars,
		"skeygen":  salt,
		"cookies":  cookies,
	})
	return b
}

func validUserVars() map[string]any {
	return map[string]any{
		"userID":   fakeUserID,
		"password": fakePassword,
		"sc":       fakeSC,
		"df_level": "415",
	}
}

func TestBridgeAcceptsPayload(t *testing.T) {
	_, srv, store := testBridge(t)

	resp, err := http.Post(srv.URL+"/api/userData", "application/json",
		bytes.NewReader(payloadJSON(validUserVars(), fakeSalt, "")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	cr, salt, ok := store.Get()
	if !ok {
		t.Fatal("credentials not stored")
	}
	if cr.UserID != fakeUserID || cr.Password != fakePassword || cr.SC != fakeSC {
		t.Error("stored credentials do not match the payload")
	}
	if salt != fakeSalt {
		t.Errorf("salt = %q, want the bridge-reported value to win", salt)
	}
}

func TestBridgePersistsAt0600(t *testing.T) {
	_, srv, store := testBridge(t)
	http.Post(srv.URL+"/api/userData", "application/json",
		bytes.NewReader(payloadJSON(validUserVars(), fakeSalt, "")))

	st, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file mode = %o, want 600", perm)
	}
}

// The cookie IS stored, reversing an earlier decision. It was discarded on the
// reasoning that userID+password+sc authenticates everything, so a cookie added a
// secret without adding capability - which turned out to be wrong for endpoints
// under hotrods/, where load_challenge redirects to the site's front page
// without one.
//
// What must hold is the discipline around it: private on disk, and redacted in
// every stringification (TestBridgeNeverLogsSecrets covers the logging side).
func TestBridgeStoresTheCookiePrivately(t *testing.T) {
	const cookie = "DeadFrontierFairview=session-value; lastLoginUser=someone"
	_, srv, store := testBridge(t)
	resp, err := http.Post(srv.URL+"/api/userData", "application/json",
		bytes.NewReader(payloadJSON(validUserVars(), fakeSalt, cookie)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	cr, _, ok := store.Get()
	if !ok {
		t.Fatal("credentials not stored")
	}
	if cr.Cookie != cookie {
		t.Errorf("Cookie = %q, want it stored for hashed calls", cr.Cookie)
	}

	st, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file mode = %o, want 600 now that it holds a session cookie", perm)
	}
	// A payload with no cookie must still be accepted: get_values needs none, so
	// rejecting it would break the common case for the sake of the rare one.
	if _, err := store.Set(Credentials{UserID: "1", Password: "2", SC: "3"}, ""); err != nil {
		t.Errorf("a triple without a cookie must remain valid: %v", err)
	}
}

func TestBridgeRejectsIncompletePayloads(t *testing.T) {
	cases := map[string]map[string]any{
		"no userID":   {"password": fakePassword, "sc": fakeSC},
		"no password": {"userID": fakeUserID, "sc": fakeSC},
		"no sc":       {"userID": fakeUserID, "password": fakePassword},
		"empty":       {},
	}
	for name, uv := range cases {
		t.Run(name, func(t *testing.T) {
			_, srv, store := testBridge(t)
			resp, err := http.Post(srv.URL+"/api/userData", "application/json",
				bytes.NewReader(payloadJSON(uv, "", "")))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
			if _, _, ok := store.Get(); ok {
				t.Error("a partial triple must not be stored")
			}
		})
	}
}

func TestBridgeRejectsBadInput(t *testing.T) {
	_, srv, _ := testBridge(t)

	t.Run("malformed JSON", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/api/userData", "application/json",
			strings.NewReader("{not json"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("wrong content type", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/api/userData", "text/plain",
			bytes.NewReader(payloadJSON(validUserVars(), "", "")))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d, want 415", resp.StatusCode)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		uv := validUserVars()
		uv["junk"] = strings.Repeat("x", bridgeMaxBody+1)
		resp, err := http.Post(srv.URL+"/api/userData", "application/json",
			bytes.NewReader(payloadJSON(uv, "", "")))
		if err != nil {
			return // the server may close the connection first, which is fine
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("an oversized body must not be accepted")
		}
	})

	t.Run("GET is not allowed", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/userData")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("userData must be POST-only")
		}
	})
}

// TestBridgeNeverLogsSecrets is the important one. The payload is
// account-equivalent, so no code path - success, rejection, or error - may put
// any part of it into the log.
func TestBridgeNeverLogsSecrets(t *testing.T) {
	var logged bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(old)

	_, srv, store := testBridge(t)

	// Success path, no-change path (a repost), a partial payload, and garbage.
	body := payloadJSON(validUserVars(), fakeSalt, "DeadFrontierFairview=secretcookie")
	http.Post(srv.URL+"/api/userData", "application/json", bytes.NewReader(body))
	http.Post(srv.URL+"/api/userData", "application/json", bytes.NewReader(body))
	http.Post(srv.URL+"/api/userData", "application/json",
		bytes.NewReader(payloadJSON(map[string]any{"userID": fakeUserID}, "", "")))
	http.Post(srv.URL+"/api/userData", "application/json", strings.NewReader("{bad"))

	// Also exercise the deliberately-safe stringifications.
	logged.WriteString(fmt.Sprintf(" %v %s %#v ", store, store, store))
	cr, _, _ := store.Get()
	logged.WriteString(fmt.Sprintf(" %v %s %#v ", cr, cr, cr))
	if j, err := json.Marshal(cr); err == nil {
		logged.Write(j)
	}

	out := logged.String()
	for name, secret := range map[string]string{
		"password":     fakePassword,
		"sc":           fakeSC,
		"salt":         fakeSalt,
		"cookie value": "secretcookie",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("%s leaked into log output:\n%s", name, out)
		}
	}
	// The userID is the least sensitive but still should not appear in full.
	if strings.Contains(out, fakeUserID) {
		t.Errorf("userID leaked into log output:\n%s", out)
	}
}

func TestBridgeHealthReportsNoSecrets(t *testing.T) {
	_, srv, _ := testBridge(t)
	http.Post(srv.URL+"/api/userData", "application/json",
		bytes.NewReader(payloadJSON(validUserVars(), fakeSalt, "")))

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["have_credentials"] != true {
		t.Error("health should report credentials present")
	}
	if got["have_signing_salt"] != true {
		t.Error("health should report the salt present")
	}
	raw, _ := json.Marshal(got)
	for _, secret := range []string{fakePassword, fakeSC, fakeSalt, fakeUserID} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("healthz leaked a secret: %s", raw)
		}
	}
}

func TestBridgeOnCredentialsFiresOnlyOnChange(t *testing.T) {
	store := newCredStore(filepath.Join(t.TempDir(), "credentials.json"))
	calls := 0
	bs := &bridgeServer{creds: store, onCredentials: func() { calls++ }}
	srv := httptest.NewServer(bs.handler())
	defer srv.Close()

	body := payloadJSON(validUserVars(), fakeSalt, "")
	http.Post(srv.URL+"/api/userData", "application/json", bytes.NewReader(body))
	http.Post(srv.URL+"/api/userData", "application/json", bytes.NewReader(body))
	if calls != 1 {
		t.Errorf("onCredentials fired %d times, want 1 (the script reposts unchanged data)", calls)
	}

	// A rotated sc - the case that matters - must wake the pollers.
	uv := validUserVars()
	uv["sc"] = "ffffffffffffffffffffffffffffffff"
	http.Post(srv.URL+"/api/userData", "application/json",
		bytes.NewReader(payloadJSON(uv, fakeSalt, "")))
	if calls != 2 {
		t.Errorf("onCredentials fired %d times, want 2 after sc rotated", calls)
	}
}

func TestValidateLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9275", "localhost:9275", "[::1]:9275"} {
		if err := validateLoopback(addr); err != nil {
			t.Errorf("%s should be allowed: %v", addr, err)
		}
	}
	// Anything reachable off-machine hands account credentials to the network.
	for _, addr := range []string{":9275", "0.0.0.0:9275", "192.168.1.2:9275", "example.com:9275", "9275"} {
		if err := validateLoopback(addr); err == nil {
			t.Errorf("%s must be rejected", addr)
		}
	}
}

func TestCoerce(t *testing.T) {
	// JSON numbers must not arrive as "1.054e+03" - positions and levels are
	// compared as integers downstream.
	cases := []struct {
		in   any
		want string
	}{
		{"1054", "1054"}, {float64(1054), "1054"}, {float64(415), "415"},
		{nil, ""}, {true, "1"}, {false, "0"}, {float64(1.5), "1.5"},
	}
	for _, tc := range cases {
		if got := coerce(tc.in); got != tc.want {
			t.Errorf("coerce(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The run clock and the xp rate are correctable from a keybind, not only from the
// tray. This is also the layer that "start the timer when I click the game's Start
// button" belongs at: Hyprland can pass a click through with bindn and check the
// cursor position before calling this, so df-hud never needs to watch global
// input - a capability worth not having.
func TestBridgeCorrectionEndpoints(t *testing.T) {
	bs, srv, _ := testBridge(t)

	for _, path := range []string{"/api/run/start", "/api/xp/reset"} {
		// Not wired: a 503 that says so, never a silent 200 that did nothing.
		resp, err := http.Post(srv.URL+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s with no hook = %d, want 503", path, resp.StatusCode)
		}
	}

	var runs, resets int
	bs.runStart = func() { runs++ }
	bs.xpReset = func() { resets++ }
	for _, path := range []string{"/api/run/start", "/api/xp/reset"} {
		resp, err := http.Post(srv.URL+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, resp.StatusCode)
		}
	}
	if runs != 1 || resets != 1 {
		t.Errorf("fired run=%d reset=%d, want one each", runs, resets)
	}

	// GET must not act: a bookmark, a preflight or a curl typo should not silently
	// restart the clock.
	resp, err := http.Get(srv.URL + "/api/run/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("GET should not trigger the action")
	}
	if runs != 1 {
		t.Errorf("a GET fired the hook %d times", runs)
	}
}

func TestBridgeOverlayToggle(t *testing.T) {
	bs, srv, _ := testBridge(t)
	var toggles int
	bs.overlayToggle = func() { toggles++ }

	resp, err := http.Post(srv.URL+"/api/overlay/toggle", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || toggles != 1 {
		t.Errorf("status %d, toggles %d", resp.StatusCode, toggles)
	}
}

// The guard that makes binding a mouse button safe: once a run is going, every
// click is inert without the compositor being asked anything at all.
func TestRunClickAllowed(t *testing.T) {
	cfg := RunStartConfig{ClickEnabled: true, ButtonX: 1230, ButtonY: 660, ButtonWidth: 100, ButtonHeight: 40}

	if !runClickAllowed(cfg, false, true) {
		t.Error("no run in progress and the game running: the click is worth checking")
	}
	if runClickAllowed(cfg, true, true) {
		t.Error("a run is already going; this is how a click per shot fired stays free")
	}
	if runClickAllowed(cfg, false, false) {
		t.Error("the game is not running, so nothing can be started")
	}
	off := cfg
	off.ClickEnabled = false
	if runClickAllowed(off, false, true) {
		t.Error("disabled means disabled")
	}
}

func TestRunStartButtonContains(t *testing.T) {
	cfg := RunStartConfig{ButtonX: 1230, ButtonY: 660, ButtonWidth: 100, ButtonHeight: 40}

	for _, p := range [][2]int{{1230, 660}, {1279, 679}, {1329, 699}} {
		if !cfg.ButtonContains(p[0], p[1]) {
			t.Errorf("%v should be on the button", p)
		}
	}
	// Half-open on the far edges, so two adjacent buttons could never both claim
	// the same pixel.
	for _, p := range [][2]int{{1229, 660}, {1230, 659}, {1330, 680}, {1280, 700}, {0, 0}} {
		if cfg.ButtonContains(p[0], p[1]) {
			t.Errorf("%v should be off the button", p)
		}
	}
	// A zero-sized button can never be hit, however the coordinates line up.
	if (RunStartConfig{ButtonX: 10, ButtonY: 10}).ButtonContains(10, 10) {
		t.Error("a zero-sized button must not swallow clicks")
	}
}
