package main

import "errors"

// ErrNotFound is returned when an operation references an ID
// that does not exist in the store.
var ErrNotFound = errors.New("not found")

// TodoStore describes everything the app needs to do with todos.
// Mutations operate on the store (by ID), not on individual items,
// so storage backends stay interchangeable.
type TodoStore interface {
	AddTodo(content string) (Todo, error)
	GetTodos() ([]Todo, error)
	GetTodo(id int) (Todo, error)
	MarkTodoDone(id int) error
	MarkTodoUndone(id int) error
	DeleteTodo(id int) error
}

// NoteStore describes everything the app needs to do with notes.
// Notes have no done state, so there are no MarkDone/MarkUndone methods.
type NoteStore interface {
	AddNote(content string) (Note, error)
	GetNotes() ([]Note, error)
	GetNote(id int) (Note, error)
	DeleteNote(id int) error
}

// CheckoffStore describes everything the app needs to do with daily
// check-offs. Days are "YYYY-MM-DD" strings in the server's local date;
// an empty day means Today(), so callers that mean "today" pass "" and
// never derive the date themselves.
//
// Checking a day is idempotent — checking twice is the same as once — so
// retries and double-taps cannot corrupt a streak.
type CheckoffStore interface {
	AddCheckoff(name string) (Checkoff, error)
	GetCheckoffs() ([]Checkoff, error)
	GetCheckoff(id int) (Checkoff, error)
	DeleteCheckoff(id int) error
	CheckDay(id int, day string) error
	UncheckDay(id int, day string) error
	GetCheckoffDays(id int) ([]string, error)
	GetToday() (TodayView, error)
}

// Store is the single abstraction the rest of the program talks to.
// Both the JSON and SQLite backends satisfy it, so switching storage
// is a one-line change at the call site (see openStore in main.go).
// main.go must only use this interface — never SQL or JSON directly.
type Store interface {
	TodoStore
	NoteStore
	CheckoffStore
	Close() error
}
