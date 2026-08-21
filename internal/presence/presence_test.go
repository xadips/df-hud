package presence

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"df-hud/internal/citymap"
)

// The four forms captured live on 2026-08-16, plus the ones that must NOT be
// mistaken for a position.
func TestParsePresenceDetails(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		details string
		want    PresenceState
	}{
		{"Inner City 1054 x 986", PresenceState{HasPosition: true, X: 1054, Y: 986, Place: "Inner City"}},
		{"Inner City 1055 x 985", PresenceState{HasPosition: true, X: 1055, Y: 985, Place: "Inner City"}},
		// Inside a building: same block, labelled with the building.
		{"Hospital 1058 x 1016", PresenceState{HasPosition: true, X: 1058, Y: 1016, Place: "Hospital", Indoors: true}},
		{"Secronom Bunker", PresenceState{InOutpost: true, OutpostName: "Secronom Bunker"}},
		{"Nastya's Holdout", PresenceState{InOutpost: true, OutpostName: "Nastya's Holdout"}},
		{"Loading...", PresenceState{Loading: true}},
		{"", PresenceState{}},
		// Not an outpost we know, so it is reported as unparsed rather than
		// filed as one - the alternative is inventing an outpost from a string
		// the game changed the format of.
		{"Somewhere New", PresenceState{}},
		{"Inner City 1054", PresenceState{}},
		{"Inner City abc x def", PresenceState{}},
	} {
		got := parsePresenceDetails(tc.details, now)
		want := tc.want
		want.At, want.Details = now, tc.details
		if got != want {
			t.Errorf("parsePresenceDetails(%q):\n got %+v\nwant %+v", tc.details, got, want)
		}
	}
}

// The client publishes coordinates in the same space as df_positionx/y, so a
// parsed position must land on a real block rather than needing a transform.
func TestPresencePositionIsACityBlock(t *testing.T) {
	got := parsePresenceDetails("Inner City 1054 x 986", time.Unix(1000, 0))
	if !got.HasPosition {
		t.Fatal("expected a position")
	}
	if !citymap.Default().IsBlock(got.X, got.Y) {
		t.Errorf("%d,%d is not a block in the city map - the coordinate spaces disagree", got.X, got.Y)
	}
}

func TestPresenceFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writePresenceFrame(&buf, presenceOpFrame, map[string]string{"cmd": "SET_ACTIVITY"}); err != nil {
		t.Fatal(err)
	}
	// Little-endian opcode and length, which is the part a hand-rolled codec gets
	// wrong and no unit test above would notice.
	if op := binary.LittleEndian.Uint32(buf.Bytes()[0:4]); op != presenceOpFrame {
		t.Errorf("opcode = %d, want %d", op, presenceOpFrame)
	}
	if n := binary.LittleEndian.Uint32(buf.Bytes()[4:8]); int(n) != buf.Len()-8 {
		t.Errorf("length = %d, want %d", n, buf.Len()-8)
	}
	op, body, err := readPresenceFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if op != presenceOpFrame || string(body) != `{"cmd":"SET_ACTIVITY"}` {
		t.Errorf("round trip = %d %s", op, body)
	}
}

// A frame claiming a huge length must be refused rather than allocated.
func TestPresenceFrameRefusesAbsurdLength(t *testing.T) {
	var head [8]byte
	binary.LittleEndian.PutUint32(head[0:4], presenceOpFrame)
	binary.LittleEndian.PutUint32(head[4:8], 1<<30)
	if _, _, err := readPresenceFrame(bytes.NewReader(head[:])); err == nil {
		t.Error("expected a frame over the size limit to be refused")
	}
}

// SUBSCRIBE carries a literal null for args, which is what crashed the first
// capture script. It must be a no-op here, not a panic.
func TestPresenceHandlesNullArgs(t *testing.T) {
	p := newPresenceServer("")
	var got int
	p.SetOnState(func(PresenceState) { got++ })
	p.applyActivity(json.RawMessage(`null`))
	p.applyActivity(nil)
	p.applyActivity(json.RawMessage(`{"pid":1}`))
	if got != 0 {
		t.Errorf("onState fired %d times for frames carrying no activity", got)
	}
}

func TestPresenceAppliesActivity(t *testing.T) {
	p := newPresenceServer("")
	var got PresenceState
	p.SetOnState(func(s PresenceState) { got = s })
	p.applyActivity(json.RawMessage(`{"pid":42,"activity":{"details":"Inner City 1054 x 986","state":"Multiplayer"}}`))
	if !got.HasPosition || got.X != 1054 || got.Y != 986 {
		t.Errorf("got %+v, want position 1054,986", got)
	}
	if last, ok := p.Last(); !ok || last.X != 1054 {
		t.Errorf("Last() = %+v %v", last, ok)
	}
}
