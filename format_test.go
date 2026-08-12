package main

import (
	"testing"
	"time"
)

func TestFormatInt(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 5: "5", 42: "42", 999: "999", 1000: "1,000", 12345: "12,345",
		176_000_000: "176,000,000",
		// The real one: cumulative XP at high level is ten digits.
		10_000_000: "10,000,000,000",
		-1234:          "-1,234",
		-999:           "-999",
	} {
		if got := formatInt(in); got != want {
			t.Errorf("formatInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatCompact(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 999: "999",
		1000: "1", 1200: "1.2", 12_340: "12.3", 123_400: "123",
		1_000_000: "1", 1_234_567: "1.23", 10_000_000: "23.5",
		-1_500_000: "-1.5",
	} {
		got := formatCompact(in)
		// Suffixes are appended by size, so check the numeric part with its unit.
		var suffix string
		switch {
		case in >= 1_000_000_000 || in <= -1_000_000_000:
			suffix = "B"
		case in >= 1_000_000 || in <= -1_000_000:
			suffix = "M"
		case in >= 1000 || in <= -1000:
			suffix = "K"
		}
		if got != want+suffix {
			t.Errorf("formatCompact(%d) = %q, want %q", in, got, want+suffix)
		}
	}
}

func TestFormatClock(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00:00"},
		{42 * time.Second, "0:00:42"},
		{90 * time.Second, "0:01:30"},
		{time.Hour + 23*time.Minute + 45*time.Second, "1:23:45"},
		// A long session must keep counting rather than wrapping at 24h: a long
		// session is exactly when the number is interesting.
		{26*time.Hour + time.Minute, "26:01:00"},
		{-time.Minute, "0:00:00"},
	} {
		if got := formatClock(tc.in); got != tc.want {
			t.Errorf("formatClock(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatCountdown(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, ""},
		{-time.Second, ""},
		{45 * time.Second, "45s"},
		{2 * time.Minute, "2m"},
		{2*time.Minute + 5*time.Second, "2m05s"},
		{3 * time.Hour, "3h"},
		{3*time.Hour + 7*time.Minute, "3h07m"},
		{50 * time.Hour, "2d2h"},
		{48 * time.Hour, "2d"},
	} {
		if got := formatCountdown(tc.in); got != tc.want {
			t.Errorf("formatCountdown(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatAgeStaysQuietWhenFresh(t *testing.T) {
	// A two-second-old reading is simply current; saying "2s ago" would be noise
	// on every frame.
	if got := formatAge(2 * time.Second); got != "" {
		t.Errorf("formatAge(2s) = %q, want empty", got)
	}
	if got := formatAge(5 * time.Minute); got != "5m ago" {
		t.Errorf("formatAge(5m) = %q, want \"5m ago\"", got)
	}
}

func TestFormatRate(t *testing.T) {
	// Zero is a real reading, and rendering it as nothing left a bare "xp" on the
	// live HUD - which reads as broken rather than as idle.
	if got := formatRate(0); got != "0/hr" {
		t.Errorf("formatRate(0) = %q, want 0/hr", got)
	}
	if got := formatRate(-5); got != "" {
		t.Errorf("a negative rate is never real and should render as nothing, got %q", got)
	}
	if got := formatRate(1_234_567); got != "1.23M/hr" {
		t.Errorf("formatRate(1234567) = %q", got)
	}
}

func TestFormatExpProgress(t *testing.T) {
	if got := formatExpProgress(1000, 5000); got != "1,000 / 5,000" {
		t.Errorf("formatExpProgress = %q", got)
	}
	// At the cap there is no threshold, and inventing a denominator would be a
	// lie - the site itself prints "I have no life" there.
	if got := formatExpProgress(6_904_495_883, 0); got != "6,904,495,883" {
		t.Errorf("at the cap: %q, want the bare total", got)
	}
}

func TestFormatPosition(t *testing.T) {
	if got := formatPosition(1058, 1019, 0); got != "1058, 1019" {
		t.Errorf("formatPosition = %q", got)
	}
	// Z is a floor index, so it is only worth showing when you are off the street.
	if got := formatPosition(1058, 1019, 2); got != "1058, 1019 (floor 2)" {
		t.Errorf("formatPosition with a floor = %q", got)
	}
}

func TestFormatCash(t *testing.T) {
	if got := formatCash(1000); got != "$47,128" {
		t.Errorf("formatCash = %q", got)
	}
}

// The danger level is shown as a bare number on purpose: the web client never
// renders df_dangerlevel, so its scale and wording are unknown and any adjective
// would be invented game knowledge presented as the game's own.
func TestFormatDangerLevelIsJustTheNumber(t *testing.T) {
	for _, level := range []int{0, 3, 9, 42} {
		if got := formatDangerLevel(level); got != itoa(level) {
			t.Errorf("formatDangerLevel(%d) = %q, want the bare number", level, got)
		}
	}
}
