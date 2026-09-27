package canvas

import (
	"fmt"
	"time"

	"rdb/rileighos/internal/models"
)

// WorkSession is one suggested block of work on a todo. It is a stub for
// the post-fair auto-scheduling heuristic (roadmap Phase 4): e.g. if a
// Canvas-imported todo has no work session scheduled and its due date is
// within N days, suggest or auto-create one.
type WorkSession struct {
	TodoID   int       `json:"todo_id"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Reason   string    `json:"reason"`
}

// SuggestWorkSessions proposes work sessions for dated open todos. It
// currently returns nothing; the heuristic gets designed once real usage
// data exists to tune it against.
func SuggestWorkSessions(todos []models.Todo, now time.Time, horizonDays int) []WorkSession {
	// TODO(phase-4): rank dated open todos by urgency (due proximity,
	// course load balance) and pack sessions into free time.
	_ = todos
	_ = now
	_ = horizonDays
	return nil
}

// FormatInterval renders a duration for startup lines ("30m", "1h").
// time.Duration.String is needlessly precise ("30m0s") for this.
func FormatInterval(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// means the default; "0" disables. Anything unparseable disables with an
// error so the caller can warn instead of crashing the server.
func ParseInterval(raw string, def time.Duration) (d time.Duration, enabled bool, err error) {
	if raw == "" {
		return def, true, nil
	}
	d, err = time.ParseDuration(raw)
	if err != nil {
		return 0, false, err
	}
	if d <= 0 {
		return 0, false, nil
	}
	return d, true, nil
}

// SyncStale reports whether an on-open auto-sync is due: never synced, or
// the last sync is older than the threshold.
func SyncStale(last, now time.Time, threshold time.Duration) bool {
	if last.IsZero() {
		return true
	}
	return now.Sub(last) > threshold
}
