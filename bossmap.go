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
	"strconv"
	"strings"
	"sync"
	"time"
)

// What is standing on your block: bosses, bandit packs, missions and QRF events.
//
// This is the one feature whose data is not in the game's own API. The public
// stats feed carries the map's geometry (buildings, street layout) but nothing
// about what has spawned on it, and the player record only knows where YOU are.
// DFProfiler publishes the event map, so that is where this comes from.
//
// ## Being a good citizen with somebody else's site
//
// DFProfiler is a community site run by a person, not an API vendor, so the
// budget here is deliberately far below what their own page costs them:
//
//   - their own bossmap page polls this endpoint every 30 SECONDS per open tab
//     (bossmap.js: setTimeout(I, 3e4)). df-hud defaults to one minute and will not
//     be configured below 30s, so it can never cost more than their own page does.
//   - nothing is requested at all while the game is not running, same rule as the
//     game server.
//   - one request per interval, jittered, with exponential backoff on failure and
//     no retry storms.
//   - the User-Agent is df-hud's own, naming the tool and how to contact its
//     author. If this ever becomes a nuisance, the operator can identify it and
//     say so.
//
// The endpoint returns 404 without an `X-Requested-With: XMLHttpRequest` header,
// so that is sent. It is worth being precise about what that means: it is the
// convention their API is written against, not a claim to be something we are
// not - the User-Agent still says exactly what this is. A forged Referer WOULD be
// such a claim ("this came from a page on your site"), so none is sent, and it
// turns out not to be needed. The `_=<millis>` cache-buster their jQuery adds is
// also left off, so any HTTP cache in between is free to help.
const defaultBossMapURL = "https://www.dfprofiler.com/bossmap/json/"

// bossMapPastWindow is how long a finished event stays worth mentioning. Sized
// for Onslaught, whose cycles are five minutes and overlap in practice.
const bossMapPastWindow = 6 * time.Minute

// onslaughtCoord is the block Onslaught events sit on.
//
// Their own page treats it as "off screen" and labels it "OS", because the city
// map has no cell there. It is not a null value though: a player IN Onslaught
// reports df_positionx/y of exactly 3000,3000, so this is a real coordinate in the
// same space as everything else. Indexing it like any other block is therefore
// both simpler and more correct than special-casing it - the Onslaught cycles
// surface exactly when you are in Onslaught, and never otherwise.
const onslaughtCoord = 3000

// CityEventKind is what sort of thing an event is. The classification is a port
// of the branch order in bossmap.js, which is load-bearing: a mission also
// carries a special_enemy_type, so testing that first would file every mission as
// a plain spawn.
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

	// Enemies is already in "6 x Bandits" form, as the feed states it. Several
	// entries mean several enemy types on the same block, which the feed joins
	// with <br /> in one string.
	Enemies []string
	// Objectives is the mission's task list, formatted the way their own page
	// formats it.
	Objectives []string
	RewardExp  int64

	Locations [][2]int
	Start     time.Time
	End       time.Time

	// startedFlag and endedFlag are the feed's own booleans, used only when an
	// event carries no usable timestamps. Everything else derives state from
	// Start and End against the clock - see ActiveAt.
	startedFlag bool
	endedFlag   bool

	// Onslaught marks the boss cycles that run in the instanced Onslaught mode
	// rather than out on the city map. They shift every five minutes, which is the
	// tightest cycle in the feed and the reason a one-minute poll is worth having.
	Onslaught bool
}

// ActiveAt reports whether this event is happening at a given instant.
//
// Derived from the event's own start and end times rather than from the feed's
// started/ended flags, and this is the whole trick: the feed contains the NEXT
// cycle before it begins (observed appearing around :59 for a cycle that starts
// at :00) and keeps the PREVIOUS one for minutes afterwards. Deciding from the
// clock means one fetch stays correct straight through a changeover, with no
// request at the moment it matters and no window where the HUD shows a boss that
// has gone or hides one that has arrived.
//
// The flags are the fallback for an event with no usable timestamps, so a feed
// that stops sending them degrades to what it used to do rather than to nothing.
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

// EndedRecentlyAt is last cycle's, and only while "last cycle" still means
// something. The window exists because an event that finished an hour ago is not
// news, and the feed can carry one for a while.
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
	FetchedAt  time.Time
	ServerTime time.Time
	// Hash is the feed's own change marker (bosshash). Their page uses it to
	// decide whether anything moved; df-hud uses it only to log that it did.
	Hash   string
	Events []CityEvent

	// skew is the feed's clock minus ours at the moment of the fetch. Every
	// boundary in the feed is in their time, so comparing against our clock
	// without this makes a changeover land early or late by however far the two
	// have drifted.
	skew time.Duration

	// OutpostAttack is the feed's isoa flag: every outpost is under attack. It is
	// map-wide rather than per-block, and it is the sort of thing worth knowing
	// wherever you happen to be standing.
	OutpostAttack bool

	byBlock map[[2]int][]int
}

// ServerNow converts a local instant into the feed's clock.
func (b *BossMap) ServerNow(now time.Time) time.Time {
	if b == nil {
		return now
	}
	return now.Add(b.skew)
}

// At returns the events happening on one block at a given instant.
//
// Upcoming ones are deliberately excluded: "a titan will be here in forty
// minutes" is planning information, not something to put in front of someone who
// is being chased. They are still in the map, which is what lets a cached fetch
// become correct on its own when their start time arrives.
func (b *BossMap) At(x, y int, now time.Time) []CityEvent {
	server := b.ServerNow(now)
	return b.eventsAt(x, y, func(e CityEvent) bool { return e.ActiveAt(server) })
}

// AtEnded returns the previous cycle's events on one block.
//
// Only useful where the cycle is short enough that they overlap, which in practice
// means Onslaught: it shifts every five minutes, and the boss from the last cycle
// is routinely still standing there when the next one appears. The caller decides
// whether to ask - out in the city, where cycles are hourly, the previous cycle is
// a boss that has gone, and reporting it would send you somewhere for nothing.
func (b *BossMap) AtEnded(x, y int, now time.Time) []CityEvent {
	server := b.ServerNow(now)
	return b.eventsAt(x, y, func(e CityEvent) bool { return e.EndedRecentlyAt(server) })
}

// NearestEvent is the closest active event to a block, for when the block you are
// standing on has nothing on it.
//
// Distance is counted in block moves - |dx| + |dy| - because that is how you cover
// it: you walk north, south, east or west between adjacent blocks, so a diagonal
// costs two.
//
// Onslaught is excluded. Its cycles sit on 3000,3000, which is not a place you can
// walk to from the city, so including it would report the nearest boss as being
// roughly two thousand blocks away.
func (b *BossMap) NearestEvent(x, y int, now time.Time) (event CityEvent, dx, dy int, ok bool) {
	if b == nil {
		return CityEvent{}, 0, 0, false
	}
	server := b.ServerNow(now)
	best := -1
	for _, e := range b.Events {
		if e.Onslaught || !e.ActiveAt(server) {
			continue
		}
		for _, loc := range e.Locations {
			ex, ey := loc[0], loc[1]
			if ex == onslaughtCoord && ey == onslaughtCoord {
				continue
			}
			gx, gy := ex-x, ey-y
			distance := abs(gx) + abs(gy)
			if distance == 0 {
				// Something on your own block, which the caller already has from At.
				continue
			}
			// Ties broken by the first one seen, which is the feed's own order, so
			// the row does not flicker between two equidistant bosses every poll.
			if best == -1 || distance < best {
				best, event, dx, dy, ok = distance, e, gx, gy, true
			}
		}
	}
	return event, dx, dy, ok
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
// This is what replaces polling on a fixed timer. The feed tells us exactly when
// it will next be wrong, so the schedule can be derived from the data instead of
// guessed at, which is both fresher at the moments that matter and quieter at the
// ones that do not.
//
// withOnslaught decides whether the five-minute Onslaught cycles count. They are
// always in the feed, so including them unconditionally would pull the whole
// schedule down to five minutes for someone who is out in the city and cannot see
// them.
func (b *BossMap) NextBoundary(now time.Time, withOnslaught bool) time.Time {
	if b == nil {
		return time.Time{}
	}
	server := b.ServerNow(now)
	var best time.Time
	consider := func(t time.Time) {
		if t.IsZero() || !t.After(server) {
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
	if best.IsZero() {
		return time.Time{}
	}
	return best.Add(-b.skew) // back into local time
}

// Age is how stale this fetch is, for deciding whether to trust it.
func (b *BossMap) Age(now time.Time) time.Duration {
	if b == nil {
		return 0
	}
	return now.Sub(b.FetchedAt)
}

// rawBossEvent mirrors the feed. Every value is a JSON string, including the
// numbers and the booleans, which is why nothing here is a Go int or bool.
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
	StartTime        string     `json:"start_time"`
	EndTime          string     `json:"end_time"`

	// Objectives is an OBJECT when a mission has them and an empty ARRAY when it
	// does not, so it cannot be decoded straight into a map.
	Objectives json.RawMessage `json:"dfp_objectives"`
}

// parseBossMap decodes the feed.
//
// The shape is a JSON object whose keys are event indices as strings, with three
// scalars mixed in at the same level (bosshash, servertime, version). So it is
// decoded loosely and each value is examined, rather than into a struct that
// would break the first time they add a field at the top level.
func parseBossMap(data []byte, fetchedAt time.Time) (*BossMap, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("bossmap: not JSON (%w)", err)
	}

	out := &BossMap{FetchedAt: fetchedAt, byBlock: map[[2]int][]int{}}
	if raw, ok := top["bosshash"]; ok {
		_ = json.Unmarshal(raw, &out.Hash)
	}
	// The feed's own clock decides what is active. Using ours would make every
	// event's state depend on local clock skew against their server.
	var serverTime int64
	if raw, ok := top["servertime"]; ok {
		_ = json.Unmarshal(raw, &serverTime)
	}
	if serverTime > 0 {
		out.ServerTime = time.Unix(serverTime, 0)
		out.skew = out.ServerTime.Sub(fetchedAt)
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
		// An empty map is possible in principle but has never been observed, and
		// silently showing "nothing here" for a feed that changed shape would be
		// the worst outcome: no error, no data, no clue.
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
	// Required: the endpoint answers 404 without it. See the note at the top of
	// this file for why sending it is not the same as pretending to be a browser.
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Bounded read: this is a third-party response, and an unbounded ReadAll on
	// somebody else's endpoint is a memory bug waiting for a bad day.
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
// The schedule is derived from three things rather than from a fixed timer, which
// is both fresher where it matters and quieter where it does not:
//
//  1. The next boundary in the data. Every event states when it starts and ends,
//     so the feed says exactly when it will next be wrong. Most cycles turn over
//     on the hour, and a poll a few seconds later picks the new one up.
//  2. Arriving on a new block. What is on the block you just walked onto is the
//     one question this feed answers, so that is the moment to be current.
//  3. A heartbeat, for the events that follow no cycle at all: Devil Hound,
//     Behemoth, Volatile Leaper and the 8x bandit packs spawn once a day at a
//     random time and last two or three hours, so nothing in the data predicts
//     them appearing.
//
// Underneath all three is the minimum interval, which nothing can breach.
type BossPoller struct {
	client *http.Client
	game   *GameWatcher
	cfg    func() *Config
	// inOnslaught decides whether the five-minute Onslaught cycles are worth
	// scheduling around. They are always in the feed, so counting them for a
	// player out in the city would drag the whole schedule down to five minutes
	// for events they cannot see.
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

func (p *BossPoller) Run(ctx context.Context) {
	next := time.Now()
	loggedPause := ""

	for {
		if reason := p.pauseReason(); reason != "" {
			if loggedPause != reason {
				log.Printf("bossmap: paused - %s", reason)
				loggedPause = reason
			}
			// Re-checked on a timer as well as on a poke, for the same reason the
			// challenge board is: a pause that can only be lifted by someone
			// remembering to send a wake-up is a pause that will one day be
			// permanent.
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
// because the event's own end time is when it stops being true and their server
// needs a moment to have published the replacement.
const bossMapBoundarySlack = 5 * time.Second

// nextDelay is how long to wait before the next fetch.
func (p *BossPoller) nextDelay(now time.Time) time.Duration {
	cfg := p.cfg()
	min := cfg.BossMap.Interval.Duration
	max := cfg.BossMap.MaxInterval.Duration
	if max < min {
		max = min
	}

	p.mu.RLock()
	current := p.current
	p.mu.RUnlock()

	onslaught := p.inOnslaught != nil && p.inOnslaught()
	delay := max
	if boundary := current.NextBoundary(now, onslaught); !boundary.IsZero() {
		if until := boundary.Add(bossMapBoundarySlack).Sub(now); until < delay {
			delay = until
		}
	}
	// Jittered BEFORE the floor is applied, not after. Jitter is allowed to spread
	// requests out; it is not allowed to breach a minimum that exists to protect
	// somebody else's server. Clamping last is what makes the floor absolute - a
	// test caught 60s of minimum becoming 54s of actual.
	delay = p.jittered(delay)
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
