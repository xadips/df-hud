package format

import (
	"testing"
	"time"
)

func TestInt(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 5: "5", 42: "42", 999: "999", 1000: "1,000", 12345: "12,345",
		176_000_000:    "176,000,000",
		10_000_000: "10,000,000,000",
		-1234:          "-1,234",
		-999:           "-999",
	} {
		if got := Int(in); got != want {
			t.Errorf("Int(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestClock(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00:00"},
		{42 * time.Second, "0:00:42"},
		{90 * time.Second, "0:01:30"},
		{time.Hour + 23*time.Minute + 45*time.Second, "1:23:45"},
		{26*time.Hour + time.Minute, "26:01:00"},
		{-time.Minute, "0:00:00"},
	} {
		if got := Clock(tc.in); got != tc.want {
			t.Errorf("Clock(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCountdownAndAge(t *testing.T) {
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
		if got := Countdown(tc.in); got != tc.want {
			t.Errorf("Countdown(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := Age(2 * time.Second); got != "" {
		t.Errorf("Age(2s) = %q", got)
	}
	if got := Age(5 * time.Minute); got != "5m ago" {
		t.Errorf("Age(5m) = %q", got)
	}
}

func TestOtherFormats(t *testing.T) {
	if got := Rate(0); got != "0" {
		t.Errorf("zero Rate = %q", got)
	}
	if got := Rate(58_143_000); got != "58,143,000" {
		t.Errorf("Rate = %q", got)
	}
	if got := Rate(1_234_567.9); got != "1,234,567" {
		t.Errorf("truncated Rate = %q", got)
	}
	if got := Rate(-1); got != "" {
		t.Errorf("negative Rate = %q", got)
	}
	if got := Cash(1000); got != "$47,128" {
		t.Errorf("Cash = %q", got)
	}
	if got := Position(1058, 1019, 2); got != "1058, 1019 (floor 2)" {
		t.Errorf("Position = %q", got)
	}
	if got := Position(1058, 1019, 0); got != "1058, 1019" {
		t.Errorf("street Position = %q", got)
	}
	if got := ExpProgress(1000, 5000); got != "1,000 / 5,000" {
		t.Errorf("ExpProgress = %q", got)
	}
	if got := ExpProgress(1000, 0); got != "1,000" {
		t.Errorf("capped ExpProgress = %q", got)
	}
	if got := DangerLevel(42); got != "42" {
		t.Errorf("DangerLevel = %q", got)
	}
}
