package cardigann

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sites write sizes, numbers and dates in many ways. These turn them into
// plain values.

var reSize = regexp.MustCompile(`(?i)([\d.,]+)\s*([kmgtp]i?b|bytes?|b)?`)

// parseSize reads "1.4 GB", "700 MiB", "1,234.5 MB" or a plain number of
// bytes. KB/MB/GB count in 1024s, as torrent sites mean them.
func parseSize(s string) int64 {
	m := reSize.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	num := normalizeNumber(m[1])
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(m[2])
	mult := 1.0
	switch {
	case strings.HasPrefix(unit, "k"):
		mult = 1 << 10
	case strings.HasPrefix(unit, "m"):
		mult = 1 << 20
	case strings.HasPrefix(unit, "g"):
		mult = 1 << 30
	case strings.HasPrefix(unit, "t"):
		mult = 1 << 40
	case strings.HasPrefix(unit, "p"):
		mult = 1 << 50
	}
	return int64(f * mult)
}

// normalizeNumber turns "1,234.5" and "1.234,5" into "1234.5".
func normalizeNumber(s string) string {
	lastDot, lastComma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case lastComma > lastDot && len(s)-lastComma != 4:
		// "1.234,5" or "1,5": comma is the decimal mark.
		s = strings.ReplaceAll(s, ".", "")
		return strings.Replace(s, ",", ".", 1)
	default:
		return strings.ReplaceAll(s, ",", "")
	}
}

// parseInt reads "1,234" or "12" and gives 0 for anything else.
func parseInt(s string) int {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f)
	}
	return 0
}

var dateLayouts = []string{
	time.RFC3339, time.RFC1123Z, time.RFC1123,
	"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02",
	"02-01-2006", "02.01.2006", "Jan 2, 2006", "2 Jan 2006", "January 2, 2006",
}

// parseDate reads a date in any of the common forms, a Unix time, or a
// relative time like "3 days ago". The zero time means it couldn't.
func parseDate(s string, now time.Time) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		switch {
		case n > 1e12: // milliseconds
			return time.UnixMilli(n).UTC()
		case n > 1e8:
			return time.Unix(n, 0).UTC()
		}
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	if t, ok := parseRelativeTime(s, now); ok {
		return t
	}
	return time.Time{}
}

var reAgo = regexp.MustCompile(`(?i)(\d+|an?|one)\s*(sec|min|hour|hr|day|week|month|year|yr)s?`)

// parseRelativeTime reads "3 days ago", "1 hour 20 mins ago", "today",
// "yesterday" or "just now".
func parseRelativeTime(s string, now time.Time) (time.Time, bool) {
	low := strings.ToLower(strings.TrimSpace(s))
	switch {
	case low == "now" || low == "just now":
		return now, true
	case strings.HasPrefix(low, "today"):
		return now, true
	case strings.HasPrefix(low, "yesterday"):
		return now.AddDate(0, 0, -1), true
	}
	matches := reAgo.FindAllStringSubmatch(low, -1)
	if matches == nil {
		return time.Time{}, false
	}
	t := now
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			n = 1 // "a day ago", "an hour ago", "one week ago"
		}
		switch m[2] {
		case "sec":
			t = t.Add(-time.Duration(n) * time.Second)
		case "min":
			t = t.Add(-time.Duration(n) * time.Minute)
		case "hour", "hr":
			t = t.Add(-time.Duration(n) * time.Hour)
		case "day":
			t = t.AddDate(0, 0, -n)
		case "week":
			t = t.AddDate(0, 0, -7*n)
		case "month":
			t = t.AddDate(0, -n, 0)
		case "year", "yr":
			t = t.AddDate(-n, 0, 0)
		}
	}
	return t, true
}

var reIMDB = regexp.MustCompile(`(?:tt)?(\d{5,})`)

// normalizeIMDB turns "1234567", "tt1234567" or an IMDb address into
// "tt1234567".
func normalizeIMDB(s string) string {
	m := reIMDB.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	id := m[1]
	for len(id) < 7 {
		id = "0" + id
	}
	return "tt" + id
}
