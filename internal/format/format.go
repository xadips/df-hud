// Package format provides the HUD's display formatting.
package format

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Int writes thousands separators, matching the game's own number formatting.
func Int(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Clock is the session clock: H:MM:SS, growing past 24h rather than wrapping.
func Clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h, m, s := total/3600, (total/60)%60, total%60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

// Countdown formats expiration durations at a deliberately coarse precision.
func Countdown(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%02ds", m, s)
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%02dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}

// Age describes how stale a reading is, staying quiet below 30 seconds.
func Age(d time.Duration) string {
	if d < 30*time.Second {
		return ""
	}
	return Countdown(d) + " ago"
}

// Rate renders XP per hour in full, grouped in threes.
func Rate(perHour float64) string {
	if perHour < 0 {
		return ""
	}
	return Int(int64(perHour))
}

// Cash puts the game's own dollar sign on money.
func Cash(n int64) string { return "$" + Int(n) }

// Position renders the game's bare coordinates, adding a non-zero floor.
func Position(x, y, z int) string {
	if z != 0 {
		return fmt.Sprintf("%d, %d (floor %d)", x, y, z)
	}
	return fmt.Sprintf("%d, %d", x, y)
}

// ExpProgress formats current and required experience.
func ExpProgress(exp, needed int64) string {
	if needed <= 0 {
		return Int(exp)
	}
	return Int(exp) + " / " + Int(needed)
}

// DangerLevel deliberately shows the raw number and no adjective.
func DangerLevel(level int) string { return strconv.Itoa(level) }
