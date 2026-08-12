package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Display formatting. Kept apart from the widgets so the choices are testable
// without a GUI, and so every widget renders numbers the same way.
//
// The house style follows the game's own HUD: thousands separators on money and
// XP (the game uses Intl.NumberFormat), and clock-style elapsed time. There was a
// compact form too (58.1M) for places where width was the constraint; it went
// when the rate moved to its own position on screen, because the digits it was
// rounding away were the ones worth reading.

// formatInt writes thousands separators, matching the game's own number
// formatting. XP at high level is ten digits, so this is not decoration.
func formatInt(n int64) string {
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

// formatClock is the session clock: H:MM:SS, growing past 24h rather than
// wrapping, because a long session is exactly when you want to see the number.
func formatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h, m, s := total/3600, (total/60)%60, total%60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

// formatCountdown is for things that expire: block support, an XP boost. Coarse
// on purpose - seconds matter near the end, hours do not.
func formatCountdown(d time.Duration) string {
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

// formatAge describes how stale a reading is, for the "data is old" hint. It
// stays quiet under a threshold, since a two-second-old reading is simply
// current and saying so would be noise.
func formatAge(d time.Duration) string {
	if d < 30*time.Second {
		return ""
	}
	return formatCountdown(d) + " ago"
}

// formatRate renders XP per hour in full, grouped in threes.
//
// Not compact. "58.1M/hr" is three significant figures on a number where the
// digits are the point: at level 415 the difference between a good run and an
// ordinary one lives in the millions place, and a rate that reads 58.1M for
// several minutes while genuinely moving looks stuck. Grouping is what keeps
// nine digits readable at a glance.
//
// Zero renders as "0" rather than as nothing. Returning "" for zero produced a
// bare label on the live HUD, which reads as broken; a genuine zero is
// information, and while playing it is information you want. Only a negative
// rate renders as nothing, since that is never a real reading.
func formatRate(perHour float64) string {
	if perHour < 0 {
		return ""
	}
	return formatInt(int64(perHour))
}

// formatCash puts the game's own dollar sign on money.
func formatCash(n int64) string { return "$" + formatInt(n) }

// formatPosition is how the game itself talks about where you are: bare
// coordinates. Z is only shown when it is non-zero, since it is a floor index
// and zero is the street.
func formatPosition(x, y, z int) string {
	if z != 0 {
		return fmt.Sprintf("%d, %d (floor %d)", x, y, z)
	}
	return fmt.Sprintf("%d, %d", x, y)
}

// formatExpProgress is the "6,904,495,883 / 176,000,000" line. It returns "" when
// there is no threshold to show, which is the case at the level cap - the site's
// own sidebar prints "I have no life" there, and a HUD is better off saying
// nothing than inventing a denominator.
func formatExpProgress(exp, needed int64) string {
	if needed <= 0 {
		return formatInt(exp)
	}
	return formatInt(exp) + " / " + formatInt(needed)
}

// formatDangerLevel deliberately shows the raw number and no adjective.
//
// The web client never renders df_dangerlevel anywhere - grep the saved source -
// so its scale, range and wording are all unknown. Mapping it to "moderate" or
// "severe" would be inventing game knowledge and presenting it as the game's,
// which is worse than showing a bare number the player can learn to read. When
// the real scale is known (from the standalone client's HUD, or from observing
// values across blocks), this is where the wording goes.
func formatDangerLevel(level int) string {
	return strconv.Itoa(level)
}
