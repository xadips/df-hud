package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What is standing on your block: bosses, bandit packs, missions and QRF events.
//
// The one feature whose data is not in the game's own API - the stats feed has
// the map's geometry and the player record knows where YOU are, but neither knows
// what has spawned. DFProfiler publishes it.
//
// Somebody else's site, run by a person rather than an API vendor, so: their own
// bossmap page polls this every 30s per open tab (bossmap.js: setTimeout(I, 3e4))
// and df-hud's defaults stay at or under that; nothing at all while the game is
// closed; one jittered request per interval with exponential backoff; and a
// User-Agent naming the tool so the operator can identify and complain about it.
//
// The endpoint 404s without `X-Requested-With: XMLHttpRequest`, so that is sent -
// the convention their API is written against, not a claim to be a browser. A
// forged Referer WOULD be such a claim, so none is sent, and it turns out not to
// be needed. Their jQuery's `_=<millis>` cache-buster is left off too, so any
// cache in between is free to help.
const defaultBossMapURL = "https://www.dfprofiler.com/bossmap/json/"

// bossMapPastWindow is how long a finished event stays worth mentioning. Wider
// than one Onslaught cycle because Onslaught skips slots often enough that the
// last SPAWN can be more than five minutes back - the onslaught_bosses userscript
// paid for this lesson first, trying 6 minutes and losing "prev" too early.
const bossMapPastWindow = 12 * time.Minute

// onslaughtCoord is the block Onslaught events sit on.
//
// Their page treats it as "off screen" since the city map has no such cell, but a
// player IN Onslaught reports df_positionx/y of exactly 3000,3000 - so it is a
// real coordinate in the same space, and indexing it like any other block makes
// those cycles surface exactly when you are there and never otherwise.
const onslaughtCoord = 3000

// CityEventKind is what sort of thing an event is. The classification order is a
// port of bossmap.js and is load-bearing: a mission also carries a
// special_enemy_type, so testing that first files every mission as a plain spawn.
type CityEventKind int

const (
	EventSpawn   CityEventKind = iota // bosses, bandits, anything with a special enemy type
	EventMission                      // has a briefing; the game's own mission board
	EventQRF                          // quick reaction force
	EventUnknown                      // an event with no type at all ("Unknown GPS")
)

func (k CityEventKind) String() string {
	switch k {
	case EventMission:
		return "mission"
	case EventQRF:
		return "qrf"
	case EventUnknown:
		return "unknown"
	}
	return "spawn"
}

// CityEvent is one entry in the feed.
type CityEvent struct {
	ID    string
	Kind  CityEventKind
	Title string

	// Enemies is already in "6 x Bandits" form. Several entries mean several
	// types on the same block, which the feed joins with <br /> in one string.
	Enemies []string
	// Objectives is the mission's task list, formatted as their own page does.
	Objectives []string
	RewardExp  int64

	// Slot is the feed's boss_num: the game's own event slots, numbered per
	// event type, and what the map's identifiers are built from.
	//
	// Within a type the slots ascend with difficulty. Measured on a real cycle:
	// bandit camps at 1, 2, 3, 14, 16 carrying 1, 2, 2, 4 and 6 bandits; nests at
	// 4..11, from a pair of Flaming Zombies up to a bear pit; single-type bosses
	// at 17..27. So ranking the active events of one category by slot gives
	// B1..B6 in the order the city gets harder, which is what their map means.
	//
	// For a mission the slot IS the outpost: 0 Nastya's, 1 Fort Pastor, 2 Dogg's,
	// 3 Precinct 13, 4 Secronom. Hence M1..M5 with no ranking at all.
	Slot int

	Locations [][2]int
	Start     time.Time
	End       time.Time

	// startedFlag and endedFlag are the feed's own booleans, used only when an
	// event carries no usable timestamps. See ActiveAt.
	startedFlag bool
	endedFlag   bool

	// Onslaught marks the cycles that run in the instanced mode rather than out
	// on the city map. They shift every five minutes, the tightest cycle here.
	Onslaught bool
}

// ActiveAt reports whether this event is happening at a given instant.
//
// From the event's own timestamps rather than the started/ended flags, which is
// the whole trick: the feed carries the NEXT cycle before it begins (seen around
// :59 for a cycle starting at :00) and keeps the PREVIOUS one for minutes after.
// Deciding from the clock means one fetch stays correct straight through a
// changeover, with no request at the moment it matters.
//
// The flags are the fallback for an event with no usable timestamps.
func (e CityEvent) ActiveAt(now time.Time) bool {
	if e.Start.IsZero() || e.End.IsZero() {
		return e.startedFlag && !e.endedFlag
	}
	return !now.Before(e.Start) && now.Before(e.End)
}

// UpcomingAt is an event with a slot that has not begun.
func (e CityEvent) UpcomingAt(now time.Time) bool {
	if e.Start.IsZero() {
		return !e.startedFlag && !e.endedFlag
	}
	return now.Before(e.Start)
}

// EndedRecentlyAt is last cycle's, while "last cycle" still means something.
func (e CityEvent) EndedRecentlyAt(now time.Time) bool {
	if e.End.IsZero() {
		return e.endedFlag
	}
	return !now.Before(e.End) && now.Sub(e.End) <= bossMapPastWindow
}

// Label is the one-line form for the HUD.
func (e CityEvent) Label() string {
	switch e.Kind {
	case EventMission:
		if e.Title != "" {
			return "mission: " + e.Title
		}
		return "mission"
	case EventQRF:
		if e.Title != "" {
			return e.Title
		}
		return "quick reaction force"
	case EventUnknown:
		return "unknown GPS"
	}
	if len(e.Enemies) == 0 {
		return "something"
	}
	return strings.Join(e.Enemies, " + ")
}

// BossMap is one fetch of the feed, indexed by block.
type BossMap struct {
	FetchedAt time.Time

	// ServerTime is the feed's own servertime, which is NOT a clock to compare
	// against - it is how fresh their data is. Measured 2026-08-17 against an
	// NTP-synced local clock: 19s behind, then 53s behind 14 minutes later, so
	// it is when their backend last synced with the game rather than what time
	// it is there.
	//
	// Every timestamp in the feed is an absolute unix second landing on the
	// game's own schedule (Onslaught's are all `unix % 300 == 2`), so the local
	// clock is what they are compared against. Treating this field as a clock
	// offset used to delay every changeover by however stale the data was.
	ServerTime time.Time

	// Hash is the feed's own change marker (bosshash). Their page uses it to
	// decide whether anything moved; df-hud uses it only to log that it did.
	Hash   string
	Events []CityEvent

	// OutpostAttack is the feed's isoa flag: every outpost under attack, map-wide
	// rather than per-block.
	OutpostAttack bool

	byBlock map[[2]int][]int
}

// At returns the events happening on one block at a given instant.
//
// Upcoming ones are excluded: "a titan will be here in forty minutes" is planning
// information, not something to put in front of someone being chased. They stay
// in the map, which is what lets a cached fetch become correct on its own.
func (b *BossMap) At(x, y int, now time.Time) []CityEvent {
	return b.eventsAt(x, y, func(e CityEvent) bool { return e.ActiveAt(now) })
}

// AtEnded returns the previous cycle's events on one block, narrowed to whichever
// ended most recently.
//
// Only useful where cycles are short enough to overlap, which means Onslaught.
// The caller decides whether to ask: out in the city the previous cycle is a boss
// that has gone, and reporting it would send you somewhere for nothing.
//
// Narrowed rather than returning everything inside bossMapPastWindow, which is
// wide enough to span more than one past cycle when Onslaught skips slots.
func (b *BossMap) AtEnded(x, y int, now time.Time) []CityEvent {
	events := b.eventsAt(x, y, func(e CityEvent) bool { return e.EndedRecentlyAt(now) })
	return edgeGroup(events, func(e CityEvent) time.Time { return e.End }, afterEdge)
}

// AtUpcoming returns the next cycle's events on one block, narrowed to whichever
// starts soonest. Onslaught only in practice, same as AtEnded.
func (b *BossMap) AtUpcoming(x, y int, now time.Time) []CityEvent {
	events := b.eventsAt(x, y, func(e CityEvent) bool { return e.UpcomingAt(now) })
	return edgeGroup(events, func(e CityEvent) time.Time { return e.Start }, beforeEdge)
}

// edgeGroup narrows events to whichever share the most extreme value of field. A
// group rather than one event, because a slot can carry several enemy types.
//
// Ported from the onslaught_bosses userscript, for the reason it exists there:
// the window a "previous" or "upcoming" set is drawn from can span more than one
// cycle, and blending two of them into one unlabelled group is worse than only
// ever showing the nearest.
func edgeGroup(events []CityEvent, field func(CityEvent) time.Time, moreExtreme func(candidate, currentEdge time.Time) bool) []CityEvent {
	if len(events) == 0 {
		return nil
	}
	edge := field(events[0])
	for _, e := range events {
		if t := field(e); moreExtreme(t, edge) {
			edge = t
		}
	}
	var out []CityEvent
	for _, e := range events {
		if field(e).Equal(edge) {
			out = append(out, e)
		}
	}
	return out
}

func afterEdge(candidate, currentEdge time.Time) bool  { return candidate.After(currentEdge) }
func beforeEdge(candidate, currentEdge time.Time) bool { return candidate.Before(currentEdge) }

// CityMark is one active event at one place, ready to draw. Both the map group
// and the "which way to walk" row are built from these, so the two cannot
// disagree about what is on the map or how far away it is.
type CityMark struct {
	// Marker is the character drawn in the cell and repeated in the key.
	Marker string
	Label  string
	// Enemies is the types standing there, separately, because a nest can carry
	// seven at once and Label joins them into a 140-character line.
	Enemies []string
	Kind    CityEventKind
	X, Y    int
	// EndsIn is how long is left, zero when the feed gave no usable end time.
	EndsIn time.Duration

	// OffMap is Onslaught: a real coordinate, but not a place on the city grid
	// and not somewhere you can walk.
	OffMap bool
	// Walk is the route from where the player is standing, valid only when
	// Reachable - which excludes standing in Onslaught, standing nowhere known,
	// and the mark being off the map.
	Walk      cityWalk
	Reachable bool
}

// eventMarkers is the identifier each active event is drawn with.
//
// The scheme is DFProfiler's, because their map is the one everybody reads: a
// letter for what sort of place it is and a number for which one, so B4 means the
// same camp to everyone.
//
//	B1..B6   bandit camps, ascending with the endgame
//	I1..In   inner city bosses, one enemy type
//	N1..Nn   nests, several types on one block
//	M1..M5   missions, one per outpost
//	Δ        a QRF, numbered only when there is more than one
//	DH VL BH LB   today's daily, whichever it is
//
// The numbers come from the game's own slots (see CityEvent.Slot), so an
// identifier means the same place all cycle and the same place it means on their
// map. It deliberately does NOT renumber by distance: a marker that changed as
// you walked could not be said out loud to anyone.
//
// The daily takes initials instead of a number, but only standing alone - a nest
// containing today's boss keeps its N number, because a nest of six things is not
// "the Devil Hound". The ring around it carries that.
func eventMarkers(events []CityEvent) []string {
	// Rank slots per category first, so a category present as 1, 2, 3, 14, 16
	// draws as 1, 2, 3, 4, 5.
	ranks := make(map[markCategory]map[int]int)
	for _, e := range events {
		cat := markCategoryOf(e.Kind, e.Enemies)
		switch cat {
		case markBoss, markNest, markBandits:
			if dailyMarker(e.Enemies) != "" && cat != markNest {
				continue // takes its initials, so it is not in the numbering
			}
		default:
			// Missions are numbered by their own slot; QRFs all carry slot 0.
			continue
		}
		if ranks[cat] == nil {
			ranks[cat] = make(map[int]int)
		}
		ranks[cat][e.Slot] = 0
	}
	for _, slots := range ranks {
		ordered := make([]int, 0, len(slots))
		for slot := range slots {
			ordered = append(ordered, slot)
		}
		sort.Ints(ordered)
		for i, slot := range ordered {
			slots[slot] = i + 1
		}
	}

	// QRFs are counted rather than ranked: every one arrives on slot 0 - measured,
	// two at once in the capture - so ranking would give them the same number.
	qrfTotal, qrfSeen := 0, 0
	for _, e := range events {
		if markCategoryOf(e.Kind, e.Enemies) == markQRF {
			qrfTotal++
		}
	}

	out := make([]string, len(events))
	for i, e := range events {
		cat := markCategoryOf(e.Kind, e.Enemies)
		daily := dailyMarker(e.Enemies)
		switch {
		case daily != "" && cat != markNest:
			out[i] = daily
		case cat == markMission:
			// The slot is the outpost, zero-based on the wire.
			out[i] = "M" + strconv.Itoa(e.Slot+1)
		case cat == markQRF:
			// A number on the only one of something says there are others.
			qrfSeen++
			out[i] = qrfMarker
			if qrfTotal > 1 {
				out[i] += strconv.Itoa(qrfSeen)
			}
		case cat == markBandits:
			out[i] = "B" + strconv.Itoa(ranks[cat][e.Slot])
		case cat == markNest:
			out[i] = "N" + strconv.Itoa(ranks[cat][e.Slot])
		case cat == markBoss:
			out[i] = "I" + strconv.Itoa(ranks[cat][e.Slot])
		default:
			out[i] = "?"
		}
	}
	return out
}

// qrfMarker is the triangle their map uses, kept as their glyph so a screenshot
// of this map and one of theirs say the same thing.
const qrfMarker = "Δ"

// dailyBosses are the rotating daily events, each with initials of its own.
//
// Initials rather than a number because the question about a daily is WHICH one
// it is - it is what you logged in for - and "I3" answers that only via the key.
//
// Matched on the full name as a substring, which is not fussiness: "hound" alone
// would file every Flaming Flesh Hound as a Devil Hound, and those are seven
// blocks of walking apart in difficulty. A Charred Devil Hound still matches.
var dailyBosses = []struct{ Name, Marker string }{
	{"devil hound", "DH"},
	{"volatile leaper", "VL"},
	{"behemoth", "BH"},
}

// legendaryBanditPack is the size at which a bandit camp is the daily rather than
// one of the six standing camps: the daily pack is eight, the largest ordinary
// camp observed is six.
const legendaryBanditPack = 8

// dailyMarker returns the initials for today's daily if these enemies are it.
func dailyMarker(enemies []string) string {
	for _, e := range enemies {
		low := strings.ToLower(e)
		for _, d := range dailyBosses {
			if strings.Contains(low, d.Name) {
				return d.Marker
			}
		}
		if strings.Contains(low, "bandit") && enemyCount(e) >= legendaryBanditPack {
			return "LB"
		}
	}
	return ""
}

// enemyCount is the number at the front of "6 x Bandits", or 0.
func enemyCount(enemy string) int {
	n, _, ok := strings.Cut(enemy, " x ")
	if !ok {
		return 0
	}
	count, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil {
		return 0
	}
	return count
}

// ActiveMarks is everything happening on the map right now.
//
// dist is a walk-distance table from where the player is standing, or nil when
// that is unknown - one breadth-first search shared by every mark.
func (b *BossMap) ActiveMarks(now time.Time, from [2]int, dist []int32) []CityMark {
	if b == nil {
		return nil
	}
	// Onslaught is left out unless you are in it: out in the city those are events
	// you can do nothing about, several every cycle. Filtered HERE rather than
	// where the list is built, so their identifiers are never assigned - otherwise
	// the letters on the map would have gaps, which looks like a drawing failure.
	inOnslaught := from[0] == onslaughtCoord && from[1] == onslaughtCoord

	// Two passes, because identifiers are ranked WITHIN the active set: B1 is the
	// first bandit camp actually up, which cannot be known while still walking.
	active := make([]CityEvent, 0, len(b.Events))
	for _, e := range b.Events {
		if !e.ActiveAt(now) {
			continue
		}
		if e.Onslaught && !inOnslaught {
			continue
		}
		active = append(active, e)
	}
	markers := eventMarkers(active)

	var out []CityMark
	for i, e := range active {
		char := markers[i]
		var ends time.Duration
		if !e.End.IsZero() {
			if left := e.End.Sub(now); left > 0 {
				ends = left
			}
		}
		for _, loc := range e.Locations {
			m := CityMark{
				Marker: char, Label: e.Label(), Enemies: e.Enemies, Kind: e.Kind,
				X: loc[0], Y: loc[1], EndsIn: ends,
				OffMap: loc[0] == onslaughtCoord && loc[1] == onslaughtCoord,
			}
			if !m.OffMap && dist != nil {
				m.Walk, m.Reachable = theCity.routeFrom(dist, from[0], from[1], m.X, m.Y)
			}
			out = append(out, m)
		}
	}
	return out
}

// nearestMark is the closest active event to walk to, for when your own block has
// nothing on it.
//
// Closest is by WALKING, not by subtracting coordinates: the city is not a full
// rectangle (see citymap.go), so a boss five blocks up can be nine blocks away
// around a gap while one seven blocks east is seven.
//
// Ties go to the first seen, which is the feed's order, so the row does not
// flicker between two equidistant bosses every poll.
func nearestMark(marks []CityMark) (CityMark, bool) {
	var best CityMark
	found := false
	for _, m := range marks {
		if !m.Reachable || m.Walk.Blocks == 0 {
			// Zero is your own block, which the caller already has from At.
			continue
		}
		if !found || m.Walk.Blocks < best.Walk.Blocks {
			best, found = m, true
		}
	}
	return best, found
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (b *BossMap) eventsAt(x, y int, keep func(CityEvent) bool) []CityEvent {
	if b == nil {
		return nil
	}
	var out []CityEvent
	for _, i := range b.byBlock[[2]int{x, y}] {
		if keep(b.Events[i]) {
			out = append(out, b.Events[i])
		}
	}
	return out
}

// NextBoundary is the next instant at which this map's answer changes: the
// earliest start or end still ahead of now, in local time. Zero when there is
// none.
//
// This is what replaces polling on a fixed timer - the feed states exactly when
// it will next be wrong, so the schedule comes from the data.
//
// withOnslaught decides whether the five-minute cycles count. They are always in
// the feed, so including them unconditionally would pull the whole schedule down
// to five minutes for someone who cannot see them.
func (b *BossMap) NextBoundary(now time.Time, withOnslaught bool) time.Time {
	if b == nil {
		return time.Time{}
	}
	var best time.Time
	consider := func(t time.Time) {
		if t.IsZero() || !t.After(now) {
			return
		}
		if best.IsZero() || t.Before(best) {
			best = t
		}
	}
	for _, e := range b.Events {
		if e.Onslaught && !withOnslaught {
			continue
		}
		consider(e.Start)
		consider(e.End)
	}
	return best
}

// Horizon is how far into the future this map's data already reaches: the latest
// end time among its events. What bossMapPublishWindow is measured back from.
func (b *BossMap) Horizon(onslaughtOnly bool) time.Time {
	if b == nil {
		return time.Time{}
	}
	var edge time.Time
	for _, e := range b.Events {
		if onslaughtOnly && !e.Onslaught {
			continue
		}
		if e.End.After(edge) {
			edge = e.End
		}
	}
	return edge
}

// BlockBoundary is the next instant at which what shows on ONE block changes.
// Zero when there is none.
//
// Where NextBoundary schedules polling across the whole map, this drives a
// countdown the player is watching. Onslaught is the only real user: its cycle is
// short enough to be worth a ticking clock, and every Onslaught event sits on the
// same block.
//
// Reaching zero and the panel's rows shifting are the same instant by
// construction, because both read the same clock: at the boundary the ended
// cycle fails ActiveAt and becomes prev, the one starting there becomes now, and
// this returns the next boundary after it. Nothing shifts the rows on a timer.
func (b *BossMap) BlockBoundary(x, y int, now time.Time) time.Time {
	if b == nil {
		return time.Time{}
	}
	var best time.Time
	consider := func(t time.Time) {
		if t.IsZero() || !t.After(now) {
			return
		}
		if best.IsZero() || t.Before(best) {
			best = t
		}
	}
	for _, i := range b.byBlock[[2]int{x, y}] {
		e := b.Events[i]
		consider(e.Start)
		consider(e.End)
	}
	return best
}

// Age is how stale this fetch is, for deciding whether to trust it.
func (b *BossMap) Age(now time.Time) time.Duration {
	if b == nil {
		return 0
	}
	return now.Sub(b.FetchedAt)
}

// rawBossEvent mirrors the feed. Every value is a JSON string, including the
// numbers and the booleans.
type rawBossEvent struct {
	EventID          string     `json:"event_id"`
	ISOA             string     `json:"isoa"`
	Locations        [][]string `json:"locations"`
	Started          string     `json:"started"`
	Ended            string     `json:"ended"`
	RewardExp        string     `json:"reward_exp"`
	NeedBriefing     string     `json:"need_briefing"`
	Title            string     `json:"title"`
	SpecialEnemyType string     `json:"special_enemy_type"`
	EventType        string     `json:"event_type"`
	BossNum          string     `json:"boss_num"`
	StartTime        string     `json:"start_time"`
	EndTime          string     `json:"end_time"`

	// Objectives is an OBJECT when a mission has them and an empty ARRAY when it
	// does not, so it cannot be decoded straight into a map.
	Objectives json.RawMessage `json:"dfp_objectives"`
}

// parseBossMap decodes the feed.
//
// The shape is a JSON object whose keys are event indices as strings, with three
// scalars mixed in at the same level (bosshash, servertime, version) - so it is
// decoded loosely rather than into a struct that would break the first time they
// add a top-level field.
func parseBossMap(data []byte, fetchedAt time.Time) (*BossMap, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("bossmap: not JSON (%w)", err)
	}

	out := &BossMap{FetchedAt: fetchedAt, byBlock: map[[2]int][]int{}}
	if raw, ok := top["bosshash"]; ok {
		_ = json.Unmarshal(raw, &out.Hash)
	}
	// Kept for what it says about the data's age, not used for timing. See the
	// field.
	var serverTime int64
	if raw, ok := top["servertime"]; ok {
		_ = json.Unmarshal(raw, &serverTime)
	}
	if serverTime > 0 {
		out.ServerTime = time.Unix(serverTime, 0)
	} else {
		out.ServerTime = fetchedAt
	}

	for key, raw := range top {
		switch key {
		case "bosshash", "servertime", "version":
			continue
		}
		var re rawBossEvent
		if err := json.Unmarshal(raw, &re); err != nil {
			continue // a scalar or a shape we do not know; not an error
		}
		if re.ISOA == "1" {
			// Map-wide, and its location is not a block. Their page renders this
			// as "All Outposts Under Attack" rather than placing it.
			if re.Ended != "1" {
				out.OutpostAttack = true
			}
			continue
		}
		event := CityEvent{
			ID:          re.EventID,
			Kind:        classifyBossEvent(re),
			Title:       strings.TrimSpace(html.UnescapeString(re.Title)),
			Enemies:     splitEnemyTypes(re.SpecialEnemyType),
			startedFlag: re.Started == "1",
			endedFlag:   re.Ended == "1",
		}
		event.RewardExp, _ = strconv.ParseInt(re.RewardExp, 10, 64)
		event.Slot, _ = strconv.Atoi(re.BossNum)
		if secs, err := strconv.ParseInt(re.StartTime, 10, 64); err == nil && secs > 0 {
			event.Start = time.Unix(secs, 0)
		}
		if secs, err := strconv.ParseInt(re.EndTime, 10, 64); err == nil && secs > 0 {
			event.End = time.Unix(secs, 0)
		}
		event.Objectives = parseBossObjectives(re.Objectives)

		for _, loc := range re.Locations {
			if len(loc) < 2 {
				continue
			}
			x, errX := strconv.Atoi(loc[0])
			y, errY := strconv.Atoi(loc[1])
			if errX != nil || errY != nil {
				continue
			}
			if x == onslaughtCoord && y == onslaughtCoord {
				// Kept, not skipped: this is where a player in Onslaught actually
				// is, so the ordinary block lookup does the right thing.
				event.Onslaught = true
			}
			event.Locations = append(event.Locations, [2]int{x, y})
		}

		index := len(out.Events)
		out.Events = append(out.Events, event)
		for _, loc := range event.Locations {
			out.byBlock[loc] = append(out.byBlock[loc], index)
		}
	}

	if len(out.Events) == 0 && !out.OutpostAttack {
		// Possible in principle but never observed. Silently showing "nothing
		// here" for a feed that changed shape would be the worst outcome: no
		// error, no data, no clue.
		return nil, errors.New("bossmap: no events in the response (has the feed changed shape?)")
	}
	return out, nil
}

// classifyBossEvent ports the branch ORDER from bossmap.js. A mission also has a
// special_enemy_type, so testing that first would misfile every mission.
func classifyBossEvent(re rawBossEvent) CityEventKind {
	switch {
	case re.NeedBriefing == "1":
		return EventMission
	case re.SpecialEnemyType != "0" && re.SpecialEnemyType != "":
		return EventSpawn
	case re.EventType != "":
		return EventQRF
	}
	return EventUnknown
}

// splitEnemyTypes turns "2 x Flaming Zombie<br />1 x Riot Shield Guy" into two
// entries. "0" means none.
func splitEnemyTypes(s string) []string {
	if s == "" || s == "0" {
		return nil
	}
	s = strings.NewReplacer("<br />", "\n", "<br/>", "\n", "<br>", "\n").Replace(s)
	var out []string
	for _, part := range strings.Split(s, "\n") {
		if part = strings.TrimSpace(html.UnescapeString(part)); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseBossObjectives formats a mission's tasks the way their own page does:
// findnpc plain, loot and kill with their amounts.
func parseBossObjectives(raw json.RawMessage) []string {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return nil // an empty array: this event has no objectives
	}
	var fields map[string]string
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	var out []string
	for _, key := range []string{"findnpc", "loot", "kill"} {
		label := strings.TrimSpace(html.UnescapeString(fields[key]))
		if label == "" {
			continue
		}
		if amount := fields[key+"_amount"]; amount != "" && amount != "1" {
			label += ": " + amount
		}
		out = append(out, label)
	}
	return out
}

// fetchBossMap performs one request.
func fetchBossMap(ctx context.Context, client *http.Client, url, userAgent string) (*BossMap, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	// Required: the endpoint answers 404 without it. See the note at the top.
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Bounded read: an unbounded ReadAll on somebody else's endpoint is a memory
	// bug waiting for a bad day.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bossmap: HTTP %s", resp.Status)
	}
	return parseBossMap(body, time.Now())
}

// BossPoller keeps the event map current while the game is running.
//
// The schedule comes from three things rather than a fixed timer:
//
//  1. The next boundary in the data - every event states when it starts and ends,
//     so the feed says exactly when it will next be wrong.
//  2. Arriving on a new block, which is the one question this feed answers.
//  3. A heartbeat, for what follows no cycle: Devil Hound, Behemoth, Volatile
//     Leaper and the 8x bandit packs spawn once a day at a random time, so
//     nothing in the data predicts them.
//
// Underneath all three is the minimum interval, which nothing can breach.
type BossPoller struct {
	client *http.Client
	game   *GameWatcher
	cfg    func() *Config
	// inOnslaught decides whether the five-minute cycles are worth scheduling
	// around, and which interval pair applies.
	inOnslaught func() bool

	onMap func(*BossMap)
	// current is the last good map, kept so the schedule can be read out of it.
	current *BossMap

	wake chan struct{}

	mu       sync.RWMutex
	failures int
	lastErr  string
	lastHash string
}

func newBossPoller(client *http.Client, game *GameWatcher, cfg func() *Config,
	inOnslaught func() bool) *BossPoller {
	return &BossPoller{
		client:      client,
		game:        game,
		cfg:         cfg,
		inOnslaught: inOnslaught,
		wake:        make(chan struct{}, 1),
	}
}

func (p *BossPoller) SetOnMap(fn func(*BossMap)) {
	p.mu.Lock()
	p.onMap = fn
	p.mu.Unlock()
}

func (p *BossPoller) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *BossPoller) pauseReason() string {
	cfg := p.cfg()
	if !cfg.BossMap.Enabled {
		return "the boss map is disabled"
	}
	if cfg.Poll.OnlyWhenGameRunning && p.game != nil && !p.game.State().Running {
		return "the game is not running (poll.only_when_game_running)"
	}
	return ""
}

func (p *BossPoller) jittered(d time.Duration) time.Duration {
	j := p.cfg().Poll.Jitter
	if j <= 0 {
		return d
	}
	out := time.Duration(float64(d) * (1 + (rand.Float64()*2-1)*j))
	if out <= 0 {
		return d
	}
	return out
}

// jitteredLate is jittered for a wake that must not arrive early: the spread is
// one-sided, so it can only ever delay. See nextDelay.
func (p *BossPoller) jitteredLate(d time.Duration) time.Duration {
	j := p.cfg().Poll.Jitter
	if j <= 0 {
		return d
	}
	return time.Duration(float64(d) * (1 + rand.Float64()*j))
}

func (p *BossPoller) Run(ctx context.Context) {
	next := time.Now()
	loggedPause := ""

	for {
		if reason := p.pauseReason(); reason != "" {
			if loggedPause != reason {
				log.Printf("bossmap: paused - %s", reason)
				loggedPause = reason
			}
			// Re-checked on a timer as well as on a poke: a pause that can only be
			// lifted by someone remembering to send a wake-up is a pause that will
			// one day be permanent.
			timer := time.NewTimer(pauseRecheck)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-p.wake:
				timer.Stop()
				next = time.Now()
				continue
			case <-timer.C:
				continue
			}
		}
		if loggedPause != "" {
			log.Print("bossmap: resumed")
			loggedPause = ""
		}

		if wait := time.Until(next); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-p.wake:
				timer.Stop()
				// Bring the next fetch forward to the earliest polite moment, and
				// no earlier - the same shape as the other two pollers.
				//
				// This used to be a bare `continue`, which left `next` untouched
				// and re-armed the timer for the same deadline: every Wake while
				// running was silently swallowed. So "arriving on a new block
				// refetches the map", which main.go does on every block change,
				// had never once happened.
				next = p.earliestFetch(time.Now())
				continue
			case <-timer.C:
			}
		}

		if err := p.pollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			next = time.Now().Add(p.backoff())
			continue
		}
		next = time.Now().Add(p.nextDelay(time.Now()))
	}
}

// bossMapBoundarySlack is how long after a changeover to poll. A few seconds,
// because their server needs a moment to have published the replacement.
const bossMapBoundarySlack = 5 * time.Second

// bossMapPublishWindow is how far before an Onslaught cycle starts to go looking
// for it - a little INSIDE the window where the feed already carries it.
//
// This is the fix for "next never shows up": reacting to NextBoundary alone means
// waking when the CURRENT cycle ends, which on a back-to-back schedule is also
// when the next one starts, so it has already become "now".
//
// 100s, measured. Sampling the feed every 20s on 2026-08-16, the 00:45:01 cycle
// first appeared at 00:43:18 - a lead of 103s, at most 123s allowing for the
// sample gap, and the player watching their page beside df-hud reported the same
// independently. The 150s this used to be, inherited from the userscript's
// PUBLISH_WINDOW_MS ("~2 minutes" as an impression, not a measurement), therefore
// woke BEFORE publication every cycle and left discovery to the floor's retry.
//
// Erring late by design: early looks for something that does not exist yet, while
// late merely finds it a few seconds on, bounded by onslaught_max_interval.
const bossMapPublishWindow = 100 * time.Second

// earliestFetch is the soonest a wake may bring the next fetch, which is one
// minimum interval after the last one landed. The floor protects somebody else's
// server, so a poke cannot breach it however often it arrives - walking across
// ten blocks is ten wakes and still at most one fetch per interval.
func (p *BossPoller) earliestFetch(now time.Time) time.Time {
	onslaught := p.inOnslaught != nil && p.inOnslaught()
	min, _ := p.cfg().BossMap.Intervals(onslaught)

	p.mu.RLock()
	current := p.current
	p.mu.RUnlock()
	if current == nil || current.FetchedAt.IsZero() {
		return now
	}
	if earliest := current.FetchedAt.Add(min); earliest.After(now) {
		return earliest
	}
	return now
}

// nextDelay is how long to wait before the next fetch.
func (p *BossPoller) nextDelay(now time.Time) time.Duration {
	cfg := p.cfg()

	p.mu.RLock()
	current := p.current
	p.mu.RUnlock()

	onslaught := p.inOnslaught != nil && p.inOnslaught()
	// Onslaught gets its own floor and heartbeat: a five-minute cycle cannot be
	// watched by numbers sized for an hourly one. See BossMapConfig.
	min, max := cfg.BossMap.Intervals(onslaught)
	if max < min {
		max = min
	}
	delay := max
	if boundary := current.NextBoundary(now, onslaught); !boundary.IsZero() {
		if until := boundary.Add(bossMapBoundarySlack).Sub(now); until < delay {
			delay = until
		}
	}
	// aimed marks a wake that has to land inside a window rather than merely
	// happen eventually, which changes what jitter may do to it.
	aimed := false
	if onslaught {
		if horizon := current.Horizon(true); !horizon.IsZero() {
			if until := horizon.Add(-bossMapPublishWindow).Sub(now); until < delay {
				delay = until
				aimed = true
			}
		}
	}
	// Jittered BEFORE the floor, not after: jitter may spread requests out, but
	// not breach a minimum protecting somebody else's server. A test caught 60s of
	// minimum becoming 54s of actual.
	//
	// On an aimed wake it may only push LATER - ten percent of a ~200s delay is
	// 20s, which is most of the margin between aiming at 100s and the 123s upper
	// bound on the publish lead.
	if aimed {
		delay = p.jitteredLate(delay)
	} else {
		delay = p.jittered(delay)
	}
	if delay < min {
		delay = min
	}
	return delay
}

func (p *BossPoller) backoff() time.Duration {
	p.mu.RLock()
	failures := p.failures
	p.mu.RUnlock()
	cfg := p.cfg()
	d := cfg.BossMap.Interval.Duration
	for i := 1; i < failures && d < cfg.Poll.BackoffMax.Duration; i++ {
		d *= 2
	}
	if d > cfg.Poll.BackoffMax.Duration {
		d = cfg.Poll.BackoffMax.Duration
	}
	return p.jittered(d)
}

// Once fetches the map immediately, for -once and the diagnostics.
func (p *BossPoller) Once(ctx context.Context) error { return p.pollOnce(ctx) }

func (p *BossPoller) pollOnce(ctx context.Context) error {
	cfg := p.cfg()
	reqCtx, cancel := context.WithTimeout(ctx, cfg.DF.Timeout.Duration)
	defer cancel()

	m, err := fetchBossMap(reqCtx, p.client, cfg.BossMap.URL, cfg.DF.UserAgent)

	p.mu.Lock()
	if err != nil {
		p.failures++
		p.lastErr = err.Error()
		failures := p.failures
		p.mu.Unlock()
		if failures == 1 || failures%10 == 0 {
			log.Printf("bossmap: %v", err)
		}
		return err
	}
	p.failures, p.lastErr = 0, ""
	p.current = m
	changed := p.lastHash != "" && p.lastHash != m.Hash
	first := p.lastHash == ""
	p.lastHash = m.Hash
	fn := p.onMap
	p.mu.Unlock()

	switch {
	case first:
		log.Printf("bossmap: %d events on the map%s", len(m.Events), outpostAttackSuffix(m))
	case changed:
		log.Printf("bossmap: the map changed (%d events)%s", len(m.Events), outpostAttackSuffix(m))
	}
	if fn != nil {
		fn(m)
	}
	return nil
}

func outpostAttackSuffix(m *BossMap) string {
	if m.OutpostAttack {
		return ", OUTPOST ATTACK active"
	}
	return ""
}

// Status is what the HUD shows when there is no map.
func (p *BossPoller) Status() (failures int, lastErr string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.failures, p.lastErr
}
