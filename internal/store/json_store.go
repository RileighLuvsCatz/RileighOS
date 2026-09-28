package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"rdb/rileighos/internal/models"
)

// jsonFile is the on-disk shape of the JSON backend: both collections
// plus the next IDs, so IDs stay unique across restarts.
type jsonFile struct {
	Todos           []models.Todo         `json:"todos"`
	Notes           []models.Note         `json:"notes"`
	Checkoffs       []models.Checkoff     `json:"checkoffs"`
	CheckoffDays    []CheckoffDay         `json:"checkoff_days"`
	CanvasCourses   []models.CanvasCourse `json:"canvas_courses"`
	CanvasLastSync  string                `json:"canvas_last_sync"`
	CanvasDismissed []int64               `json:"canvas_dismissed"`
	NextTodoID      int                   `json:"next_todo_id"`
	NextNoteID      int                   `json:"next_note_id"`
	NextCheckoffID  int                   `json:"next_checkoff_id"`
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
	mu              sync.Mutex
	path            string
	todos           []models.Todo
	notes           []models.Note
	checkoffs       []models.Checkoff
	days            []CheckoffDay
	canvasCourses   []models.CanvasCourse
	canvasLastSync  string
	canvasDismissed map[int64]bool
	nextTodoID      int
	nextNoteID      int
	nextCheckoffID  int
}

// Compile-time check that JSONStore satisfies Store.
var _ Store = (*JSONStore)(nil)

// Compile-time check that JSONStore satisfies FullStore (Canvas sync).
var _ FullStore = (*JSONStore)(nil)

// OpenJSONStore loads (or creates) the JSON file at path.
func OpenJSONStore(path string) (*JSONStore, error) {
	s := &JSONStore{path: path, nextTodoID: 1, nextNoteID: 1, nextCheckoffID: 1, canvasDismissed: map[int64]bool{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	if s.canvasDismissed == nil {
		s.canvasDismissed = map[int64]bool{}
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
	s.canvasCourses = f.CanvasCourses
	s.canvasLastSync = f.CanvasLastSync
	s.canvasDismissed = map[int64]bool{}
	for _, id := range f.CanvasDismissed {
		s.canvasDismissed[id] = true
	}
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
		s.todos = []models.Todo{}
	}
	if s.notes == nil {
		s.notes = []models.Note{}
	}
	if s.checkoffs == nil {
		s.checkoffs = []models.Checkoff{}
	}
	if s.days == nil {
		s.days = []CheckoffDay{}
	}
	if s.canvasCourses == nil {
		s.canvasCourses = []models.CanvasCourse{}
	}
	return nil
}

// save writes the full state atomically. Caller must hold s.mu.
func (s *JSONStore) save() error {
	dismissed := make([]int64, 0, len(s.canvasDismissed))
	for id := range s.canvasDismissed {
		dismissed = append(dismissed, id)
	}
	sort.Slice(dismissed, func(i, j int) bool { return dismissed[i] < dismissed[j] })
	f := jsonFile{
		Todos:           s.todos,
		Notes:           s.notes,
		Checkoffs:       s.checkoffs,
		CheckoffDays:    s.days,
		CanvasCourses:   s.canvasCourses,
		CanvasLastSync:  s.canvasLastSync,
		CanvasDismissed: dismissed,
		NextTodoID:      s.nextTodoID,
		NextNoteID:      s.nextNoteID,
		NextCheckoffID:  s.nextCheckoffID,
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

func (s *JSONStore) AddTodo(content string) (models.Todo, error) {
	if strings.TrimSpace(content) == "" {
		return models.Todo{}, fmt.Errorf("todo content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := models.Todo{ID: s.nextTodoID, Content: content, CreatedAt: time.Now()}
	s.nextTodoID++
	s.todos = append(s.todos, t)
	if err := s.save(); err != nil {
		return models.Todo{}, err
	}
	return t, nil
}

func (s *JSONStore) GetTodos() ([]models.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]models.Todo, len(s.todos))
	copy(out, s.todos)
	return out, nil
}

func (s *JSONStore) GetTodo(id int) (models.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.todos {
		if t.ID == id {
			return t, nil
		}
	}
	return models.Todo{}, fmt.Errorf("todo %d: %w", id, ErrNotFound)
}

func (s *JSONStore) MarkTodoDone(id int) error {
	return s.setTodoDone(id, true)
}

func (s *JSONStore) MarkTodoUndone(id int) error {
	return s.setTodoDone(id, false)
}

func (s *JSONStore) SetTodoWorkDate(id int, day string) error {
	day, err := NormalizeWorkDate(day)
	if err != nil {
		return err
	}
	return s.updateTodoWorkDate(id, &day)
}

func (s *JSONStore) ClearTodoWorkDate(id int) error {
	return s.updateTodoWorkDate(id, nil)
}

func (s *JSONStore) updateTodoWorkDate(id int, day *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].WorkDate = day
			return s.save()
		}
	}
	return fmt.Errorf("todo %d: %w", id, ErrNotFound)
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

func (s *JSONStore) AddNote(content string) (models.Note, error) {
	if strings.TrimSpace(content) == "" {
		return models.Note{}, fmt.Errorf("note content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := models.Note{ID: s.nextNoteID, Content: content, CreatedAt: time.Now()}
	s.nextNoteID++
	s.notes = append(s.notes, n)
	if err := s.save(); err != nil {
		return models.Note{}, err
	}
	return n, nil
}

func (s *JSONStore) GetNotes() ([]models.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]models.Note, len(s.notes))
	copy(out, s.notes)
	return out, nil
}

func (s *JSONStore) GetNote(id int) (models.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.notes {
		if n.ID == id {
			return n, nil
		}
	}
	return models.Note{}, fmt.Errorf("note %d: %w", id, ErrNotFound)
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

func (s *JSONStore) AddCheckoff(name string) (models.Checkoff, error) {
	if strings.TrimSpace(name) == "" {
		return models.Checkoff{}, fmt.Errorf("checkoff name must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := models.Checkoff{ID: s.nextCheckoffID, Name: name, CreatedAt: time.Now()}
	s.nextCheckoffID++
	s.checkoffs = append(s.checkoffs, c)
	if err := s.save(); err != nil {
		return models.Checkoff{}, err
	}
	return c, nil
}

func (s *JSONStore) GetCheckoffs() ([]models.Checkoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]models.Checkoff, len(s.checkoffs))
	copy(out, s.checkoffs)
	return out, nil
}

func (s *JSONStore) GetCheckoff(id int) (models.Checkoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.checkoffs {
		if c.ID == id {
			return c, nil
		}
	}
	return models.Checkoff{}, fmt.Errorf("checkoff %d: %w", id, ErrNotFound)
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

func (s *JSONStore) GetToday() (models.TodayView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := Today()

	overdue, planned, due := groupTodayTodos(s.todos, today)
	views := []models.CheckoffView{}
	for _, c := range s.checkoffs {
		days := []string{}
		for _, d := range s.days {
			if d.CheckoffID == c.ID {
				days = append(days, d.Day)
			}
		}
		sort.Strings(days)
		checkedToday := len(days) > 0 && days[len(days)-1] == today
		views = append(views, models.CheckoffView{
			Checkoff:     c,
			Days:         days,
			Streak:       CurrentStreak(days, today),
			CheckedToday: checkedToday,
		})
	}
	return models.TodayView{Date: today, OverdueTodos: overdue, PlannedTodos: planned, DueTodos: due, Checkoffs: views}, nil
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

// --- Canvas sync support ---

func (s *JSONStore) GetCanvasCourses() ([]models.CanvasCourse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]models.CanvasCourse, len(s.canvasCourses))
	copy(out, s.canvasCourses)
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (s *JSONStore) UpsertCanvasCourse(c models.CanvasCourse) (models.CanvasCourse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.canvasCourses {
		if s.canvasCourses[i].CourseID == c.CourseID {
			// Preserve the user's decision; refresh upstream facts only.
			s.canvasCourses[i].Code = c.Code
			s.canvasCourses[i].Name = c.Name
			s.canvasCourses[i].LastSeen = c.LastSeen
			got := s.canvasCourses[i]
			if err := s.save(); err != nil {
				return models.CanvasCourse{}, err
			}
			return got, nil
		}
	}
	s.canvasCourses = append(s.canvasCourses, c)
	if err := s.save(); err != nil {
		return models.CanvasCourse{}, err
	}
	return c, nil
}

func (s *JSONStore) ResolveCanvasCourse(courseID int64, excluded bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.canvasCourses {
		if s.canvasCourses[i].CourseID == courseID {
			s.canvasCourses[i].Excluded = excluded
			s.canvasCourses[i].PendingConfirm = false
			return s.save()
		}
	}
	return fmt.Errorf("canvas course %d: %w", courseID, ErrNotFound)
}

func (s *JSONStore) AddCanvasTodo(content string, dueAt *time.Time, assignmentID int64, courseCode, url, updatedAt string) (models.Todo, error) {
	if strings.TrimSpace(content) == "" {
		return models.Todo{}, fmt.Errorf("todo content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := models.Todo{ID: s.nextTodoID, Content: content, CreatedAt: time.Now(), DueAt: dueAt, Origin: "canvas", CanvasAssignID: &assignmentID, CanvasCourseCode: courseCode, CanvasURL: url, CanvasUpdatedAt: updatedAt}
	s.nextTodoID++
	s.todos = append(s.todos, t)
	if err := s.save(); err != nil {
		return models.Todo{}, err
	}
	return t, nil
}

func (s *JSONStore) GetTodoByCanvasID(assignmentID int64) (models.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.todos {
		if t.CanvasAssignID != nil && *t.CanvasAssignID == assignmentID {
			return t, nil
		}
	}
	return models.Todo{}, fmt.Errorf("canvas assignment %d: %w", assignmentID, ErrNotFound)
}

func (s *JSONStore) UpdateCanvasTodo(id int, content string, dueAt *time.Time, courseCode, url, updatedAt string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("todo content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].Content = content
			s.todos[i].DueAt = dueAt
			s.todos[i].CanvasCourseCode = courseCode
			s.todos[i].CanvasURL = url
			s.todos[i].CanvasUpdatedAt = updatedAt
			return s.save()
		}
	}
	return fmt.Errorf("todo %d: %w", id, ErrNotFound)
}

func (s *JSONStore) DetachCanvasTodo(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.todos {
		if s.todos[i].ID == id {
			s.todos[i].Origin = ""
			s.todos[i].CanvasAssignID = nil
			s.todos[i].CanvasURL = ""
			s.todos[i].CanvasUpdatedAt = ""
			return s.save()
		}
	}
	return fmt.Errorf("todo %d: %w", id, ErrNotFound)
}

func (s *JSONStore) AddCanvasNote(content string, dueAt *time.Time, assignmentID int64, courseCode, url, updatedAt string) (models.Note, error) {
	if strings.TrimSpace(content) == "" {
		return models.Note{}, fmt.Errorf("note content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := models.Note{ID: s.nextNoteID, Content: content, CreatedAt: time.Now(), DueAt: dueAt, Origin: "canvas", CanvasAssignID: &assignmentID, CanvasCourseCode: courseCode, CanvasURL: url, CanvasUpdatedAt: updatedAt}
	s.nextNoteID++
	s.notes = append(s.notes, n)
	if err := s.save(); err != nil {
		return models.Note{}, err
	}
	return n, nil
}

func (s *JSONStore) GetNoteByCanvasID(assignmentID int64) (models.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.notes {
		if n.CanvasAssignID != nil && *n.CanvasAssignID == assignmentID {
			return n, nil
		}
	}
	return models.Note{}, fmt.Errorf("canvas assignment %d: %w", assignmentID, ErrNotFound)
}

func (s *JSONStore) UpdateCanvasNote(id int, content string, dueAt *time.Time, courseCode, url, updatedAt string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("note content must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.notes {
		if s.notes[i].ID == id {
			s.notes[i].Content = content
			s.notes[i].DueAt = dueAt
			s.notes[i].CanvasCourseCode = courseCode
			s.notes[i].CanvasURL = url
			s.notes[i].CanvasUpdatedAt = updatedAt
			return s.save()
		}
	}
	return fmt.Errorf("note %d: %w", id, ErrNotFound)
}

func (s *JSONStore) GetLastCanvasSync() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.canvasLastSync == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.canvasLastSync)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last canvas sync %q: %w", s.canvasLastSync, err)
	}
	return t, nil
}

func (s *JSONStore) SetLastCanvasSync(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canvasLastSync = t.UTC().Format(time.RFC3339Nano)
	return s.save()
}

// DetachCanvasCourse converts one course's imported todos/notes to plain
// local items: origin, external ID, URL, and upstream timestamp are
// cleared, while content (including the "[CODE] " prefix) and done state
// are left exactly as the user left them.
func (s *JSONStore) DetachCanvasCourse(courseID int64) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := ""
	for _, c := range s.canvasCourses {
		if c.CourseID == courseID {
			code = c.Code
		}
	}
	if code == "" {
		return 0, 0, fmt.Errorf("canvas course %d: %w", courseID, ErrNotFound)
	}
	todos, notes := 0, 0
	for i := range s.todos {
		if s.todos[i].Origin == "canvas" && s.todos[i].CanvasCourseCode == code {
			s.todos[i].Origin = ""
			s.todos[i].CanvasAssignID = nil
			s.todos[i].CanvasURL = ""
			s.todos[i].CanvasUpdatedAt = ""
			todos++
		}
	}
	for i := range s.notes {
		if s.notes[i].Origin == "canvas" && s.notes[i].CanvasCourseCode == code {
			s.notes[i].Origin = ""
			s.notes[i].CanvasAssignID = nil
			s.notes[i].CanvasURL = ""
			s.notes[i].CanvasUpdatedAt = ""
			notes++
		}
	}
	return todos, notes, s.save()
}

func (s *JSONStore) DismissCanvasAssignment(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.canvasDismissed == nil {
		s.canvasDismissed = map[int64]bool{}
	}
	if s.canvasDismissed[id] {
		return nil
	}
	s.canvasDismissed[id] = true
	return s.save()
}

func (s *JSONStore) UndismissCanvasAssignment(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.canvasDismissed == nil {
		s.canvasDismissed = map[int64]bool{}
		return nil
	}
	if !s.canvasDismissed[id] {
		return nil
	}
	delete(s.canvasDismissed, id)
	return s.save()
}

func (s *JSONStore) IsCanvasDismissed(id int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canvasDismissed[id], nil
}
