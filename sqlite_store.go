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
