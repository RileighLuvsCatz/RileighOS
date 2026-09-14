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
