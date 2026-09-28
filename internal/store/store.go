package store

import (
	"errors"
	"time"

	"rdb/rileighos/internal/models"
)

// ErrNotFound is returned when an operation references an ID
// that does not exist in the store.
var ErrNotFound = errors.New("not found")

// TodoStore describes everything the app needs to do with todos.
// Mutations operate on the store (by ID), not on individual items,
// so storage backends stay interchangeable.
type TodoStore interface {
	AddTodo(content string) (models.Todo, error)
	GetTodos() ([]models.Todo, error)
	GetTodo(id int) (models.Todo, error)
	MarkTodoDone(id int) error
	MarkTodoUndone(id int) error
	SetTodoWorkDate(id int, day string) error
	ClearTodoWorkDate(id int) error
	DeleteTodo(id int) error
}

// NoteStore describes everything the app needs to do with notes.
// Notes have no done state, so there are no MarkDone/MarkUndone methods.
type NoteStore interface {
	AddNote(content string) (models.Note, error)
	GetNotes() ([]models.Note, error)
	GetNote(id int) (models.Note, error)
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
	AddCheckoff(name string) (models.Checkoff, error)
	GetCheckoffs() ([]models.Checkoff, error)
	GetCheckoff(id int) (models.Checkoff, error)
	DeleteCheckoff(id int) error
	CheckDay(id int, day string) error
	UncheckDay(id int, day string) error
	GetCheckoffDays(id int) ([]string, error)
	GetToday() (models.TodayView, error)
}

// CanvasStore describes everything Canvas sync needs beyond plain CRUD:
// course gating state, external-ID lookups for dedup, field updates for
// re-syncs, and the last-sync timestamp shared by manual, on-open, and
// ticker triggers.
//
// Lookups by Canvas assignment ID return ErrNotFound when the assignment
// was never imported — never when it was imported and deleted, because
// sync never deletes user data.
type CanvasStore interface {
	GetCanvasCourses() ([]models.CanvasCourse, error)
	UpsertCanvasCourse(c models.CanvasCourse) (models.CanvasCourse, error)
	ResolveCanvasCourse(courseID int64, excluded bool) error
	// DetachCanvasCourse converts a course's imported todos/notes to plain
	// local items (clearing origin and external IDs) and returns how many
	// of each were converted. Excluding never deletes: the items stay, the
	// course just stops importing. Re-including the course later re-imports
	// its assignments as new items, so detachment is one-way by design.
	DetachCanvasCourse(courseID int64) (todos, notes int, err error)
	// DismissCanvasAssignment tombstones one Canvas assignment ID so sync
	// skips it forever, even when no local copy exists. Dismissing an
	// unknown-never-seen ID is allowed (tombstone first, import never
	// happens).
	DismissCanvasAssignment(id int64) error
	// UndismissCanvasAssignment removes a dismiss tombstone. Undismissing
	// a non-dismissed ID is a silent no-op that still succeeds; it does
	// not re-import by itself — the next sync does.
	UndismissCanvasAssignment(id int64) error
	// IsCanvasDismissed reports whether an assignment ID is tombstoned.
	IsCanvasDismissed(id int64) (bool, error)
	AddCanvasTodo(content string, dueAt *time.Time, assignmentID int64, courseCode, url, updatedAt string) (models.Todo, error)
	GetTodoByCanvasID(assignmentID int64) (models.Todo, error)
	UpdateCanvasTodo(id int, content string, dueAt *time.Time, courseCode, url, updatedAt string) error
	DetachCanvasTodo(id int) error
	AddCanvasNote(content string, dueAt *time.Time, assignmentID int64, courseCode, url, updatedAt string) (models.Note, error)
	GetNoteByCanvasID(assignmentID int64) (models.Note, error)
	UpdateCanvasNote(id int, content string, dueAt *time.Time, courseCode, url, updatedAt string) error
	GetLastCanvasSync() (time.Time, error)
	SetLastCanvasSync(t time.Time) error
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

// FullStore is a Store plus Canvas sync state. The server and the sync
// engine need both against the same backend, so they take this. The HTTP
// Client is deliberately only a Store plus the three CLI-facing Canvas
// calls (see CanvasSyncer): course gating and dedup state live server-side,
// never on the CLI machine.
type FullStore interface {
	Store
	CanvasStore
}
