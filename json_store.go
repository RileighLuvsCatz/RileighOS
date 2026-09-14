package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// jsonFile is the on-disk shape of the JSON backend: both collections
// plus the next IDs, so IDs stay unique across restarts.
type jsonFile struct {
	Todos      []Todo `json:"todos"`
	Notes      []Note `json:"notes"`
	NextTodoID int    `json:"next_todo_id"`
	NextNoteID int    `json:"next_note_id"`
}

// JSONStore is a Store backed by a single flat JSON file.
// It exists as the Phase 1 starting point: dead simple, human-readable,
// and enough until querying/filtering starts to strain.
type JSONStore struct {
	mu         sync.Mutex
	path       string
	todos      []Todo
	notes      []Note
	nextTodoID int
	nextNoteID int
}

// Compile-time check that JSONStore satisfies Store.
var _ Store = (*JSONStore)(nil)

// OpenJSONStore loads (or creates) the JSON file at path.
func OpenJSONStore(path string) (*JSONStore, error) {
	s := &JSONStore{path: path, nextTodoID: 1, nextNoteID: 1}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *JSONStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh store, file created on first save
		}
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var f jsonFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	s.todos = f.Todos
	s.notes = f.Notes
	s.nextTodoID = max(f.NextTodoID, 1)
	s.nextNoteID = max(f.NextNoteID, 1)
	// Guard against hand-edited files where next-ID lags behind the data.
	for _, t := range s.todos {
		s.nextTodoID = max(s.nextTodoID, t.ID+1)
	}
	for _, n := range s.notes {
		s.nextNoteID = max(s.nextNoteID, n.ID+1)
	}
	if s.todos == nil {
		s.todos = []Todo{}
	}
	if s.notes == nil {
		s.notes = []Note{}
	}
	return nil
}

// save writes the full state atomically. Caller must hold s.mu.
func (s *JSONStore) save() error {
	f := jsonFile{
		Todos:      s.todos,
		Notes:      s.notes,
		NextTodoID: s.nextTodoID,
		NextNoteID: s.nextNoteID,
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode store: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	return nil
}

// Close is a no-op for the JSON backend; it exists so JSONStore
// satisfies the Store interface uniformly with SQLiteStore.
func (s *JSONStore) Close() error { return nil }

func (s *JSONStore) AddTodo(content string) (Todo, error) {
	if strings.TrimSpace(content) == "" {
		return Todo{}, fmt.Errorf("todo content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := Todo{ID: s.nextTodoID, Content: content, CreatedAt: time.Now()}
	s.nextTodoID++
	s.todos = append(s.todos, t)
	if err := s.save(); err != nil {
		return Todo{}, err
	}
	return t, nil
}

func (s *JSONStore) GetTodos() ([]Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Todo, len(s.todos))
	copy(out, s.todos)
	return out, nil
}

func (s *JSONStore) GetTodo(id int) (Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.todos {
		if t.ID == id {
			return t, nil
		}
	}
	return Todo{}, fmt.Errorf("todo %d: %w", id, ErrNotFound)
}

func (s *JSONStore) MarkTodoDone(id int) error {
	return s.setTodoDone(id, true)
}

func (s *JSONStore) MarkTodoUndone(id int) error {
	return s.setTodoDone(id, false)
}

func (s *JSONStore) setTodoDone(id int, done bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].Done = done
			return s.save()
		}
	}
	return fmt.Errorf("todo %d: %w", id, ErrNotFound)
}

func (s *JSONStore) DeleteTodo(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos = append(s.todos[:i], s.todos[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("todo %d: %w", id, ErrNotFound)
}

func (s *JSONStore) AddNote(content string) (Note, error) {
	if strings.TrimSpace(content) == "" {
		return Note{}, fmt.Errorf("note content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := Note{ID: s.nextNoteID, Content: content, CreatedAt: time.Now()}
	s.nextNoteID++
	s.notes = append(s.notes, n)
	if err := s.save(); err != nil {
		return Note{}, err
	}
	return n, nil
}

func (s *JSONStore) GetNotes() ([]Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Note, len(s.notes))
	copy(out, s.notes)
	return out, nil
}

func (s *JSONStore) GetNote(id int) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.notes {
		if n.ID == id {
			return n, nil
		}
	}
	return Note{}, fmt.Errorf("note %d: %w", id, ErrNotFound)
}

func (s *JSONStore) DeleteNote(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.notes {
		if s.notes[i].ID == id {
			s.notes = append(s.notes[:i], s.notes[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("note %d: %w", id, ErrNotFound)
}
