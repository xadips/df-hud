package main

import (
	"strings"
	"testing"
	"time"
)

func mapCfg() MapWidgetConfig {
	return MapWidgetConfig{Enabled: true, CellSize: 17, Opacity: 1, ShowList: true, MaxListed: 20}
}

// mark is one location of one event, as ActiveMarks would produce it.
func mark(marker string, blocks int, reachable bool, ends time.Duration, enemies ...string) CityMark {
	return CityMark{
		Marker: marker, Label: strings.Join(enemies, " + "), Enemies: enemies,
		X: 1020, Y: 1000, EndsIn: ends,
		Walk: cityWalk{Blocks: blocks}, Reachable: reachable,
	}
}

// One key per event, not per block. The feed puts the same bandit pack on a dozen
// blocks at once - 185 marks from 30 events in a live capture - and a row each meant
// the same enemies and the same countdown twelve times over, then "+173 more".
func TestMapListCollapsesAnEventToOneEntry(t *testing.T) {
	v := &View{CityMarks: []CityMark{
		mark("1", 9, true, time.Minute, "6 x Bandits"),
		mark("1", 3, true, time.Minute, "6 x Bandits"),
		mark("1", 14, true, time.Minute, "6 x Bandits"),
	}}
	rows := mapListLines(v, mapCfg())
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want one entry for one event: %#v", len(rows), rows)
	}
	if rows[0].Marker != "1" || !strings.Contains(rows[0].Text, "Bandits") {
		t.Errorf("row = %#v", rows[0])
	}
	// And it reports the block you would actually walk to, which is the nearest.
	if !strings.Contains(rows[0].Timer, "1m") {
		t.Errorf("timer = %q, want the countdown", rows[0].Timer)
	}
}

// Ordered by the walk to the nearest of an event's blocks, so the top of the list is
// what you can reach. Off the map goes last: Onslaught is a real coordinate but not
// somewhere you can walk.
func TestMapListOrdersByTheWalk(t *testing.T) {
	off := mark("z", 0, false, time.Minute, "Onslaught boss")
	off.OffMap = true
	v := &View{CityMarks: []CityMark{
		mark("a", 12, true, time.Minute, "far"),
		off,
		mark("b", 2, true, time.Minute, "near"),
		mark("c", 0, false, time.Minute, "unknown distance"),
	}}
	var markers []string
	for _, r := range mapListLines(v, mapCfg()) {
		if r.Marker != "" {
			markers = append(markers, r.Marker)
		}
	}
	want := []string{"b", "a", "c", "z"}
	if strings.Join(markers, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", markers, want)
	}
}

// A single enemy type is one line. A nest is the headline plus a row each, because
// seven of them joined is 140 characters and runs off the side of the screen - taking
// the part that says what is dangerous with it.
func TestMapListSplitsANest(t *testing.T) {
	nest := mark("4", 7, true, 30*time.Second,
		"3 x Evolved Longarms", "1 x Irradiated Wraith", "1 x Mega Mother")
	v := &View{CityMarks: []CityMark{mark("1", 1, true, time.Minute, "6 x Bandits"), nest}}
	rows := mapListLines(v, mapCfg())
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 1 for the pack and 3 for the nest: %#v", len(rows), rows)
	}
	if rows[0].Marker == "" || rows[0].Sub {
		t.Errorf("the single pack should be one headline row: %#v", rows[0])
	}
	if rows[1].Marker != "4" || rows[1].Text != "3 x Evolved Longarms" {
		t.Errorf("the nest's first type belongs on its headline: %#v", rows[1])
	}
	for _, r := range rows[2:] {
		if r.Marker != "" || !r.Sub {
			t.Errorf("the rest of the nest should be indented continuations: %#v", r)
		}
	}
}

// A mission has no enemy list, and its name is the thing worth saying.
func TestMapListNamesAMission(t *testing.T) {
	m := CityMark{Marker: "7", Label: "mission: The Clue", Kind: EventMission,
		X: 1002, Y: 1000, Walk: cityWalk{Blocks: 4}, Reachable: true}
	rows := mapListLines(&View{CityMarks: []CityMark{m}}, mapCfg())
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "The Clue") {
		t.Errorf("rows = %#v", rows)
	}
}

// What is dropped is counted. A list that stops without saying so reads as "that is
// everything", which is the one thing it is not.
func TestMapListSaysWhatItDropped(t *testing.T) {
	cfg := mapCfg()
	cfg.MaxListed = 2
	v := &View{CityMarks: []CityMark{
		mark("1", 1, true, 0, "a"),
		mark("2", 2, true, 0, "b"),
		mark("3", 3, true, 0, "c"),
		mark("4", 4, true, 0, "d"),
	}}
	rows := mapListLines(v, cfg)
	last := rows[len(rows)-1]
	if last.Marker != "" || last.Text != "+2 more" {
		t.Errorf("last row = %#v, want a count of what was dropped", last)
	}
}

// Onslaught says so, because a row with only a countdown on it would look like
// somewhere you could walk.
func TestMapListMarksOnslaught(t *testing.T) {
	off := mark("z", 0, false, 4*time.Minute, "3 x Mega Wraith")
	off.OffMap = true
	rows := mapListLines(&View{CityMarks: []CityMark{off}}, mapCfg())
	if len(rows) == 0 || !strings.Contains(rows[0].Timer, "Onslaught") {
		t.Errorf("rows = %#v", rows)
	}
}

// The markup is where the identifier gets its chip and its colour, and everything
// from the feed has to be escaped before it reaches Pango - a malformed span makes
// GTK render an EMPTY label, so one boss named with an ampersand would blank the
// whole key.
func TestMapListMarkupEscapesAndStyles(t *testing.T) {
	v := &View{CityMarks: []CityMark{mark("1", 2, true, time.Minute, "6 x Bandits & friends")}}
	got := mapListMarkup(v, mapCfg())
	if !strings.Contains(got, "&amp;") {
		t.Errorf("the ampersand was not escaped: %q", got)
	}
	if strings.Contains(got, "& ") {
		t.Errorf("raw ampersand left in the markup: %q", got)
	}
	if !strings.Contains(got, mapMarkerColor) || !strings.Contains(got, mapMarkerChip) {
		t.Errorf("the identifier lost its colour or its chip: %q", got)
	}
}

func TestMapListEmpty(t *testing.T) {
	if rows := mapListLines(&View{}, mapCfg()); rows != nil {
		t.Errorf("rows = %#v, want nothing with no events", rows)
	}
	if got := mapListMarkup(&View{}, mapCfg()); got != "" {
		t.Errorf("markup = %q, want empty", got)
	}
}
