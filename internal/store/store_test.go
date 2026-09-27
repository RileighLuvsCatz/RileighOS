package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// openTestStores returns one factory per backend so every test below
// runs against both JSON and SQLite through the Store interface only.
func openTestStores(t *testing.T) map[string]func(t *testing.T) FullStore {
	t.Helper()
	return map[string]func(t *testing.T) FullStore{
		"json": func(t *testing.T) FullStore {
			t.Helper()
			s, err := OpenJSONStore(filepath.Join(t.TempDir(), "rileighos.json"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		},
		"sqlite": func(t *testing.T) FullStore {
			t.Helper()
			s, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "rileighos.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		},
	}
}

func TestTodoCRUD(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)

			a, err := s.AddTodo("buy milk")
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.AddTodo("write report")
			if err != nil {
				t.Fatal(err)
			}
			if a.ID == b.ID {
				t.Fatalf("ids must be unique, both were %d", a.ID)
			}

			todos, err := s.GetTodos()
			if err != nil {
				t.Fatal(err)
			}
			if len(todos) != 2 {
				t.Fatalf("want 2 todos, got %d", len(todos))
			}

			if err := s.MarkTodoDone(a.ID); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetTodo(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Done {
				t.Fatalf("todo %d should be done", a.ID)
			}

			if err := s.MarkTodoUndone(a.ID); err != nil {
				t.Fatal(err)
			}
			got, err = s.GetTodo(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Done {
				t.Fatalf("todo %d should be open after undone", a.ID)
			}

			if err := s.DeleteTodo(b.ID); err != nil {
				t.Fatal(err)
			}
			todos, err = s.GetTodos()
			if err != nil {
				t.Fatal(err)
			}
			if len(todos) != 1 || todos[0].ID != a.ID {
				t.Fatalf("want only todo %d left, got %+v", a.ID, todos)
			}
		})
	}
}

func TestTodoNotFound(t *testing.T) {
	cases := []struct {
		name string
		op   func(s Store) error
	}{
		{"get", func(s Store) error { _, err := s.GetTodo(999); return err }},
		{"done", func(s Store) error { return s.MarkTodoDone(999) }},
		{"undone", func(s Store) error { return s.MarkTodoUndone(999) }},
		{"delete", func(s Store) error { return s.DeleteTodo(999) }},
	}
	for backend, open := range openTestStores(t) {
		for _, tc := range cases {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				s := open(t)
				if err := tc.op(s); !errors.Is(err, ErrNotFound) {
					t.Fatalf("want ErrNotFound, got %v", err)
				}
			})
		}
	}
}

func TestTodoRejectsEmpty(t *testing.T) {
	for backend, open := range openTestStores(t) {
		for _, content := range []string{"", "   "} {
			t.Run(backend+"/empty", func(t *testing.T) {
				if _, err := open(t).AddTodo(content); err == nil {
					t.Fatal("want error for empty todo content")
				}
			})
		}
	}
}

func TestNoteCRUD(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)

			a, err := s.AddNote("idea: build a thing")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddNote("second note"); err != nil {
				t.Fatal(err)
			}

			notes, err := s.GetNotes()
			if err != nil {
				t.Fatal(err)
			}
			if len(notes) != 2 {
				t.Fatalf("want 2 notes, got %d", len(notes))
			}

			got, err := s.GetNote(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Content != "idea: build a thing" {
				t.Fatalf("unexpected note content %q", got.Content)
			}

			if err := s.DeleteNote(a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetNote(a.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound after delete, got %v", err)
			}

			if _, err := s.AddNote(""); err == nil {
				t.Fatal("want error for empty note content")
			}
		})
	}
}

func TestJSONPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rileighos.json")

	s, err := OpenJSONStore(path)
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.AddTodo("persist me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote("persist me too"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened, err := OpenJSONStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, err := reopened.GetTodo(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "persist me" {
		t.Fatalf("todo did not survive reopen: %+v", got)
	}
	notes, err := reopened.GetNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("want 1 note after reopen, got %d", len(notes))
	}

	// IDs must keep increasing after a reopen, not restart at 1.
	again, err := reopened.AddTodo("second")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == added.ID {
		t.Fatalf("id reused after reopen: %d", again.ID)
	}
}

func TestSQLitePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rileighos.db")

	s, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.AddTodo("persist me")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, err := reopened.GetTodo(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "persist me" {
		t.Fatalf("todo did not survive reopen: %+v", got)
	}
}

func canvasDueAt(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

// TestTodayDueFilter pins the today contract: open todos due today or
// overdue, sorted by due instant (ties by ID); undated, future-dated, and
// done todos are excluded.
func TestTodayDueFilter(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)
			today := Today()
			yesterday, _ := prevDay(today)
			tomorrow := time.Now().Add(30 * time.Hour).Format(dayLayout)

			mk := func(day string) *time.Time {
				if day == "" {
					return nil
				}
				return canvasDueAt(day + "T12:00:00Z")
			}
			if _, err := s.AddTodo("undated"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddCanvasTodo("future", mk(tomorrow), 901, "C", "", "v1"); err != nil {
				t.Fatal(err)
			}
			done, err := s.AddCanvasTodo("done overdue", mk(yesterday), 902, "C", "", "v1")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.MarkTodoDone(done.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddCanvasTodo("overdue b", mk(yesterday), 903, "C", "", "v1"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddCanvasTodo("overdue a", mk(yesterday), 904, "C", "", "v1"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddCanvasTodo("due today", mk(today), 905, "C", "", "v1"); err != nil {
				t.Fatal(err)
			}

			view, err := s.GetToday()
			if err != nil {
				t.Fatal(err)
			}
			if view.Date != today {
				t.Fatalf("want date %s, got %s", today, view.Date)
			}
			var got []string
			for _, td := range view.DueTodos {
				got = append(got, td.Content)
			}
			want := []string{"overdue b", "overdue a", "due today"}
			if len(got) != len(want) {
				t.Fatalf("want %v, got %v", want, got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("want %v, got %v", want, got)
				}
			}
		})
	}
}
