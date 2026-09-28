package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"rdb/rileighos/internal/models"
)

// SQLiteStore is a Store backed by a SQLite database file.
// It is the default backend: real querying/filtering without any
// separate database process, and the file moves to the Pi as-is in Phase 3.
//
// Pure-Go driver (modernc.org/sqlite, no CGO) so cross-compiling for the
// Pi (GOOS=linux GOARCH=arm64) works with no toolchain changes.
type SQLiteStore struct {
	mu sync.Mutex
	db *sql.DB
}

// Compile-time check that SQLiteStore satisfies Store.
var _ Store = (*SQLiteStore)(nil)

// Compile-time check that SQLiteStore satisfies FullStore (Canvas sync).
var _ FullStore = (*SQLiteStore)(nil)

// OpenSQLiteStore opens (creating if needed) the database at path
// and ensures the schema exists.
func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS todos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			done INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS notes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS checkoffs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		// One row per checked day. The composite key makes checking the
		// same day twice a no-op (INSERT OR IGNORE), so streaks cannot
		// be corrupted by retries or double-taps.
		`CREATE TABLE IF NOT EXISTS checkoff_days (
			checkoff_id INTEGER NOT NULL REFERENCES checkoffs(id),
			day TEXT NOT NULL,
			PRIMARY KEY (checkoff_id, day)
		)`,
		// Canvas import gating: one row per course seen upstream. Excluded
		// and pending_confirm persist here so every sync trigger (manual,
		// on-open, ticker) honors decisions made by any other.
		`CREATE TABLE IF NOT EXISTS canvas_courses (
			course_id INTEGER PRIMARY KEY,
			code TEXT NOT NULL,
			name TEXT NOT NULL,
			excluded INTEGER NOT NULL DEFAULT 0,
			pending_confirm INTEGER NOT NULL DEFAULT 0,
			last_seen TEXT NOT NULL DEFAULT ''
		)`,
		// Single-row key/value for sync bookkeeping (last_sync_at).
		`CREATE TABLE IF NOT EXISTS canvas_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		// Dismiss tombstones: one row per never-show-again Canvas
		// assignment ID. Sync checks this first and skips dismissed IDs
		// (counted as Skipped), so a deleted local copy is never
		// re-imported. Tombstones survive deletes and re-syncs; only
		// undismiss removes them.
		`CREATE TABLE IF NOT EXISTS canvas_dismissed (
			assignment_id INTEGER PRIMARY KEY
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Column additions for pre-Canvas databases. ALTER TABLE has no
	// IF NOT EXISTS guard, so duplicate-column errors are expected on
	// every open after the first and are ignored; anything else fails.
	adds := []string{
		`ALTER TABLE todos ADD COLUMN due_at TEXT`,
		`ALTER TABLE todos ADD COLUMN origin TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE todos ADD COLUMN canvas_assignment_id INTEGER`,
		`ALTER TABLE todos ADD COLUMN canvas_course_code TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE todos ADD COLUMN canvas_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE todos ADD COLUMN canvas_updated_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE notes ADD COLUMN due_at TEXT`,
		`ALTER TABLE notes ADD COLUMN origin TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE notes ADD COLUMN canvas_assignment_id INTEGER`,
		`ALTER TABLE notes ADD COLUMN canvas_course_code TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE notes ADD COLUMN canvas_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE notes ADD COLUMN canvas_updated_at TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range adds {
		if _, err := s.db.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Indexes last: they reference columns the ALTERs above just added,
	// which do not exist on pre-Canvas databases until this runs.
	indexes := []string{
		// External IDs are unique where present (NULLs never collide in
		// SQLite unique indexes), so a sync bug surfaces as a loud error
		// instead of silent duplicate todos.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_todos_canvas_assignment ON todos(canvas_assignment_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_notes_canvas_assignment ON notes(canvas_assignment_id)`,
	}
	for _, stmt := range indexes {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Close releases the database connection.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// todoColumns / noteColumns are the full column lists for reads; every
// SELECT must use them in this order to match the scan helpers below.
const todoColumns = `id, content, done, created_at, due_at, origin, canvas_assignment_id, canvas_course_code, canvas_url, canvas_updated_at`
const noteColumns = `id, content, created_at, due_at, origin, canvas_assignment_id, canvas_course_code, canvas_url, canvas_updated_at`

// todoScan holds the raw scan targets for one Todo row.
type todoScan struct {
	todo     models.Todo
	done     int
	created  string
	dueAt    sql.NullString
	assignID sql.NullInt64
}

// finish resolves Nulls into the Todo. due_at is stored RFC3339Nano;
// unparseable values (hand-edited DBs) fall back to nil, never an error.
func (ts *todoScan) finish() {
	ts.todo.Done = ts.done != 0
	ts.todo.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts.created)
	if ts.dueAt.Valid && ts.dueAt.String != "" {
		if dt, err := time.Parse(time.RFC3339Nano, ts.dueAt.String); err == nil {
			d := dt
			ts.todo.DueAt = &d
		}
	}
	if ts.assignID.Valid {
		id := ts.assignID.Int64
		ts.todo.CanvasAssignID = &id
	}
}

func (ts *todoScan) targets() []any {
	return []any{&ts.todo.ID, &ts.todo.Content, &ts.done, &ts.created, &ts.dueAt, &ts.todo.Origin, &ts.assignID, &ts.todo.CanvasCourseCode, &ts.todo.CanvasURL, &ts.todo.CanvasUpdatedAt}
}

// noteScan holds the raw scan targets for one Note row.
type noteScan struct {
	note     models.Note
	created  string
	dueAt    sql.NullString
	assignID sql.NullInt64
}

func (ns *noteScan) finish() {
	ns.note.CreatedAt, _ = time.Parse(time.RFC3339Nano, ns.created)
	if ns.dueAt.Valid && ns.dueAt.String != "" {
		if dt, err := time.Parse(time.RFC3339Nano, ns.dueAt.String); err == nil {
			d := dt
			ns.note.DueAt = &d
		}
	}
	if ns.assignID.Valid {
		id := ns.assignID.Int64
		ns.note.CanvasAssignID = &id
	}
}

func (ns *noteScan) targets() []any {
	return []any{&ns.note.ID, &ns.note.Content, &ns.created, &ns.dueAt, &ns.note.Origin, &ns.assignID, &ns.note.CanvasCourseCode, &ns.note.CanvasURL, &ns.note.CanvasUpdatedAt}
}

// formatDue renders an optional due timestamp for storage (NULL when nil).
func formatDue(dueAt *time.Time) sql.NullString {
	if dueAt == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: dueAt.Format(time.RFC3339Nano), Valid: true}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *SQLiteStore) AddTodo(content string) (models.Todo, error) {
	if strings.TrimSpace(content) == "" {
		return models.Todo{}, fmt.Errorf("todo content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO todos (content, done, created_at) VALUES (?, 0, ?)`,
		content, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return models.Todo{}, fmt.Errorf("insert todo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.Todo{}, fmt.Errorf("todo id: %w", err)
	}
	return models.Todo{ID: int(id), Content: content, CreatedAt: now}, nil
}

func (s *SQLiteStore) GetTodos() ([]models.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ` + todoColumns + ` FROM todos ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	defer rows.Close()
	todos := []models.Todo{}
	for rows.Next() {
		var ts todoScan
		if err := rows.Scan(ts.targets()...); err != nil {
			return nil, fmt.Errorf("scan todo: %w", err)
		}
		ts.finish()
		todos = append(todos, ts.todo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	return todos, nil
}

func (s *SQLiteStore) GetTodo(id int) (models.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ts todoScan
	err := s.db.QueryRow(
		`SELECT `+todoColumns+` FROM todos WHERE id = ?`, id,
	).Scan(ts.targets()...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.Todo{}, fmt.Errorf("todo %d: %w", id, ErrNotFound)
		}
		return models.Todo{}, fmt.Errorf("get todo %d: %w", id, err)
	}
	ts.finish()
	return ts.todo, nil
}

func (s *SQLiteStore) MarkTodoDone(id int) error {
	return s.setTodoDone(id, true)
}

func (s *SQLiteStore) MarkTodoUndone(id int) error {
	return s.setTodoDone(id, false)
}

func (s *SQLiteStore) setTodoDone(id int, done bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	flag := 0
	if done {
		flag = 1
	}
	res, err := s.db.Exec(`UPDATE todos SET done = ? WHERE id = ?`, flag, id)
	if err != nil {
		return fmt.Errorf("update todo %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update todo %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("todo %d: %w", id, ErrNotFound)
	}
	return nil
}

func (s *SQLiteStore) DeleteTodo(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM todos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete todo %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete todo %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("todo %d: %w", id, ErrNotFound)
	}
	return nil
}

func (s *SQLiteStore) AddNote(content string) (models.Note, error) {
	if strings.TrimSpace(content) == "" {
		return models.Note{}, fmt.Errorf("note content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO notes (content, created_at) VALUES (?, ?)`,
		content, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return models.Note{}, fmt.Errorf("insert note: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.Note{}, fmt.Errorf("note id: %w", err)
	}
	return models.Note{ID: int(id), Content: content, CreatedAt: now}, nil
}

func (s *SQLiteStore) GetNotes() ([]models.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ` + noteColumns + ` FROM notes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()
	notes := []models.Note{}
	for rows.Next() {
		var ns noteScan
		if err := rows.Scan(ns.targets()...); err != nil {
			return nil, fmt.Errorf("scan note: %w", err)
		}
		ns.finish()
		notes = append(notes, ns.note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	return notes, nil
}

func (s *SQLiteStore) GetNote(id int) (models.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ns noteScan
	err := s.db.QueryRow(
		`SELECT `+noteColumns+` FROM notes WHERE id = ?`, id,
	).Scan(ns.targets()...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.Note{}, fmt.Errorf("note %d: %w", id, ErrNotFound)
		}
		return models.Note{}, fmt.Errorf("get note %d: %w", id, err)
	}
	ns.finish()
	return ns.note, nil
}

func (s *SQLiteStore) DeleteNote(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM notes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete note %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete note %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("note %d: %w", id, ErrNotFound)
	}
	return nil
}

func (s *SQLiteStore) AddCheckoff(name string) (models.Checkoff, error) {
	if strings.TrimSpace(name) == "" {
		return models.Checkoff{}, fmt.Errorf("checkoff name must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO checkoffs (name, created_at) VALUES (?, ?)`,
		name, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return models.Checkoff{}, fmt.Errorf("insert checkoff: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.Checkoff{}, fmt.Errorf("checkoff id: %w", err)
	}
	return models.Checkoff{ID: int(id), Name: name, CreatedAt: now}, nil
}

func (s *SQLiteStore) GetCheckoffs() ([]models.Checkoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, name, created_at FROM checkoffs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list checkoffs: %w", err)
	}
	defer rows.Close()
	checkoffs := []models.Checkoff{}
	for rows.Next() {
		var c models.Checkoff
		var created string
		if err := rows.Scan(&c.ID, &c.Name, &created); err != nil {
			return nil, fmt.Errorf("scan checkoff: %w", err)
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		checkoffs = append(checkoffs, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list checkoffs: %w", err)
	}
	return checkoffs, nil
}

func (s *SQLiteStore) GetCheckoff(id int) (models.Checkoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCheckoff(id)
}

// getCheckoff is the lock-held helper behind GetCheckoff and the day
// methods, so existence checks and day writes stay atomic.
func (s *SQLiteStore) getCheckoff(id int) (models.Checkoff, error) {
	var c models.Checkoff
	var created string
	err := s.db.QueryRow(
		`SELECT id, name, created_at FROM checkoffs WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.Checkoff{}, fmt.Errorf("checkoff %d: %w", id, ErrNotFound)
		}
		return models.Checkoff{}, fmt.Errorf("get checkoff %d: %w", id, err)
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return c, nil
}

func (s *SQLiteStore) DeleteCheckoff(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getCheckoff(id); err != nil {
		return err
	}
	// Days are deleted explicitly rather than by foreign-key cascade, so
	// this works regardless of the connection's foreign_keys pragma.
	if _, err := s.db.Exec(`DELETE FROM checkoff_days WHERE checkoff_id = ?`, id); err != nil {
		return fmt.Errorf("delete checkoff %d days: %w", id, err)
	}
	if _, err := s.db.Exec(`DELETE FROM checkoffs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete checkoff %d: %w", id, err)
	}
	return nil
}

func (s *SQLiteStore) CheckDay(id int, day string) error {
	if day == "" {
		day = Today()
	}
	if !ValidDay(day) {
		return fmt.Errorf("invalid day %q: want YYYY-MM-DD", day)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getCheckoff(id); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO checkoff_days (checkoff_id, day) VALUES (?, ?)`, id, day,
	); err != nil {
		return fmt.Errorf("check checkoff %d on %s: %w", id, day, err)
	}
	return nil
}

func (s *SQLiteStore) UncheckDay(id int, day string) error {
	if day == "" {
		day = Today()
	}
	if !ValidDay(day) {
		return fmt.Errorf("invalid day %q: want YYYY-MM-DD", day)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getCheckoff(id); err != nil {
		return err
	}
	// Removing a day that was never checked is a no-op (idempotent,
	// mirroring CheckDay), so RowsAffected is deliberately ignored.
	if _, err := s.db.Exec(
		`DELETE FROM checkoff_days WHERE checkoff_id = ? AND day = ?`, id, day,
	); err != nil {
		return fmt.Errorf("uncheck checkoff %d on %s: %w", id, day, err)
	}
	return nil
}

func (s *SQLiteStore) GetCheckoffDays(id int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.getCheckoff(id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT day FROM checkoff_days WHERE checkoff_id = ? ORDER BY day`, id,
	)
	if err != nil {
		return nil, fmt.Errorf("list checkoff %d days: %w", id, err)
	}
	defer rows.Close()
	days := []string{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, fmt.Errorf("scan checkoff day: %w", err)
		}
		days = append(days, day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list checkoff %d days: %w", id, err)
	}
	return days, nil
}

func (s *SQLiteStore) GetToday() (models.TodayView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := Today()

	todoRows, err := s.db.Query(`SELECT ` + todoColumns + ` FROM todos ORDER BY id`)
	if err != nil {
		return models.TodayView{}, fmt.Errorf("today todos: %w", err)
	}
	defer todoRows.Close()
	due := []models.Todo{}
	for todoRows.Next() {
		var ts todoScan
		if err := todoRows.Scan(ts.targets()...); err != nil {
			return models.TodayView{}, fmt.Errorf("scan today todo: %w", err)
		}
		ts.finish()
		// Today shows open todos due today or overdue: undated and
		// future-dated items live in `todo list`, not here.
		if ts.todo.Done || ts.todo.DueAt == nil || dueDay(ts.todo.DueAt) > today {
			continue
		}
		due = append(due, ts.todo)
	}
	if err := todoRows.Err(); err != nil {
		return models.TodayView{}, fmt.Errorf("today todos: %w", err)
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].DueAt.Equal(*due[j].DueAt) {
			return due[i].DueAt.Before(*due[j].DueAt)
		}
		return due[i].ID < due[j].ID
	})

	coRows, err := s.db.Query(`SELECT id, name, created_at FROM checkoffs ORDER BY id`)
	if err != nil {
		return models.TodayView{}, fmt.Errorf("today checkoffs: %w", err)
	}
	defer coRows.Close()
	checkoffs := []models.Checkoff{}
	for coRows.Next() {
		var c models.Checkoff
		var created string
		if err := coRows.Scan(&c.ID, &c.Name, &created); err != nil {
			return models.TodayView{}, fmt.Errorf("scan today checkoff: %w", err)
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		checkoffs = append(checkoffs, c)
	}
	if err := coRows.Err(); err != nil {
		return models.TodayView{}, fmt.Errorf("today checkoffs: %w", err)
	}

	dayRows, err := s.db.Query(`SELECT checkoff_id, day FROM checkoff_days ORDER BY checkoff_id, day`)
	if err != nil {
		return models.TodayView{}, fmt.Errorf("today checkoff days: %w", err)
	}
	defer dayRows.Close()
	byCheckoff := map[int][]string{}
	for dayRows.Next() {
		var cid int
		var day string
		if err := dayRows.Scan(&cid, &day); err != nil {
			return models.TodayView{}, fmt.Errorf("scan today checkoff day: %w", err)
		}
		byCheckoff[cid] = append(byCheckoff[cid], day)
	}
	if err := dayRows.Err(); err != nil {
		return models.TodayView{}, fmt.Errorf("today checkoff days: %w", err)
	}

	views := []models.CheckoffView{}
	for _, c := range checkoffs {
		days := byCheckoff[c.ID]
		if days == nil {
			days = []string{}
		}
		// Days arrive ordered ascending, so the last one is the latest.
		checkedToday := len(days) > 0 && days[len(days)-1] == today
		views = append(views, models.CheckoffView{
			Checkoff:     c,
			Days:         days,
			Streak:       CurrentStreak(days, today),
			CheckedToday: checkedToday,
		})
	}
	return models.TodayView{Date: today, DueTodos: due, Checkoffs: views}, nil
}

// --- Canvas sync support ---

func scanCanvasCourse(row interface {
	Scan(dest ...any) error
}) (models.CanvasCourse, error) {
	var c models.CanvasCourse
	var excluded, pending int
	var lastSeen string
	if err := row.Scan(&c.CourseID, &c.Code, &c.Name, &excluded, &pending, &lastSeen); err != nil {
		return models.CanvasCourse{}, err
	}
	c.Excluded = excluded != 0
	c.PendingConfirm = pending != 0
	c.LastSeen = lastSeen
	return c, nil
}

func (s *SQLiteStore) GetCanvasCourses() ([]models.CanvasCourse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT course_id, code, name, excluded, pending_confirm, last_seen FROM canvas_courses ORDER BY code`)
	if err != nil {
		return nil, fmt.Errorf("list canvas courses: %w", err)
	}
	defer rows.Close()
	courses := []models.CanvasCourse{}
	for rows.Next() {
		c, err := scanCanvasCourse(rows)
		if err != nil {
			return nil, fmt.Errorf("scan canvas course: %w", err)
		}
		courses = append(courses, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list canvas courses: %w", err)
	}
	return courses, nil
}

// UpsertCanvasCourse inserts a course seen upstream or refreshes its
// code/name/last_seen. Excluded and pending_confirm are preserved on
// conflict: only ResolveCanvasCourse changes a user's decision, so a
// re-sync can never silently re-include an excluded course or clear a
// pending confirmation.
func (s *SQLiteStore) UpsertCanvasCourse(c models.CanvasCourse) (models.CanvasCourse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO canvas_courses (course_id, code, name, excluded, pending_confirm, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(course_id) DO UPDATE SET code = excluded.code, name = excluded.name, last_seen = excluded.last_seen`,
		c.CourseID, c.Code, c.Name, boolToInt(c.Excluded), boolToInt(c.PendingConfirm), c.LastSeen,
	)
	if err != nil {
		return models.CanvasCourse{}, fmt.Errorf("upsert canvas course %d: %w", c.CourseID, err)
	}
	got, err := scanCanvasCourse(s.db.QueryRow(
		`SELECT course_id, code, name, excluded, pending_confirm, last_seen FROM canvas_courses WHERE course_id = ?`, c.CourseID,
	))
	if err != nil {
		return models.CanvasCourse{}, fmt.Errorf("read back canvas course %d: %w", c.CourseID, err)
	}
	return got, nil
}

// ResolveCanvasCourse records the user's include/exclude decision and
// clears pending_confirm in the same write.
func (s *SQLiteStore) ResolveCanvasCourse(courseID int64, excluded bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE canvas_courses SET excluded = ?, pending_confirm = 0 WHERE course_id = ?`, boolToInt(excluded), courseID)
	if err != nil {
		return fmt.Errorf("resolve canvas course %d: %w", courseID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("resolve canvas course %d: %w", courseID, err)
	}
	if n == 0 {
		return fmt.Errorf("canvas course %d: %w", courseID, ErrNotFound)
	}
	return nil
}

func (s *SQLiteStore) AddCanvasTodo(content string, dueAt *time.Time, assignmentID int64, courseCode, url, updatedAt string) (models.Todo, error) {
	if strings.TrimSpace(content) == "" {
		return models.Todo{}, fmt.Errorf("todo content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO todos (content, done, created_at, due_at, origin, canvas_assignment_id, canvas_course_code, canvas_url, canvas_updated_at)
		 VALUES (?, 0, ?, ?, 'canvas', ?, ?, ?, ?)`,
		content, now.Format(time.RFC3339Nano), formatDue(dueAt), assignmentID, courseCode, url, updatedAt,
	)
	if err != nil {
		return models.Todo{}, fmt.Errorf("insert canvas todo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.Todo{}, fmt.Errorf("canvas todo id: %w", err)
	}
	return models.Todo{ID: int(id), Content: content, CreatedAt: now, DueAt: dueAt, Origin: "canvas", CanvasAssignID: &assignmentID, CanvasCourseCode: courseCode, CanvasURL: url, CanvasUpdatedAt: updatedAt}, nil
}

func (s *SQLiteStore) GetTodoByCanvasID(assignmentID int64) (models.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ts todoScan
	err := s.db.QueryRow(
		`SELECT `+todoColumns+` FROM todos WHERE canvas_assignment_id = ?`, assignmentID,
	).Scan(ts.targets()...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.Todo{}, fmt.Errorf("canvas assignment %d: %w", assignmentID, ErrNotFound)
		}
		return models.Todo{}, fmt.Errorf("get todo by canvas id %d: %w", assignmentID, err)
	}
	ts.finish()
	return ts.todo, nil
}

func (s *SQLiteStore) UpdateCanvasTodo(id int, content string, dueAt *time.Time, courseCode, url, updatedAt string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("todo content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`UPDATE todos SET content = ?, due_at = ?, canvas_course_code = ?, canvas_url = ?, canvas_updated_at = ? WHERE id = ?`,
		content, formatDue(dueAt), courseCode, url, updatedAt, id,
	)
	if err != nil {
		return fmt.Errorf("update canvas todo %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update canvas todo %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("todo %d: %w", id, ErrNotFound)
	}
	return nil
}

func (s *SQLiteStore) AddCanvasNote(content string, dueAt *time.Time, assignmentID int64, courseCode, url, updatedAt string) (models.Note, error) {
	if strings.TrimSpace(content) == "" {
		return models.Note{}, fmt.Errorf("note content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO notes (content, created_at, due_at, origin, canvas_assignment_id, canvas_course_code, canvas_url, canvas_updated_at)
		 VALUES (?, ?, ?, 'canvas', ?, ?, ?, ?)`,
		content, now.Format(time.RFC3339Nano), formatDue(dueAt), assignmentID, courseCode, url, updatedAt,
	)
	if err != nil {
		return models.Note{}, fmt.Errorf("insert canvas note: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.Note{}, fmt.Errorf("canvas note id: %w", err)
	}
	return models.Note{ID: int(id), Content: content, CreatedAt: now, DueAt: dueAt, Origin: "canvas", CanvasAssignID: &assignmentID, CanvasCourseCode: courseCode, CanvasURL: url, CanvasUpdatedAt: updatedAt}, nil
}

func (s *SQLiteStore) GetNoteByCanvasID(assignmentID int64) (models.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ns noteScan
	err := s.db.QueryRow(
		`SELECT `+noteColumns+` FROM notes WHERE canvas_assignment_id = ?`, assignmentID,
	).Scan(ns.targets()...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.Note{}, fmt.Errorf("canvas assignment %d: %w", assignmentID, ErrNotFound)
		}
		return models.Note{}, fmt.Errorf("get note by canvas id %d: %w", assignmentID, err)
	}
	ns.finish()
	return ns.note, nil
}

func (s *SQLiteStore) UpdateCanvasNote(id int, content string, dueAt *time.Time, courseCode, url, updatedAt string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("note content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`UPDATE notes SET content = ?, due_at = ?, canvas_course_code = ?, canvas_url = ?, canvas_updated_at = ? WHERE id = ?`,
		content, formatDue(dueAt), courseCode, url, updatedAt, id,
	)
	if err != nil {
		return fmt.Errorf("update canvas note %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update canvas note %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("note %d: %w", id, ErrNotFound)
	}
	return nil
}

// GetLastCanvasSync returns the zero time when no sync has ever run.
func (s *SQLiteStore) GetLastCanvasSync() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var value string
	err := s.db.QueryRow(`SELECT value FROM canvas_meta WHERE key = 'last_sync_at'`).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("read last canvas sync: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last canvas sync %q: %w", value, err)
	}
	return t, nil
}

func (s *SQLiteStore) SetLastCanvasSync(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO canvas_meta (key, value) VALUES ('last_sync_at', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		t.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("write last canvas sync: %w", err)
	}
	return nil
}

// DetachCanvasCourse converts one course's imported todos/notes to plain
// local items: origin, external ID, URL, and upstream timestamp are
// cleared, while content (including the "[CODE] " prefix) and done state
// are left exactly as the user left them.
func (s *SQLiteStore) DetachCanvasCourse(courseID int64) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var code string
	err := s.db.QueryRow(`SELECT code FROM canvas_courses WHERE course_id = ?`, courseID).Scan(&code)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, fmt.Errorf("canvas course %d: %w", courseID, ErrNotFound)
		}
		return 0, 0, fmt.Errorf("read canvas course %d: %w", courseID, err)
	}
	detach := func(table string) (int, error) {
		res, err := s.db.Exec(
			`UPDATE `+table+` SET origin = '', canvas_assignment_id = NULL, canvas_url = '', canvas_updated_at = '' WHERE origin = 'canvas' AND canvas_course_code = ?`, code,
		)
		if err != nil {
			return 0, fmt.Errorf("detach %s for course %d: %w", table, courseID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("detach %s for course %d: %w", table, courseID, err)
		}
		return int(n), nil
	}
	todos, err := detach("todos")
	if err != nil {
		return 0, 0, err
	}
	notes, err := detach("notes")
	if err != nil {
		return 0, 0, err
	}
	return todos, notes, nil
}

// DismissCanvasAssignment tombstones one assignment ID. Any ID is allowed,
// including never-seen ones; re-dismissing is a no-op.
func (s *SQLiteStore) DismissCanvasAssignment(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO canvas_dismissed (assignment_id) VALUES (?)`, id); err != nil {
		return fmt.Errorf("dismiss canvas assignment %d: %w", id, err)
	}
	return nil
}

// UndismissCanvasAssignment removes a tombstone. Removing a non-dismissed
// ID is a silent no-op that still succeeds.
func (s *SQLiteStore) UndismissCanvasAssignment(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM canvas_dismissed WHERE assignment_id = ?`, id); err != nil {
		return fmt.Errorf("undismiss canvas assignment %d: %w", id, err)
	}
	return nil
}

func (s *SQLiteStore) IsCanvasDismissed(id int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM canvas_dismissed WHERE assignment_id = ?`, id).Scan(&n); err != nil {
		return false, fmt.Errorf("check dismissed canvas assignment %d: %w", id, err)
	}
	return n > 0, nil
}
