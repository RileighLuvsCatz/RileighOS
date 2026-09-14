package main

import "time"

// dayLayout is the single day format used everywhere check-offs are
// stored, compared, and served: plain calendar dates, no times, no zones.
const dayLayout = "2006-01-02"

// Today returns the server-local calendar date. The day boundary behind
// streaks is deliberately server-local (not UTC), so a Pi serving a
// single user must have its timezone set to the user's (see Phase 3).
func Today() string {
	return time.Now().Format(dayLayout)
}

// ValidDay reports whether day is a real calendar date in YYYY-MM-DD form.
// The round-trip check rejects things time.Parse alone forgives, such as
// month 13 or February 30.
func ValidDay(day string) bool {
	t, err := time.Parse(dayLayout, day)
	if err != nil {
		return false
	}
	return t.Format(dayLayout) == day
}

// prevDay steps one calendar day back. It is only called with values that
// passed ValidDay on the way into the store, so a parse failure can only
// mean corrupted data — and the safe answer then is to end the streak.
func prevDay(day string) (string, bool) {
	t, err := time.Parse(dayLayout, day)
	if err != nil {
		return "", false
	}
	return t.AddDate(0, 0, -1).Format(dayLayout), true
}

// CurrentStreak counts consecutive checked days ending today or yesterday:
// the streak stays alive through today even before today's check-in, and
// any gap (or no checks at all) means zero. Days after today are ignored,
// and duplicates or unsorted input cannot inflate the count.
func CurrentStreak(days []string, today string) int {
	checked := make(map[string]bool, len(days))
	for _, d := range days {
		if d <= today { // lexicographic order matches chronological here
			checked[d] = true
		}
	}
	streak := 0
	// A streak stays alive through today even before today's check-in,
	// so an unchecked today starts the walk from yesterday.
	cursor := today
	if !checked[cursor] {
		prev, ok := prevDay(cursor)
		if !ok {
			return 0
		}
		cursor = prev
	}
	for ; checked[cursor]; streak++ {
		prev, ok := prevDay(cursor)
		if !ok {
			break
		}
		cursor = prev
	}
	return streak
}
