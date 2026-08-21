package dfclient

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The salt the game currently ships as SKeyGen. Only a test constant: the real
// one comes from config or the bridge, because it rotates on game updates.
const testSalt = "y27bigaOAA1"

func TestHashBody(t *testing.T) {
	// Hard-coded digests, computed independently of this implementation. If the
	// algorithm ever drifts these fail loudly, which is the point: a wrong
	// digest means every hashed call is silently rejected by the server.
	tests := []struct {
		name string
		body string
		want string
	}{{
		name: "realistic load_challenge params",
		body: "userID=999&password=hashhash&sc=sc123&action=get",
		want: "7fe50eb1872ba13897d0b7cc8b83e5e4", // md5(salt+"999"+"hashhash"+"sc123"+"get")
	}, {
		// THE QUIRK, and the whole reason this function is not one line.
		// The game does params.split("&") then pair.split("=") and takes
		// index [1], so a value containing "=" contributes only the fragment
		// between the first and second "=". Here k2's value is "b=c" but only
		// "b" is hashed. A naive implementation that hashed the full value
		// would produce 2873e7b6d53e25072ecd596888c6e4ce and every request
		// signed that way would be rejected.
		name: "value containing = is truncated for hashing only",
		body: "k1=a&k2=b=c",
		want: "adedaf7c7c63f8c975c6e129793aa786", // md5(salt+"a"+"b")
	}, {
		name: "empty value contributes nothing",
		body: "userID=999&password=hashhash&sc=sc123&action=get&extra=",
		want: "7fe50eb1872ba13897d0b7cc8b83e5e4", // identical to the first case
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hashBody(testSalt, tc.body); got != tc.want {
				t.Errorf("hashBody(%q)\n got %s\nwant %s", tc.body, got, tc.want)
			}
		})
	}
}

func TestSignedBodyShape(t *testing.T) {
	p := orderedParams{{"userID", "999"}, {"password", "hashhash"}, {"sc", "sc123"}, {"action", "get"}}
	got := signedBody(testSalt, p)
	want := "hash=7fe50eb1872ba13897d0b7cc8b83e5e4&userID=999&password=hashhash&sc=sc123&action=get"
	if got != want {
		t.Errorf("signedBody\n got %s\nwant %s", got, want)
	}
	// The digest must prefix the body verbatim; the server rehashes what it
	// receives, so any reordering or re-encoding here breaks every call.
	if !strings.HasPrefix(got, "hash=") {
		t.Error("digest must come first")
	}
	if !strings.HasSuffix(got, p.Encode()) {
		t.Error("params must follow the digest byte-for-byte")
	}
}

func TestOrderedParamsEncodeDoesNotEscape(t *testing.T) {
	// objectJoin (base.js:165) does raw concatenation. Escaping would change
	// the bytes the signature covers.
	p := orderedParams{{"a", "b c"}, {"d", "e&f"}, {"g", "h=i"}}
	if got, want := p.Encode(), "a=b c&d=e&f&g=h=i"; got != want {
		t.Errorf("Encode() = %q, want %q (no escaping)", got, want)
	}
}

func TestParseFlash(t *testing.T) {
	t.Run("leading ampersand tolerated", func(t *testing.T) {
		// Real responses begin with "&" (df_api.js:126 deletes the resulting
		// empty key); we must drop it instead of producing a phantom entry.
		got, err := parseFlash("&df_level=415&df_cash=1000")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 keys, got %d: %v", len(got), got)
		}
		if got["df_level"] != "415" {
			t.Errorf("df_level = %q", got["df_level"])
		}
	})

	t.Run("value containing = kept whole", func(t *testing.T) {
		// Item property strings legitimately contain "=". Splitting on every
		// "=" would silently truncate them.
		got, err := parseFlash("&df_inv1_type=gun_stats14=2&df_level=1")
		if err != nil {
			t.Fatal(err)
		}
		if got["df_inv1_type"] != "gun_stats14=2" {
			t.Errorf("value truncated: %q", got["df_inv1_type"])
		}
	})

	t.Run("segment without = dropped", func(t *testing.T) {
		got, err := parseFlash("&junk&df_level=415")
		if err != nil {
			t.Fatal(err)
		}
		if _, bad := got["junk"]; bad {
			t.Error("segment without = should be dropped")
		}
	})

	t.Run("duplicate key last wins", func(t *testing.T) {
		got, err := parseFlash("&k=first&k=second")
		if err != nil {
			t.Fatal(err)
		}
		if got["k"] != "second" {
			t.Errorf("k = %q, want last value", got["k"])
		}
	})

	t.Run("empty body is an error not an empty map", func(t *testing.T) {
		if _, err := parseFlash(""); err == nil {
			t.Error("want an error for an empty body")
		}
	})

	t.Run("status envelope detected", func(t *testing.T) {
		var se *StatusError
		_, err := parseFlash("status=no_results&foo=bar")
		if !errors.As(err, &se) || se.Status != "no_results" {
			t.Fatalf("want StatusError{no_results}, got %v", err)
		}
	})

	t.Run("stale credentials classified separately", func(t *testing.T) {
		// This one MUST be distinguishable: the caller has to stop polling
		// rather than retry, or it becomes the bursty pattern that earns a
		// temp ban.
		for _, s := range []string{"value_mismatch", "missing_value"} {
			_, err := parseFlash("status=" + s)
			if !errors.Is(err, ErrStaleCredentials) {
				t.Errorf("status=%s should be ErrStaleCredentials, got %v", s, err)
			}
		}
	})
}

func TestLooksLikeHTML(t *testing.T) {
	for _, body := range []string{
		"<!DOCTYPE html><html>", "<html><head>", "  <TITLE>Just a moment</TITLE>",
		"<div>attention required cloudflare</div>",
	} {
		if !looksLikeHTML(body) {
			t.Errorf("should be detected as HTML: %q", body)
		}
	}
	if looksLikeHTML("&df_level=415&df_cash=10") {
		t.Error("a real flsh body must not be flagged as HTML")
	}
}

func TestClientAllowlist(t *testing.T) {
	c := &Client{HTTP: http.DefaultClient, BaseURL: "http://127.0.0.1:1", UserAgent: "test"}
	// Every write endpoint the game has must be unreachable through Call.
	for _, call := range forbiddenEndpoints {
		_, err := c.call(context.Background(), call, orderedParams{}, false, "")
		if err == nil || !strings.Contains(err.Error(), "allowlist") {
			t.Errorf("%s must be rejected by the allowlist, got %v", call, err)
		}
	}
	if _, err := c.call(context.Background(), "get_storage", orderedParams{}, false, ""); err == nil {
		t.Error("an unlisted endpoint must be rejected even if it is harmless")
	}
}

func TestClientHashedCallNeedsSalt(t *testing.T) {
	c := &Client{HTTP: http.DefaultClient, BaseURL: "http://127.0.0.1:1", UserAgent: "test"}
	_, err := c.call(context.Background(), endpointLoadChallenge, orderedParams{}, true, "")
	if err == nil || !strings.Contains(err.Error(), "salt") {
		t.Errorf("hashed call without a salt must fail clearly, got %v", err)
	}
}

func TestClientAgainstFakeServer(t *testing.T) {
	var gotBody, gotUA, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		gotBody, gotUA, gotPath = string(b), r.Header.Get("User-Agent"), r.URL.Path
		w.Write([]byte("&df_level=415&df_positionx=1054&df_positiony=987"))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, UserAgent: "df-hud/test"}
	cr := Credentials{UserID: "999", Password: "hashhash", SC: "sc123"}

	vars, err := c.GetValues(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	if vars["df_positionx"] != "1054" {
		t.Errorf("df_positionx = %q", vars["df_positionx"])
	}
	if gotPath != "/get_values.php" {
		t.Errorf("path = %q", gotPath)
	}
	if gotUA != "df-hud/test" {
		t.Errorf("User-Agent = %q, must identify us", gotUA)
	}
	// Unhashed: no digest prefix, and parameter order preserved.
	if strings.HasPrefix(gotBody, "hash=") {
		t.Error("get_values must NOT be signed")
	}
	if gotBody != "userID=999&password=hashhash&sc=sc123" {
		t.Errorf("body = %q", gotBody)
	}

	// And the hashed path signs with the salt, in the documented order.
	if _, err := c.LoadChallenge(context.Background(), cr, testSalt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotBody, "hash=7fe50eb1872ba13897d0b7cc8b83e5e4&") {
		t.Errorf("load_challenge body not signed as expected: %q", gotBody)
	}
}

func TestClientRejectsHTMLResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<!DOCTYPE html><html><title>Attention Required! | Cloudflare</title>"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, UserAgent: "test"}
	_, err := c.GetValues(context.Background(), Credentials{UserID: "1", Password: "2", SC: "3"})
	if err == nil || !strings.Contains(err.Error(), "HTML") {
		t.Errorf("an HTML body must be rejected, got %v", err)
	}
}

// TestNoForbiddenEndpointsInSource walks the package AST and fails if a write
// endpoint appears as a string literal outside the two places that legitimately
// name them (the forbiddenEndpoints list itself, and this test). Belt and braces
// on top of the runtime allowlist, because calling one of these is not
// recoverable: modify_values writes arbitrary player state.
//
// The comparison is on the WHOLE literal, or on its last path segment so that
// "hotrods/hunger" is still caught. It deliberately is not a substring test:
// substring matching flagged df_hungerhp, an ordinary field in the player
// record, which would have pushed someone to weaken this guard to silence a
// false positive. An endpoint is always passed as a complete string, so exact
// matching loses nothing.
func TestNoForbiddenEndpointsInSource(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		n := fi.Name()
		// Skip this test file (it names them) and the client (which lists them
		// in forbiddenEndpoints for the error message).
		return strings.HasSuffix(n, ".go") && n != "dfclient_test.go" && n != "dfclient.go"
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				// The endpoint an operation names is the last path segment,
				// e.g. "hotrods/load_challenge".
				segment := value
				if i := strings.LastIndex(segment, "/"); i >= 0 {
					segment = segment[i+1:]
				}
				for _, bad := range []string{"hunger", "itemspawn", "modify_values"} {
					if value == bad || segment == bad {
						t.Errorf("%s:%d: forbidden endpoint %q appears in source",
							path, fset.Position(lit.Pos()).Line, bad)
					}
				}
				return true
			})
		}
	}
}

// The whole point of the change: the credential must not leave the machine on
// the polling path. This asserts it against the bytes actually sent.
func TestGetValuesSendsNoCredentialByDefault(t *testing.T) {
	const secret = "hunter2-password"
	var got struct {
		method, url, body, cookie string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.method, got.url, got.body = r.Method, r.URL.String(), string(raw)
		got.cookie = r.Header.Get("Cookie")
		fmt.Fprint(w, "&id_member=1234567&df_level=415&df_exptotal=10000000")
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, UserAgent: "df-hud/test", Cookie: "session=abc"}
	vars, err := c.GetValues(context.Background(),
		Credentials{UserID: "1234567", Password: secret, SC: "sc-value"})
	if err != nil {
		t.Fatal(err)
	}
	if vars["df_level"] != "415" {
		t.Fatalf("record not parsed: %v", vars)
	}
	if got.method != http.MethodGet {
		t.Errorf("method %s, want GET", got.method)
	}
	if got.body != "" {
		t.Errorf("sent a request body: %q", got.body)
	}
	// Belt and braces: search everything that left, not just the body.
	for what, s := range map[string]string{"url": got.url, "body": got.body, "cookie": got.cookie} {
		for _, forbidden := range []string{secret, "sc-value", "password"} {
			if strings.Contains(s, forbidden) {
				t.Errorf("%s carried %q: %s", what, forbidden, s)
			}
		}
	}
	if !strings.Contains(got.url, "userID=1234567") {
		t.Errorf("url %q does not identify the account", got.url)
	}
}

// The GET form is undocumented, so a server that stops answering it must not
// stop the HUD: the authenticated call takes over by itself.
func TestGetValuesFallsBackToTheAuthenticatedCall(t *testing.T) {
	var sawPost bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		sawPost = true
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "password=hunter2") {
			t.Errorf("the fallback did not authenticate: %q", raw)
		}
		fmt.Fprint(w, "&id_member=1&df_level=415")
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, UserAgent: "df-hud/test"}
	if _, err := c.GetValues(context.Background(),
		Credentials{UserID: "1", Password: "hunter2", SC: "sc"}); err != nil {
		t.Fatal(err)
	}
	if !sawPost {
		t.Fatal("a failed GET did not fall back to the authenticated call")
	}
}

// A 200 that is not the record must not be accepted as one - an empty body, a
// courtesy page, anything that happens to parse.
func TestGetValuesRejectsAReplyThatIsNotARecord(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, "&something=else&unrelated=1")
			return
		}
		posted = true
		fmt.Fprint(w, "&id_member=1&df_level=415")
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, UserAgent: "df-hud/test"}
	if _, err := c.GetValues(context.Background(),
		Credentials{UserID: "1", Password: "p", SC: "s"}); err != nil {
		t.Fatal(err)
	}
	if !posted {
		t.Fatal("a 200 that was not a record was accepted")
	}
}

// The id is interpolated into a URL, so it is checked rather than escaped.
func TestGetValuesRefusesAUserIDThatIsNotANumber(t *testing.T) {
	var sawGet bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			sawGet = true
		}
		io.ReadAll(r.Body)
		fmt.Fprint(w, "&id_member=1&df_level=415")
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, UserAgent: "df-hud/test"}
	if _, err := c.GetValues(context.Background(),
		Credentials{UserID: "1&evil=1", Password: "p", SC: "s"}); err != nil {
		t.Fatal(err)
	}
	if sawGet {
		t.Fatal("a user id that is not a number was interpolated into the URL")
	}
}

// One probe per process. A server that has stopped answering the GET must not
// mean two requests per poll forever.
func TestTheCredentialFreeProbeIsTriedOnlyOnce(t *testing.T) {
	var gets, posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		posts++
		io.ReadAll(r.Body)
		fmt.Fprint(w, "&id_member=1&df_level=415")
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL, UserAgent: "df-hud/test"}
	cr := Credentials{UserID: "1", Password: "p", SC: "s"}
	for i := 0; i < 5; i++ {
		if _, err := c.GetValues(context.Background(), cr); err != nil {
			t.Fatal(err)
		}
	}
	if gets != 1 {
		t.Errorf("probed the GET %d times, want exactly 1", gets)
	}
	if posts != 5 {
		t.Errorf("made %d authenticated calls, want 5", posts)
	}
}
