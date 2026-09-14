package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// jsonFile is the on-disk shape of the JSON backend: both collections
// plus the next IDs, so IDs stay unique across restarts.
type jsonFile struct {
	Todos          []Todo        `json:"todos"`
	Notes          []Note        `json:"notes"`
	Checkoffs      []Checkoff    `json:"checkoffs"`
	CheckoffDays   []CheckoffDay `json:"checkoff_days"`
	NextTodoID     int           `json:"next_todo_id"`
	NextNoteID     int           `json:"next_note_id"`
	NextCheckoffID int           `json:"next_checkoff_id"`
}

// CheckoffDay is one checked day for one checkoff. Days live as a flat
// slice (like todos and notes) rather than nested, keeping the file
// greppable and the save path uniform.
type CheckoffDay struct {
	CheckoffID int    `json:"checkoff_id"`
	Day        string `json:"day"`
}

// JSONStore is a Store backed by a single flat JSON file.
// It exists as the Phase 1 starting point: dead simple, human-readable,
// and enough until querying/filtering starts to strain.
type JSONStore struct {
	mu             sync.Mutex
	path           string
	todos          []Todo
	notes          []Note
	checkoffs      []Checkoff
	days           []CheckoffDay
	nextTodoID     int
	nextNoteID     int
	nextCheckoffID int
}

// Compile-time check that JSONStore satisfies Store.
var _ Store = (*JSONStore)(nil)

// OpenJSONStore loads (or creates) the JSON file at path.
func OpenJSONStore(path string) (*JSONStore, error) {
	s := &JSONStore{path: path, nextTodoID: 1, nextNoteID: 1, nextCheckoffID: 1}
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
	s.checkoffs = f.Checkoffs
	s.days = f.CheckoffDays
	s.nextTodoID = max(f.NextTodoID, 1)
	s.nextNoteID = max(f.NextNoteID, 1)
	s.nextCheckoffID = max(f.NextCheckoffID, 1)
	// Guard against hand-edited files where next-ID lags behind the data.
	for _, t := range s.todos {
		s.nextTodoID = max(s.nextTodoID, t.ID+1)
	}
	for _, n := range s.notes {
		s.nextNoteID = max(s.nextNoteID, n.ID+1)
	}
	for _, c := range s.checkoffs {
		s.nextCheckoffID = max(s.nextCheckoffID, c.ID+1)
	}
	if s.todos == nil {
		s.todos = []Todo{}
	}
	if s.notes == nil {
		s.notes = []Note{}
	}
	if s.checkoffs == nil {
		s.checkoffs = []Checkoff{}
	}
	if s.days == nil {
		s.days = []CheckoffDay{}
	}
	return nil
}

// save writes the full state atomically. Caller must hold s.mu.
func (s *JSONStore) save() error {
	f := jsonFile{
		Todos:          s.todos,
		Notes:          s.notes,
		Checkoffs:      s.checkoffs,
		CheckoffDays:   s.days,
		NextTodoID:     s.nextTodoID,
		NextNoteID:     s.nextNoteID,
		NextCheckoffID: s.nextCheckoffID,
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

func (s *JSONStore) AddCheckoff(name string) (Checkoff, error) {
	if strings.TrimSpace(name) == "" {
		return Checkoff{}, fmt.Errorf("checkoff name must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := Checkoff{ID: s.nextCheckoffID, Name: name, CreatedAt: time.Now()}
	s.nextCheckoffID++
	s.checkoffs = append(s.checkoffs, c)
	if err := s.save(); err != nil {
		return Checkoff{}, err
	}
	return c, nil
}

func (s *JSONStore) GetCheckoffs() ([]Checkoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Checkoff, len(s.checkoffs))
	copy(out, s.checkoffs)
	return out, nil
}

func (s *JSONStore) GetCheckoff(id int) (Checkoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.checkoffs {
		if c.ID == id {
			return c, nil
		}
	}
	return Checkoff{}, fmt.Errorf("checkoff %d: %w", id, ErrNotFound)
}

func (s *JSONStore) DeleteCheckoff(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i := 0; i < len(s.checkoffs); {
		if s.checkoffs[i].ID == id {
			s.checkoffs = append(s.checkoffs[:i], s.checkoffs[i+1:]...)
			found = true
		} else {
			i++
		}
	}
	if !found {
		return fmt.Errorf("checkoff %d: %w", id, ErrNotFound)
	}
	// A deleted habit takes its checked days with it.
	kept := s.days[:0]
	for _, d := range s.days {
		if d.CheckoffID != id {
			kept = append(kept, d)
		}
	}
	// Clear the tail so the removed entries cannot leak via the old array.
	for i := len(kept); i < len(s.days); i++ {
		s.days[i] = CheckoffDay{}
	}
	s.days = kept
	return s.save()
}

func (s *JSONStore) CheckDay(id int, day string) error {
	if day == "" {
		day = Today()
	}
	if !ValidDay(day) {
		return fmt.Errorf("invalid day %q: want YYYY-MM-DD", day)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasCheckoff(id) {
		return fmt.Errorf("checkoff %d: %w", id, ErrNotFound)
	}
	for _, d := range s.days {
		if d.CheckoffID == id && d.Day == day {
			return nil // already checked: idempotent, no rewrite
		}
	}
	s.days = append(s.days, CheckoffDay{CheckoffID: id, Day: day})
	return s.save()
}

func (s *JSONStore) UncheckDay(id int, day string) error {
	if day == "" {
		day = Today()
	}
	if !ValidDay(day) {
		return fmt.Errorf("invalid day %q: want YYYY-MM-DD", day)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasCheckoff(id) {
		return fmt.Errorf("checkoff %d: %w", id, ErrNotFound)
	}
	kept := s.days[:0]
	for _, d := range s.days {
		if d.CheckoffID != id || d.Day != day {
			kept = append(kept, d)
		}
	}
	for i := len(kept); i < len(s.days); i++ {
		s.days[i] = CheckoffDay{}
	}
	s.days = kept
	return s.save()
}

func (s *JSONStore) GetCheckoffDays(id int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasCheckoff(id) {
		return nil, fmt.Errorf("checkoff %d: %w", id, ErrNotFound)
	}
	days := []string{}
	for _, d := range s.days {
		if d.CheckoffID == id {
			days = append(days, d.Day)
		}
	}
	sort.Strings(days)
	return days, nil
}

func (s *JSONStore) GetToday() (TodayView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := Today()

	open := []Todo{}
	for _, t := range s.todos {
		if !t.Done {
			open = append(open, t)
		}
	}
	views := []CheckoffView{}
	for _, c := range s.checkoffs {
		days := []string{}
		for _, d := range s.days {
			if d.CheckoffID == c.ID {
				days = append(days, d.Day)
			}
		}
		sort.Strings(days)
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

// hasCheckoff reports whether the checkoff exists. Caller must hold s.mu.
func (s *JSONStore) hasCheckoff(id int) bool {
	for _, c := range s.checkoffs {
		if c.ID == id {
			return true
		}
	}
	return false
}
