package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// Client talks to the game server. One per process; the caller owns pacing.
type Client struct {
	HTTP      *http.Client
	BaseURL   string // e.g. https://fairview.deadfrontier.com/onlinezombiemmo
	UserAgent string
	MaxBody   int64
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
		return nil, fmt.Errorf("df: %s: got an HTML page instead of data (login redirect or Cloudflare)", call)
	}
	vars, err := parseFlash(text)
	if err != nil {
		return nil, fmt.Errorf("df: %s: %w", call, err)
	}
	return vars, nil
}

// GetValues fetches the full player record. Unhashed: userID+password+sc is
// the whole requirement (bank.js:47, inventory.js:1114, df_api.js:706).
func (c *Client) GetValues(ctx context.Context, cr Credentials) (map[string]string, error) {
	return c.Call(ctx, endpointGetValues, orderedParams{
		{"userID", cr.UserID},
		{"password", cr.Password},
		{"sc", cr.SC},
	}, false, "")
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
