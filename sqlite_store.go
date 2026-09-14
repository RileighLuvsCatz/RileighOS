package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
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
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Close releases the database connection.
func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) AddTodo(content string) (Todo, error) {
	if strings.TrimSpace(content) == "" {
		return Todo{}, fmt.Errorf("todo content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO todos (content, done, created_at) VALUES (?, 0, ?)`,
		content, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Todo{}, fmt.Errorf("insert todo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Todo{}, fmt.Errorf("todo id: %w", err)
	}
	return Todo{ID: int(id), Content: content, CreatedAt: now}, nil
}

func (s *SQLiteStore) GetTodos() ([]Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, content, done, created_at FROM todos ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	defer rows.Close()
	todos := []Todo{}
	for rows.Next() {
		var t Todo
		var done int
		var created string
		if err := rows.Scan(&t.ID, &t.Content, &done, &created); err != nil {
			return nil, fmt.Errorf("scan todo: %w", err)
		}
		t.Done = done != 0
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		todos = append(todos, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	return todos, nil
}

func (s *SQLiteStore) GetTodo(id int) (Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Todo
	var done int
	var created string
	err := s.db.QueryRow(
		`SELECT id, content, done, created_at FROM todos WHERE id = ?`, id,
	).Scan(&t.ID, &t.Content, &done, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Todo{}, fmt.Errorf("todo %d: %w", id, ErrNotFound)
		}
		return Todo{}, fmt.Errorf("get todo %d: %w", id, err)
	}
	t.Done = done != 0
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return t, nil
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

func (s *SQLiteStore) AddNote(content string) (Note, error) {
	if strings.TrimSpace(content) == "" {
		return Note{}, fmt.Errorf("note content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO notes (content, created_at) VALUES (?, ?)`,
		content, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Note{}, fmt.Errorf("insert note: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Note{}, fmt.Errorf("note id: %w", err)
	}
	return Note{ID: int(id), Content: content, CreatedAt: now}, nil
}

func (s *SQLiteStore) GetNotes() ([]Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, content, created_at FROM notes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()
	notes := []Note{}
	for rows.Next() {
		var n Note
		var created string
		if err := rows.Scan(&n.ID, &n.Content, &created); err != nil {
			return nil, fmt.Errorf("scan note: %w", err)
		}
		n.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	return notes, nil
}

func (s *SQLiteStore) GetNote(id int) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n Note
	var created string
	err := s.db.QueryRow(
		`SELECT id, content, created_at FROM notes WHERE id = ?`, id,
	).Scan(&n.ID, &n.Content, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Note{}, fmt.Errorf("note %d: %w", id, ErrNotFound)
		}
		return Note{}, fmt.Errorf("get note %d: %w", id, err)
	}
	n.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return n, nil
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

func (s *SQLiteStore) AddCheckoff(name string) (Checkoff, error) {
	if strings.TrimSpace(name) == "" {
		return Checkoff{}, fmt.Errorf("checkoff name must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO checkoffs (name, created_at) VALUES (?, ?)`,
		name, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Checkoff{}, fmt.Errorf("insert checkoff: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Checkoff{}, fmt.Errorf("checkoff id: %w", err)
	}
	return Checkoff{ID: int(id), Name: name, CreatedAt: now}, nil
}

func (s *SQLiteStore) GetCheckoffs() ([]Checkoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, name, created_at FROM checkoffs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list checkoffs: %w", err)
	}
	defer rows.Close()
	checkoffs := []Checkoff{}
	for rows.Next() {
		var c Checkoff
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

func (s *SQLiteStore) GetCheckoff(id int) (Checkoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCheckoff(id)
}

// getCheckoff is the lock-held helper behind GetCheckoff and the day
// methods, so existence checks and day writes stay atomic.
func (s *SQLiteStore) getCheckoff(id int) (Checkoff, error) {
	var c Checkoff
	var created string
	err := s.db.QueryRow(
		`SELECT id, name, created_at FROM checkoffs WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Checkoff{}, fmt.Errorf("checkoff %d: %w", id, ErrNotFound)
		}
		return Checkoff{}, fmt.Errorf("get checkoff %d: %w", id, err)
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

func (s *SQLiteStore) GetToday() (TodayView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := Today()

	todoRows, err := s.db.Query(`SELECT id, content, done, created_at FROM todos ORDER BY id`)
	if err != nil {
		return TodayView{}, fmt.Errorf("today todos: %w", err)
	}
	defer todoRows.Close()
	open := []Todo{}
	for todoRows.Next() {
		var t Todo
		var done int
		var created string
		if err := todoRows.Scan(&t.ID, &t.Content, &done, &created); err != nil {
			return TodayView{}, fmt.Errorf("scan today todo: %w", err)
		}
		if done != 0 {
			continue
		}
		t.Done = false
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		open = append(open, t)
	}
	if err := todoRows.Err(); err != nil {
		return TodayView{}, fmt.Errorf("today todos: %w", err)
	}

	coRows, err := s.db.Query(`SELECT id, name, created_at FROM checkoffs ORDER BY id`)
	if err != nil {
		return TodayView{}, fmt.Errorf("today checkoffs: %w", err)
	}
	defer coRows.Close()
	checkoffs := []Checkoff{}
	for coRows.Next() {
		var c Checkoff
		var created string
		if err := coRows.Scan(&c.ID, &c.Name, &created); err != nil {
			return TodayView{}, fmt.Errorf("scan today checkoff: %w", err)
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		checkoffs = append(checkoffs, c)
	}
	if err := coRows.Err(); err != nil {
		return TodayView{}, fmt.Errorf("today checkoffs: %w", err)
	}

	dayRows, err := s.db.Query(`SELECT checkoff_id, day FROM checkoff_days ORDER BY checkoff_id, day`)
	if err != nil {
		return TodayView{}, fmt.Errorf("today checkoff days: %w", err)
	}
	defer dayRows.Close()
	byCheckoff := map[int][]string{}
	for dayRows.Next() {
		var cid int
		var day string
		if err := dayRows.Scan(&cid, &day); err != nil {
			return TodayView{}, fmt.Errorf("scan today checkoff day: %w", err)
		}
		byCheckoff[cid] = append(byCheckoff[cid], day)
	}
	if err := dayRows.Err(); err != nil {
		return TodayView{}, fmt.Errorf("today checkoff days: %w", err)
	}

	views := []CheckoffView{}
	for _, c := range checkoffs {
		days := byCheckoff[c.ID]
		if days == nil {
			days = []string{}
		}
		// Days arrive ordered ascending, so the last one is the latest.
		checkedToday := len(days) > 0 && days[len(days)-1] == today
		views = append(views, CheckoffView{
			Checkoff:     c,
			Days:         days,
			Streak:       CurrentStreak(days, today),
			CheckedToday: checkedToday,
		})
	}
	return TodayView{Date: today, OpenTodos: open, Checkoffs: views}, nil
}
