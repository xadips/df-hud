package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Where you are, from the game client instead of the game server.
//
// The client publishes its own position as Discord rich presence, and that is a
// STRICTLY better source than df_positionx/y - measured 2026-08-16 by running
// both at once:
//
//	17:27:45  presence  1054,986
//	17:27:50  poll      1054,987   <- 5s later, server still on the old block
//	17:27:59  poll      1054,987   <- 14s later, still old
//	17:28:01  presence  1054,985
//	17:28:06  presence  1055,985
//	17:28:10  poll      1054,985   <- server moves to a block already left
//
// The poll cadence was correct throughout (9-11s, as configured). The server's
// own record is what lags, by 15-25s, and it SKIPS blocks - 1054,986 never
// appeared in any poll at all. So this is not a freshness optimisation on the
// same data; the polled position is wrong about where you are for most of the
// time you spend walking.
//
// The client is rate limited to about one update every 5s by Discord's own SDK,
// which is still twice the poll and, unlike the poll, never lies about a block
// you have left.
//
// df-hud is the SERVER here. Discord is not involved and need not be running:
// the game talks to a Unix socket, and whoever is bound to it receives the
// activity. See knowledge/presence.md for the plumbing that gets a Proton game's
// Windows named pipe onto that socket.

// Discord IPC framing: a 4-byte little-endian opcode, a 4-byte little-endian
// length, then that many bytes of JSON.
const (
	presenceOpHandshake uint32 = 0
	presenceOpFrame     uint32 = 1
	presenceOpClose     uint32 = 2
	presenceOpPing      uint32 = 3
	presenceOpPong      uint32 = 4
)

// presenceMaxFrame bounds one frame. Activity payloads are a few hundred bytes;
// this is large enough never to matter and small enough that a confused peer
// cannot ask df-hud to allocate a gigabyte.
const presenceMaxFrame = 64 << 10

// PresenceState is what the game last said about where you are.
//
// Loading is its own state rather than "unknown": the client publishes
// "Loading..." while zoning, which is positive evidence that the position it
// last gave is about to change, and different from having heard nothing.
type PresenceState struct {
	At time.Time

	HasPosition bool
	X, Y        int

	// Place is what the coordinates were labelled with: "Inner City" out on the
	// map, or a building's name while inside one. Indoors is that same fact as a
	// boolean, and neither changes the position - a building is ON the block it
	// stands on, which is the block whose events matter when you step back out.
	Place   string
	Indoors bool

	InOutpost   bool
	OutpostName string

	Loading bool

	// Details is the raw string, kept so an unrecognised form can be logged
	// rather than silently dropped - this is somebody else's format and it can
	// change without notice.
	Details string
}

// parsePresenceDetails reads the game's `details` string.
//
// Four forms observed live:
//
//	"Inner City 1054 x 986"   out on the map, exact block coordinates
//	"Hospital 1058 x 1016"    inside a building, on that same block
//	"Secronom Bunker"         standing in an outpost, named
//	"Loading..."              zoning
//
// The coordinates are in the same space as df_positionx/y, so they need no
// transform.
//
// The building form is why the label is not required to be "Inner City". It was
// missed when this was first written - measuring was done walking the streets,
// where the label never varies - so every minute spent looting was a minute the
// position silently fell back to the poll, with the block sitting right there in
// the string. Anything ending in "<x> x <y>" is a position now, whatever it is
// labelled.
//
// An outpost is still matched against the KNOWN names rather than assumed from
// "no coordinates", so a form nobody has seen yet is reported as unparsed instead
// of being filed as an outpost called something odd.
func parsePresenceDetails(details string, at time.Time) PresenceState {
	s := PresenceState{At: at, Details: details}
	text := strings.TrimSpace(details)
	switch {
	case text == "":
		return s
	case strings.EqualFold(text, "loading..."), strings.EqualFold(text, "loading"):
		s.Loading = true
		return s
	}

	if place, x, y, ok := parseBlockPosition(text); ok {
		s.HasPosition, s.X, s.Y = true, x, y
		s.Place = place
		s.Indoors = !strings.EqualFold(place, innerCityLabel)
		return s
	}
	if _, ok := outpostLetters[text]; ok {
		s.InOutpost, s.OutpostName = true, text
		return s
	}
	return s
}

// innerCityLabel is the label that means "out on the streets" rather than inside
// something.
const innerCityLabel = "Inner City"

// parseBlockPosition reads "<label> <x> x <y>", returning the label separately.
//
// Read from the END rather than matched against a known prefix: the label is
// whatever the game feels like calling the place, and the part that has to be
// right is the two numbers. Requiring a label token means a bare "1058 x 1016",
// which nobody has seen, is reported as unparsed rather than quietly accepted.
//
// By hand rather than with a regexp because it runs on every activity frame.
func parseBlockPosition(text string) (place string, x, y int, ok bool) {
	f := strings.Fields(text)
	// label + x + "x" + y, so four fields at least.
	if len(f) < 4 || f[len(f)-2] != "x" {
		return "", 0, 0, false
	}
	x, err := strconv.Atoi(f[len(f)-3])
	if err != nil {
		return "", 0, 0, false
	}
	y, err = strconv.Atoi(f[len(f)-1])
	if err != nil {
		return "", 0, 0, false
	}
	return strings.Join(f[:len(f)-3], " "), x, y, true
}

// presenceFrame is the envelope every message shares. args is deliberately
// json.RawMessage: a SUBSCRIBE carries a literal null there, and decoding that
// into a map would be a nil dereference on the happy path.
type presenceFrame struct {
	Cmd   string          `json:"cmd"`
	Nonce string          `json:"nonce"`
	Evt   string          `json:"evt"`
	Args  json.RawMessage `json:"args"`
}

type presenceActivityArgs struct {
	PID      int             `json:"pid"`
	Activity json.RawMessage `json:"activity"`
}

type presenceActivity struct {
	Details string `json:"details"`
	State   string `json:"state"`
}

func readPresenceFrame(r io.Reader) (uint32, []byte, error) {
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	op := binary.LittleEndian.Uint32(head[0:4])
	length := binary.LittleEndian.Uint32(head[4:8])
	if length > presenceMaxFrame {
		return 0, nil, fmt.Errorf("presence: frame of %d bytes is over the %d limit", length, presenceMaxFrame)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return op, body, nil
}

func writePresenceFrame(w io.Writer, op uint32, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var head [8]byte
	binary.LittleEndian.PutUint32(head[0:4], op)
	binary.LittleEndian.PutUint32(head[4:8], uint32(len(body)))
	if _, err := w.Write(append(head[:], body...)); err != nil {
		return err
	}
	return nil
}

// PresenceServer is the Discord IPC endpoint df-hud pretends to be.
//
// It answers the handshake and every command with the minimum that keeps the
// game's SDK talking, and takes nothing from it but the activity. Refusing to
// answer is not an option: the SDK treats a silent peer as a failure and stops
// publishing, which would cost the very thing this exists for.
type PresenceServer struct {
	path    string
	onState func(PresenceState)

	mu       sync.RWMutex
	last     PresenceState
	haveLast bool
	clients  int
}

func newPresenceServer(path string) *PresenceServer {
	return &PresenceServer{path: path}
}

func (p *PresenceServer) SetOnState(fn func(PresenceState)) {
	p.mu.Lock()
	p.onState = fn
	p.mu.Unlock()
}

// Last is the most recent state, for diagnostics.
func (p *PresenceServer) Last() (PresenceState, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.last, p.haveLast
}

// defaultPresenceSocket is where Discord's own client would listen. The game
// tries discord-ipc-0 first, so that is where df-hud has to be to be found.
func defaultPresenceSocket() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir())
	}
	return filepath.Join(dir, "discord-ipc-0")
}

// Run serves until the context ends.
//
// FAILS OPEN, always. Everything this provides is an improvement on a source
// df-hud already has, so a socket that cannot be bound - most likely a real
// Discord or Vesktop already there - is logged once and then dropped. Taking the
// HUD down over it would trade a working position for a better one.
func (p *PresenceServer) Run(ctx context.Context) {
	listener, err := p.listen()
	if err != nil {
		log.Printf("presence: not listening (%v); position will come from the poll only", err)
		return
	}
	defer func() {
		listener.Close()
		os.Remove(p.path)
	}()
	log.Printf("presence: listening on %s", p.path)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("presence: accept: %v", err)
			return
		}
		go p.serve(ctx, conn)
	}
}

// listen binds the socket, clearing a stale one left by a crash.
//
// A socket file that nothing is listening on is ours to remove; one with a live
// peer is somebody else's and is left alone. Dialling it is the only way to tell
// the two apart, since both are just a file on disk.
func (p *PresenceServer) listen() (net.Listener, error) {
	if _, err := os.Stat(p.path); err == nil {
		conn, dialErr := net.DialTimeout("unix", p.path, 300*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("%s is already served by something else", p.path)
		}
		if err := os.Remove(p.path); err != nil {
			return nil, fmt.Errorf("removing the stale socket at %s: %w", p.path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return nil, err
	}
	return net.Listen("unix", p.path)
}

func (p *PresenceServer) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	p.mu.Lock()
	p.clients++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.clients--
		p.mu.Unlock()
	}()

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		op, body, err := readPresenceFrame(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				log.Printf("presence: connection ended (%v)", err)
			}
			return
		}
		if err := p.handle(conn, op, body); err != nil {
			log.Printf("presence: %v", err)
			return
		}
	}
}

func (p *PresenceServer) handle(conn net.Conn, op uint32, body []byte) error {
	switch op {
	case presenceOpHandshake:
		// The SDK waits for a READY dispatch before it will publish anything, so
		// this reply is load-bearing rather than politeness. The user block is
		// filled with placeholders: df-hud is not Discord and has no account to
		// describe, and the game only reads it to display an avatar it will never
		// show on an overlay that is not Discord.
		return writePresenceFrame(conn, presenceOpFrame, map[string]any{
			"cmd": "DISPATCH",
			"evt": "READY",
			"data": map[string]any{
				"v":      1,
				"config": map[string]any{"api_endpoint": "//discord.com/api", "environment": "production"},
				"user":   map[string]any{"id": "0", "username": "df-hud", "discriminator": "0000"},
			},
		})

	case presenceOpPing:
		return writePresenceFrame(conn, presenceOpPong, json.RawMessage(body))

	case presenceOpClose:
		return io.EOF
	}

	var frame presenceFrame
	if err := json.Unmarshal(body, &frame); err != nil {
		// Not fatal: an unparsable frame is one message lost, not a broken peer.
		log.Printf("presence: unparsable frame (%v)", err)
		return nil
	}
	if frame.Cmd == "SET_ACTIVITY" {
		p.applyActivity(frame.Args)
	}
	// Every command gets an acknowledgement, including the ones df-hud ignores.
	// An unanswered nonce is what the SDK counts as an error.
	return writePresenceFrame(conn, presenceOpFrame, map[string]any{
		"cmd": frame.Cmd, "evt": nil, "nonce": frame.Nonce, "data": nil,
	})
}

func (p *PresenceServer) applyActivity(args json.RawMessage) {
	if len(args) == 0 {
		return
	}
	var parsed presenceActivityArgs
	if err := json.Unmarshal(args, &parsed); err != nil || len(parsed.Activity) == 0 {
		return
	}
	var activity presenceActivity
	if err := json.Unmarshal(parsed.Activity, &activity); err != nil {
		return
	}
	state := parsePresenceDetails(activity.Details, time.Now())

	p.mu.Lock()
	// An unrecognised details string is logged ONCE per distinct value. The game
	// can change this format whenever it likes, and the failure would otherwise
	// be silent: position quietly falling back to the poll with nothing to say
	// why.
	unknown := state.Details != "" && !state.HasPosition && !state.InOutpost && !state.Loading &&
		(!p.haveLast || p.last.Details != state.Details)
	// One line per change of KIND, not per frame. Walking publishes a frame every
	// few seconds and none of those are interesting; loading -> city is, and it is
	// the only record of what the client says across a transition. Without it the
	// feed is silent by design and questions like "how early does it say you are
	// in the city" cannot be answered after the fact.
	kind := presenceKind(state)
	kindChanged := !p.haveLast || presenceKind(p.last) != kind
	p.last, p.haveLast = state, true
	fn := p.onState
	p.mu.Unlock()

	if kindChanged {
		log.Printf("presence: %s", kind)
	}
	if unknown {
		log.Printf("presence: unrecognised details %q - position still coming from the poll", state.Details)
	}
	if fn != nil {
		fn(state)
	}
}

// presenceKind is the state reduced to what changes rarely, so a log line can be
// emitted per transition rather than per frame. Includes the block, since moving
// between blocks is the one position change worth seeing in a journal.
func presenceKind(s PresenceState) string {
	switch {
	case s.Loading:
		return "loading"
	case s.InOutpost:
		return "outpost " + s.OutpostName
	case s.HasPosition && s.Indoors:
		return fmt.Sprintf("%s %d,%d", s.Place, s.X, s.Y)
	case s.HasPosition:
		return fmt.Sprintf("inner city %d,%d", s.X, s.Y)
	case s.Details == "":
		return "nothing"
	}
	return "unparsed"
}
