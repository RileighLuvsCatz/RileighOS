package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rdb/rileighos/internal/models"
)

func canvasDue(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestCanvasCourseGating(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)

			if courses, err := s.GetCanvasCourses(); err != nil || len(courses) != 0 {
				t.Fatalf("want no courses, got %+v, err %v", courses, err)
			}

			a, err := s.UpsertCanvasCourse(models.CanvasCourse{CourseID: 1, Code: "ART 150", Name: "Art", PendingConfirm: true, LastSeen: "x"})
			if err != nil {
				t.Fatal(err)
			}
			if !a.PendingConfirm || a.Excluded {
				t.Fatalf("new course must arrive pending, got %+v", a)
			}

			// Re-upsert refreshes upstream facts but preserves decisions.
			if err := s.ResolveCanvasCourse(1, true); err != nil {
				t.Fatal(err)
			}
			kept, err := s.UpsertCanvasCourse(models.CanvasCourse{CourseID: 1, Code: "ART 150!", Name: "Art!", PendingConfirm: true, LastSeen: "y"})
			if err != nil {
				t.Fatal(err)
			}
			if !kept.Excluded || kept.PendingConfirm || kept.Code != "ART 150!" || kept.Name != "Art!" {
				t.Fatalf("upsert must preserve exclusion, got %+v", kept)
			}

			// Resolve clears pending either way.
			if err := s.ResolveCanvasCourse(1, false); err != nil {
				t.Fatal(err)
			}
			courses, err := s.GetCanvasCourses()
			if err != nil {
				t.Fatal(err)
			}
			if len(courses) != 1 || courses[0].Excluded || courses[0].PendingConfirm {
				t.Fatalf("want included confirmed course, got %+v", courses)
			}

			if err := s.ResolveCanvasCourse(999, false); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
		})
	}
}

func TestCanvasTodoCRUD(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)
			due := canvasDue("2026-10-01T23:59:00-05:00")

			if _, err := s.GetTodoByCanvasID(101); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}

			created, err := s.AddCanvasTodo("[ART 150] Essay", due, 101, "ART 150", "http://x/101", "v1")
			if err != nil {
				t.Fatal(err)
			}
			if created.Origin != "canvas" || created.CanvasAssignID == nil || *created.CanvasAssignID != 101 {
				t.Fatalf("missing provenance: %+v", created)
			}
			if created.DueAt == nil || !created.DueAt.Equal(*due) {
				t.Fatalf("due not preserved: %+v", created.DueAt)
			}

			got, err := s.GetTodoByCanvasID(101)
			if err != nil || got.ID != created.ID {
				t.Fatalf("want %+v, got %+v, err %v", created, got, err)
			}

			due2 := canvasDue("2026-10-05T23:59:00-05:00")
			if err := s.UpdateCanvasTodo(created.ID, "[ART 150] Essay!", due2, "ART 150", "http://x/101b", "v2"); err != nil {
				t.Fatal(err)
			}
			updated, err := s.GetTodo(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Content != "[ART 150] Essay!" || updated.DueAt == nil || !updated.DueAt.Equal(*due2) || updated.CanvasURL != "http://x/101b" {
				t.Fatalf("update not applied: %+v", updated)
			}

			if err := s.UpdateCanvasTodo(999, "x", nil, "", "", ""); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
			if _, err := s.AddCanvasTodo("   ", nil, 102, "", "", ""); err == nil {
				t.Fatal("want error for blank canvas todo")
			}
		})
	}
}

func TestCanvasNoteCRUD(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)

			if _, err := s.GetNoteByCanvasID(102); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
			created, err := s.AddCanvasNote("[ART 150] Reading", nil, 102, "ART 150", "http://x/102", "v1")
			if err != nil {
				t.Fatal(err)
			}
			if created.Origin != "canvas" {
				t.Fatalf("missing origin: %+v", created)
			}
			if err := s.UpdateCanvasNote(created.ID, "[ART 150] Reading!", nil, "ART 150", "http://x/102", "v2"); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetNoteByCanvasID(102)
			if err != nil || got.Content != "[ART 150] Reading!" {
				t.Fatalf("want updated note, got %+v, err %v", got, err)
			}
			if err := s.UpdateCanvasNote(999, "x", nil, "", "", ""); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
		})
	}
}

func TestCanvasLastSync(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)
			if last, err := s.GetLastCanvasSync(); err != nil || !last.IsZero() {
				t.Fatalf("want zero time, got %v, err %v", last, err)
			}
			when := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
			if err := s.SetLastCanvasSync(when); err != nil {
				t.Fatal(err)
			}
			if last, err := s.GetLastCanvasSync(); err != nil || !last.Equal(when) {
				t.Fatalf("want %v, got %v, err %v", when, last, err)
			}
		})
	}
}

// TestCanvasPersistsAcrossReopen proves Canvas rows survive restarts on
// both backends — and that migrating a pre-Canvas database (plain CREATE
// TABLE shape) picks up the new columns and tables cleanly.
func TestCanvasPersistsAcrossReopen(t *testing.T) {
	openers := map[string]func(dir string) (FullStore, error){
		"json":   func(dir string) (FullStore, error) { return OpenJSONStore(filepath.Join(dir, "c.json")) },
		"sqlite": func(dir string) (FullStore, error) { return OpenSQLiteStore(filepath.Join(dir, "c.db")) },
	}
	for backend, opener := range openers {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			s, err := opener(dir)
			if err != nil {
				t.Fatal(err)
			}
			due := canvasDue("2026-10-01T23:59:00-05:00")
			if _, err := s.UpsertCanvasCourse(models.CanvasCourse{CourseID: 7, Code: "ART 150", Name: "Drawing", Excluded: true, LastSeen: "v1"}); err != nil {
				t.Fatal(err)
			}
			added, err := s.AddCanvasTodo("[ART 150] Essay", due, 701, "ART 150", "http://x/701", "v3")
			if err != nil {
				t.Fatal(err)
			}
			when := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
			if err := s.SetLastCanvasSync(when); err != nil {
				t.Fatal(err)
			}
			s.Close()

			reopened, err := opener(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			courses, err := reopened.GetCanvasCourses()
			if err != nil || len(courses) != 1 || !courses[0].Excluded || courses[0].PendingConfirm {
				t.Fatalf("course decision lost: %+v, err %v", courses, err)
			}
			got, err := reopened.GetTodoByCanvasID(701)
			if err != nil || got.ID != added.ID || got.Content != "[ART 150] Essay" {
				t.Fatalf("todo lost: %+v, err %v", got, err)
			}
			if got.DueAt == nil || !got.DueAt.Equal(*due) {
				t.Fatalf("due lost: %+v", got.DueAt)
			}
			if last, err := reopened.GetLastCanvasSync(); err != nil || !last.Equal(when) {
				t.Fatalf("last sync lost: %v, err %v", last, err)
			}
		})
	}
}
