package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Dead Frontier's wire protocol, ported from the game's own client
// (the game's client JS). Two shapes matter:
//
//   - Requests are application/x-www-form-urlencoded-LOOKING but are NOT
//     actually URL-encoded in either direction: objectJoin (base.js:165) does
//     raw string concatenation. We must match that byte for byte, because the
//     signature covers the exact serialised string.
//   - Responses are flsh format: &k=v&k=v, parsed by flshToArr (base.js:127).
const (
	// endpointGetValues is unhashed and returns the whole player record.
	endpointGetValues = "get_values"
	// endpointLoadChallenge is hashed and returns every challenge at once.
	endpointLoadChallenge = "hotrods/load_challenge"
)

// allowedEndpoints is an allowlist, not a blocklist, and every call is checked
// against it. Dead Frontier has write endpoints that look like reads -
// `hunger` advances the nourishment tick, `itemspawn` reseeds the item
// generator, `modify_values` writes arbitrary player state (it is how the
// client levels you up and sets your inner-city entry position). Calling any
// of them from a poller would silently corrupt the account, and that is not
// recoverable, so the only callable endpoints are named here explicitly.
// dfclient_test.go additionally walks the package AST and fails if a forbidden
// name appears anywhere in the source.
var allowedEndpoints = map[string]bool{
	endpointGetValues:     true,
	endpointLoadChallenge: true,
}

// forbiddenEndpoints exists for the error message and for the AST test's
// reference list. These are never called.
var forbiddenEndpoints = []string{"hunger", "itemspawn", "modify_values"}

// ErrStaleCredentials means the server rejected our credential triple, almost
// always because `sc` was rotated by a re-login elsewhere. The caller must
// STOP polling on this rather than retry: hammering a rejected credential is
// exactly the bursty pattern that earns a temp ban. Recovery is a fresh
// payload from the browser bridge.
var ErrStaleCredentials = errors.New("df: credentials rejected (sc likely stale)")

// StatusError is the game's own error envelope: a response whose body starts
// with "status=" (base.js:287-350).
type StatusError struct{ Status string }

func (e *StatusError) Error() string { return "df: server returned status=" + e.Status }

// param is one request field. Order is significant: the signature hashes the
// values in the order they are sent, so this cannot be a map.
type param struct{ Key, Value string }

type orderedParams []param

// Encode serialises to k=v&k=v with NO escaping, matching objectJoin
// (base.js:165-177). Escaping here would change the bytes the signature
// covers and every hashed call would be rejected.
func (p orderedParams) Encode() string {
	var b strings.Builder
	for i, kv := range p {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(kv.Key)
		b.WriteByte('=')
		b.WriteString(kv.Value)
	}
	return b.String()
}

// hashBody replicates hash() from the game's md5.js:
//
//	function hash(params) {
//	    var a = params.split("&"); var b = [];
//	    for (...) b.push(a[i].split("="));
//	    var c = SKeyGen;
//	    for (...) c += b[i][1];
//	    return MD5(c);
//	}
//
// So: digest = MD5(salt + every pair's SECOND "="-separated field, in order).
//
// The subtle part, and the one worth a test: JS `split("=")` with no limit
// then index [1] takes only the text between the FIRST and SECOND "=". A value
// that itself contains "=" therefore contributes only its leading fragment to
// the digest, even though the full value is still sent in the body. Taking the
// whole value here would produce a wrong digest for any such param.
func hashBody(salt, body string) string {
	sum := md5.New()
	sum.Write([]byte(salt))
	for _, pair := range strings.Split(body, "&") {
		fields := strings.Split(pair, "=")
		if len(fields) > 1 {
			sum.Write([]byte(fields[1]))
		}
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// signedBody prefixes the digest, matching base.js:213:
// xhr.send('hash=' + datahash + '&' + params)
func signedBody(salt string, p orderedParams) string {
	body := p.Encode()
	return "hash=" + hashBody(salt, body) + "&" + body
}

// parseFlash ports flshToArr (base.js:127-150). Rules, all load-bearing:
//
//   - split on "&"; there is no escaping, values are trusted not to contain it
//   - split each pair on the FIRST "=" only, keeping the remainder as the
//     value (item property strings legitimately contain "=")
//   - drop any segment with no "=", which absorbs the leading empty element
//     since responses BEGIN with "&" (noted at df_api.js:126)
//   - every value stays a string; coerce at point of use
//   - duplicate keys: last wins
//
// A body starting with "status=" is the error envelope, not data.
func parseFlash(body string) (map[string]string, error) {
	if strings.HasPrefix(body, "status=") {
		status, _, _ := strings.Cut(strings.TrimPrefix(body, "status="), "&")
		if status == "value_mismatch" || status == "missing_value" {
			return nil, fmt.Errorf("%w (status=%s)", ErrStaleCredentials, status)
		}
		return nil, &StatusError{Status: status}
	}
	out := make(map[string]string)
	for _, seg := range strings.Split(body, "&") {
		k, v, ok := strings.Cut(seg, "=")
		if !ok {
			continue // no "=": the leading empty element, or junk
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, errors.New("df: response contained no key=value pairs")
	}
	return out, nil
}

// looksLikeHTML catches Cloudflare interstitials and login redirects being
// handed to us as if they were data, the same guard df-allstats-watcher's
// preCheck applies to the public feed.
func looksLikeHTML(body string) bool {
	head := strings.ToLower(strings.TrimSpace(body))
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html") ||
		strings.Contains(head, "<title>") || strings.Contains(head, "cloudflare")
}

// describeHTML names the page we were handed. The <title> is the single most
// diagnostic element - "Login" and "404 Not Found" and a Cloudflare challenge
// are three completely different problems that all look the same as "got HTML".
func describeHTML(body string) string {
	lower := strings.ToLower(body)
	if i := strings.Index(lower, "<title>"); i >= 0 {
		rest := body[i+len("<title>"):]
		if j := strings.Index(strings.ToLower(rest), "</title>"); j >= 0 {
			return " titled " + strconv.Quote(excerpt(rest[:j], 80))
		}
	}
	return ": " + excerpt(body, 160)
}

// excerpt trims a body to something loggable: collapsed whitespace, bounded
// length, so a multi-kilobyte error page becomes one readable line.
func excerpt(body string, max int) string {
	flat := strings.Join(strings.Fields(body), " ")
	if len(flat) > max {
		return flat[:max] + "..."
	}
	return flat
}

// Client talks to the game server. One per process; the caller owns pacing.
type Client struct {
	HTTP      *http.Client
	BaseURL   string // e.g. https://fairview.deadfrontier.com/onlinezombiemmo
	UserAgent string
	MaxBody   int64

	// Cookie is the browser session, needed by endpoints under hotrods/ that
	// check the site session rather than only the credential triple. Empty is
	// fine for get_values.
	Cookie string

	// fellBack keeps the "the credential-free form did not work" line to one,
	// since the alternative is one per poll. publicFailed latches the decision:
	// without it, a server that had stopped answering the GET would mean TWO
	// requests per poll forever - a failed probe and then the real one - which is
	// the opposite of being polite about someone else's bandwidth. One probe per
	// process, then settle on whatever works.
	//
	// This is also the seam the poller's tests use to pin the authenticated
	// request shape, which is why there is no config key for it: the fallback is
	// automatic and one wasted request per process is not worth a knob.
	fellBack     sync.Once
	publicFailed atomic.Bool
}

// Call posts to one endpoint and parses the response. salt is only consulted
// when hashed is true.
//
// It deliberately takes no io.Reader and returns no raw body to callers: the
// request body carries account-equivalent credentials, so it must never be
// logged or bubbled up in an error string.
func (c *Client) Call(ctx context.Context, call string, p orderedParams, hashed bool, salt string) (map[string]string, error) {
	if !allowedEndpoints[call] {
		return nil, fmt.Errorf("df: endpoint %q is not on the allowlist (never callable: %s)",
			call, strings.Join(forbiddenEndpoints, ", "))
	}
	if hashed && salt == "" {
		return nil, errors.New("df: hashed call needs a signing salt (set df.skeygen or let the bridge report it)")
	}

	body := p.Encode()
	if hashed {
		body = signedBody(salt, p)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/"+call+".php", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.UserAgent)
	// Cookie, when we have one. get_values authenticates on the credential
	// triple alone, but hotrods/load_challenge redirects to the site's front
	// page without a session cookie - verified by elimination: the salt is
	// correct (it matches md5.js), the parameters and their order match
	// challenge.js exactly, the path is right (a bare POST there answers
	// "Invalid action" rather than 404), and adding Referer and
	// X-Requested-With changed nothing.
	if c.Cookie != "" {
		req.Header.Set("Cookie", c.Cookie)
	}

	return c.do(req, call)
}

// do sends a prepared request and parses the reply. Split out from Call so the
// credential-free GET below shares every bit of the response handling - the size
// limit, the HTML detection and the flsh parse - rather than reimplementing it.
func (c *Client) do(req *http.Request, call string) (map[string]string, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Wrap without the URL-embedded body: url.Error stringifies the
		// request URL only, but be explicit about intent for future readers.
		return nil, fmt.Errorf("df: %s: transport error: %w", call, err)
	}
	defer resp.Body.Close()

	max := c.MaxBody
	if max <= 0 {
		max = 8 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, fmt.Errorf("df: %s: reading body: %w", call, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("df: %s: HTTP %s", call, resp.Status)
	}
	text := string(raw)
	if looksLikeHTML(text) {
		// Include a short excerpt rather than guessing at the cause. The previous
		// message said "login redirect or Cloudflare", which is a hypothesis; the
		// excerpt is evidence, and it is the difference between diagnosing this in
		// one run and in five. Safe to surface: this is a RESPONSE from the game
		// server on the failure path, not our request body, so it carries no
		// credentials - and it is truncated regardless.
		return nil, fmt.Errorf("df: %s: got an HTML page instead of data%s", call, describeHTML(text))
	}
	vars, err := parseFlash(text)
	if err != nil {
		return nil, fmt.Errorf("df: %s: %w", call, err)
	}
	return vars, nil
}

// GetValues fetches the full player record, WITHOUT credentials.
//
// get_values answers a plain `GET ?userID=<id>` with the same 342 fields the
// authenticated POST returns (measured 2026-08-17). The record is polled every
// 10s, so the old path put an account-equivalent triple in ~360 request bodies an
// hour to read data the server hands to anyone - and stopped polling entirely
// when a re-login elsewhere rotated `sc`.
//
// The POST stays as an automatic fallback since the GET form is undocumented.
func (c *Client) GetValues(ctx context.Context, cr Credentials) (map[string]string, error) {
	if !c.publicFailed.Load() {
		vars, err := c.getValuesPublic(ctx, cr.UserID)
		switch {
		case err == nil && recordLooksReal(vars):
			return vars, nil
		case err == nil:
			err = errors.New("the reply was not a player record")
		}
		c.publicFailed.Store(true)
		c.fellBack.Do(func() {
			log.Printf("df: the credential-free record fetch did not work (%v); "+
				"using the authenticated call for the rest of this run", err)
		})
	}
	return c.Call(ctx, endpointGetValues, orderedParams{
		{"userID", cr.UserID},
		{"password", cr.Password},
		{"sc", cr.SC},
	}, false, "")
}

// getValuesPublic is the credential-free form: one query parameter, no body, no
// cookie.
func (c *Client) getValuesPublic(ctx context.Context, userID string) (map[string]string, error) {
	if !numericID.MatchString(userID) {
		// Interpolated into a URL, so it is checked rather than escaped. Every
		// real user id is a plain number.
		return nil, fmt.Errorf("%q is not a user id", userID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/"+endpointGetValues+".php?userID="+userID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	return c.do(req, endpointGetValues)
}

var numericID = regexp.MustCompile(`^[0-9]{1,20}$`)

// recordLooksReal guards against a 200 that is not the record - an empty body, a
// courtesy page, an error envelope that parsed. Both fields are present in every
// observed reply and neither is optional for a real account.
func recordLooksReal(vars map[string]string) bool {
	return vars["df_level"] != "" && vars["id_member"] != ""
}

// LoadChallenge fetches every challenge. Hashed, and the parameter order below
// is part of the signature - do not reorder (challenge.js:43-59).
func (c *Client) LoadChallenge(ctx context.Context, cr Credentials, salt string) (map[string]string, error) {
	return c.Call(ctx, endpointLoadChallenge, orderedParams{
		{"userID", cr.UserID},
		{"password", cr.Password},
		{"sc", cr.SC},
		{"action", "get"},
	}, true, salt)
}
