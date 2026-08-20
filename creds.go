package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Credential handling. Read this before touching anything here:
//
// The bridge payload contains userID + the password HASH + sc, which together
// are the exact triple the game itself signs every API call with. Anyone
// holding them can act as the player. They are therefore treated as secrets
// with no exceptions:
//
//   - the listener binds loopback only (enforced in config validation)
//   - request bodies are never logged, at any level
//   - the on-disk file is 0600, and the mode is re-verified after writing
//   - String/GoString/MarshalJSON all REDACT, so a stray %v, %+v or a JSON
//     encode of any struct containing these cannot leak them. Persistence uses
//     a separate unexported type that opts in explicitly.

// Credentials is the signing triple, plus the browser session cookie.
//
// The cookie was originally accepted and discarded, on the reasoning that the
// API authenticates on userID+password+sc so a cookie would only allow fetching
// web pages. That reasoning was WRONG for endpoints under hotrods/:
// load_challenge redirects to the site's front page without one, with a correct
// signature and correct parameters. It is stored now, under exactly the same
// discipline as the rest - 0600, redacted in every stringification - because it
// is one more account secret and adds no new class of risk alongside a password
// hash.
type Credentials struct {
	UserID   string
	Password string
	SC       string
	Cookie   string
}

// Valid covers what every call needs. The cookie is NOT required here:
// get_values works without one, so a payload lacking it is still useful and
// must not be rejected.
func (c Credentials) Valid() bool {
	return c.UserID != "" && c.Password != "" && c.SC != ""
}

// String redacts. This is the safety net for accidental %s / %v / log.Print.
func (c Credentials) String() string {
	if !c.Valid() {
		return "Credentials{unset}"
	}
	return fmt.Sprintf("Credentials{UserID:%s Password:%s SC:%s Cookie:%s}",
		redact(c.UserID), redact(c.Password), redact(c.SC), redact(c.Cookie))
}

// GoString redacts, covering %#v.
func (c Credentials) GoString() string { return c.String() }

// MarshalJSON redacts, covering any JSON encode that reaches these - an API
// response, a structured log line, a debug dump. Persistence deliberately does
// NOT go through this path; see credStore.save.
func (c Credentials) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserID   string `json:"userID"`
		Password string `json:"password"`
		SC       string `json:"sc"`
		Cookie   string `json:"cookie"`
	}{redact(c.UserID), redact(c.Password), redact(c.SC), redact(c.Cookie)})
}

// redact keeps a short prefix so two different values are distinguishable in
// logs while staying useless to anyone reading them.
func redact(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return "[redacted]"
	}
	return s[:2] + "…[redacted " + fmt.Sprint(len(s)) + "ch]"
}

// credsFile is the only representation that carries real values, and it exists
// solely so persistence has to be explicit about it.
type credsFile struct {
	SchemaVersion int       `json:"schema_version"`
	UserID        string    `json:"userID"`
	Password      string    `json:"password"`
	SC            string    `json:"sc"`
	Cookie        string    `json:"cookie,omitempty"`
	SKeyGen       string    `json:"skeygen,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

const credsSchemaVersion = 1

// credStore is the process-wide holder. Safe for concurrent use: the bridge
// writes, the pollers read.
type credStore struct {
	mu        sync.RWMutex
	path      string
	creds     Credentials
	skeyGen   string // signing salt reported by the bridge; wins over config
	updatedAt time.Time
}

func newCredStore(path string) *credStore { return &credStore{path: path} }

// Load reads a previously saved file. A missing file is not an error: it just
// means we are waiting for the browser bridge. A corrupt file is quarantined
// rather than fatal, matching the state.json discipline.
func (s *credStore) Load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f credsFile
	if err := json.Unmarshal(data, &f); err != nil || f.SchemaVersion != credsSchemaVersion {
		quarantine := fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().Unix())
		if rerr := os.Rename(s.path, quarantine); rerr == nil {
			return fmt.Errorf("credentials file unusable, moved to %s; waiting for the browser bridge", quarantine)
		}
		return fmt.Errorf("credentials file unusable and could not be moved aside: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds = Credentials{UserID: f.UserID, Password: f.Password, SC: f.SC, Cookie: f.Cookie}
	s.skeyGen, s.updatedAt = f.SKeyGen, f.UpdatedAt
	return nil
}

// Set replaces the stored credentials and persists them. Returns whether
// anything actually changed, so the caller can avoid logging a no-op on every
// bridge POST (the userscript re-posts on every page load).
func (s *credStore) Set(c Credentials, skeyGen string) (changed bool, err error) {
	if !c.Valid() {
		return false, errors.New("refusing to store an incomplete credential triple")
	}
	s.mu.Lock()
	changed = s.creds != c || (skeyGen != "" && skeyGen != s.skeyGen)
	s.creds = c
	if skeyGen != "" {
		s.skeyGen = skeyGen
	}
	s.updatedAt = time.Now()
	snapshot := credsFile{
		SchemaVersion: credsSchemaVersion,
		UserID:        c.UserID,
		Password:      c.Password,
		SC:            c.SC,
		Cookie:        c.Cookie,
		SKeyGen:       s.skeyGen,
		UpdatedAt:     s.updatedAt,
	}
	s.mu.Unlock()

	if s.path == "" {
		return changed, nil // memory-only, used by tests
	}
	return changed, s.save(snapshot)
}

// save writes atomically at 0600 and verifies the resulting mode. The verify
// matters because a pre-existing file keeps its own permissions through
// OpenFile, so a previously world-readable file would silently stay that way.
func (s *credStore) save(f credsFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	fh, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := fh.Write(data); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if err := fh.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	st, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	// Windows reports synthetic POSIX permission bits and ignores chmod. The
	// file inherits the current user's private AppData ACL instead.
	if perm := st.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		return fmt.Errorf("credentials file is mode %o, want 600", perm)
	}
	return nil
}

// Get returns the credentials plus the effective signing salt. ok is false
// until the bridge has delivered a usable triple.
func (s *credStore) Get() (c Credentials, salt string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds, s.skeyGen, s.creds.Valid()
}

// Salt returns the bridge-reported signing salt, empty if none. The caller
// falls back to the configured value; a reported value wins because it comes
// from live page context and therefore self-heals when the game rotates it.
func (s *credStore) Salt() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.skeyGen
}

// UpdatedAt is when the bridge last delivered a payload, for the HUD's
// "credentials stale" banner.
func (s *credStore) UpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

// GoString redacts, covering %#v. This is NOT redundant with String(): fmt
// cannot reach Credentials.GoString through credStore's fields because they are
// unexported (reflect's CanInterface is false for those, so fmt skips the
// Stringer/GoStringer methods and dumps raw field values). Without this,
// %#v on the store printed the password, sc and salt in the clear - caught by
// TestBridgeNeverLogsSecrets. Any future struct that holds credentials in an
// unexported field needs the same treatment.
func (s *credStore) GoString() string { return s.String() }

// String redacts, so logging the store itself is safe.
func (s *credStore) String() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprintf("credStore{path:%s creds:%s salt:%s updated:%s}",
		s.path, s.creds, redact(s.skeyGen), s.updatedAt.Format(time.RFC3339))
}
