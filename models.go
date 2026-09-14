package main

import "time"

// Todo is a single actionable item with a completion state.
// It is intentionally kept separate from Note: todos are for
// things to do, notes are for things to remember.
type Todo struct {
	ID        int       `json:"id"`
	Content   string    `json:"content"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
}

// Note is a free-form piece of text with no completion state.
type Note struct {
	ID        int       `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// Checkoff is a named daily habit: a "did I do X today" record type,
// separate from Todo (one-shot actionable items). The habit itself carries
// no state; each checked day is stored alongside it (see CheckoffStore),
// and streaks are derived from those days, never stored.
type Checkoff struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// CheckoffView is a checkoff plus everything derived from its checked
// days as of one date, so a single response answers "how am I doing".
type CheckoffView struct {
	Checkoff
	Days         []string `json:"days"`
	Streak       int      `json:"streak"`
	CheckedToday bool     `json:"checked_today"`
}

// TodayView is the answer to "what does my day look like": open todos
// plus every check-off with its streak, computed as of Date.
type TodayView struct {
	Date      string         `json:"date"`
	OpenTodos []Todo         `json:"open_todos"`
	Checkoffs []CheckoffView `json:"checkoffs"`
}
