package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rdb/rileighos/internal/models"
)

func workTestDue(day string) *time.Time {
	t, _ := time.ParseInLocation(dayLayout, day, time.Local)
	due := t.Add(12 * time.Hour)
	return &due
}

func TestSQLiteWorkDateMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE todos (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		content TEXT NOT NULL,
		done INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		due_at TEXT,
		origin TEXT NOT NULL DEFAULT '',
		canvas_assignment_id INTEGER,
		canvas_course_code TEXT NOT NULL DEFAULT '',
		canvas_url TEXT NOT NULL DEFAULT '',
		canvas_updated_at TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO todos (content, created_at) VALUES ('existing task', ?)`, time.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetTodo(1)
	if err != nil || got.WorkDate != nil {
		t.Fatalf("existing todo did not survive migration: %+v, %v", got, err)
	}
	if err := s.SetTodoWorkDate(1, "today"); err != nil {
		t.Fatal(err)
	}
}

func nextWorkTestDay(day string) string {
	t, _ := time.Parse(dayLayout, day)
	return t.AddDate(0, 0, 1).Format(dayLayout)
}

func TestWorkDateTodaySections(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)
			today := Today()
			yesterday, _ := prevDay(today)
			tomorrow := nextWorkTestDay(today)
			add := func(content string, dueAt *time.Time, assignmentID int64, workDate string) models.Todo {
				t.Helper()
				var item models.Todo
				var err error
				if dueAt == nil {
					item, err = s.AddTodo(content)
				} else {
					item, err = s.AddCanvasTodo(content, dueAt, assignmentID, "C", "", "v1")
				}
				if err != nil {
					t.Fatal(err)
				}
				if workDate != "" {
					if err := s.SetTodoWorkDate(item.ID, workDate); err != nil {
						t.Fatal(err)
					}
				}
				return item
			}
			workOverdue := add("missed plan", nil, 0, yesterday)
			dueOverdue := add("missed deadline", workTestDue(yesterday), 101, tomorrow)
			bothOverdue := add("both missed", workTestDue(yesterday), 102, yesterday)
			planned := add("work today", workTestDue(tomorrow), 103, today)
			dueToday := add("due and planned today", workTestDue(today), 104, today)
			done := add("completed plan", nil, 0, today)
			if err := s.MarkTodoDone(done.ID); err != nil {
				t.Fatal(err)
			}
			view, err := s.GetToday()
			if err != nil {
				t.Fatal(err)
			}
			if len(view.OverdueTodos) != 3 || len(view.PlannedTodos) != 1 || len(view.DueTodos) != 1 {
				t.Fatalf("unexpected today sections: %+v", view)
			}
			if view.PlannedTodos[0].ID != planned.ID || view.DueTodos[0].ID != dueToday.ID {
				t.Fatalf("wrong planned/due membership: %+v", view)
			}
			byID := map[int]models.TodayTodo{}
			for _, item := range view.OverdueTodos {
				byID[item.ID] = item
			}
			if !byID[workOverdue.ID].OverdueWorkDate || byID[workOverdue.ID].OverdueDueDate ||
				!byID[dueOverdue.ID].OverdueDueDate || byID[dueOverdue.ID].OverdueWorkDate ||
				!byID[bothOverdue.ID].OverdueDueDate || !byID[bothOverdue.ID].OverdueWorkDate {
				t.Fatalf("incorrect overdue reasons: %+v", byID)
			}
			if err := s.SetTodoWorkDate(workOverdue.ID, tomorrow); err != nil {
				t.Fatal(err)
			}
			view, _ = s.GetToday()
			if len(view.OverdueTodos) != 2 {
				t.Fatalf("replanned item should leave overdue: %+v", view.OverdueTodos)
			}
			if err := s.SetTodoWorkDate(workOverdue.ID, "today"); err != nil {
				t.Fatal(err)
			}
			view, _ = s.GetToday()
			if len(view.PlannedTodos) != 2 {
				t.Fatalf("replanned item should appear today: %+v", view.PlannedTodos)
			}
			if err := s.ClearTodoWorkDate(workOverdue.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.SetTodoWorkDate(done.ID, tomorrow); err != nil {
				t.Fatalf("completed todos remain editable: %v", err)
			}
			if got, _ := s.GetTodo(done.ID); got.WorkDate == nil || *got.WorkDate != tomorrow {
				t.Fatalf("completed work date not updated: %+v", got)
			}
		})
	}
}

func TestWorkDateValidationAndPersistence(t *testing.T) {
	for _, backend := range []string{"json", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "work-date."+backend)
			open := func() FullStore {
				t.Helper()
				if backend == "json" {
					s, err := OpenJSONStore(path)
					if err != nil {
						t.Fatal(err)
					}
					return s
				}
				s, err := OpenSQLiteStore(path)
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			s := open()
			item, err := s.AddTodo("write report")
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{"", "2026-02-30", "tomorrow", "2026-9-28"} {
				if err := s.SetTodoWorkDate(item.ID, bad); err == nil {
					t.Fatalf("accepted invalid work date %q", bad)
				}
			}
			if err := s.SetTodoWorkDate(999, Today()); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want not found, got %v", err)
			}
			if err := s.SetTodoWorkDate(item.ID, Today()); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = open()
			got, err := s.GetTodo(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.WorkDate == nil || *got.WorkDate != Today() {
				t.Fatalf("work date lost across reopen: %+v", got)
			}
			if err := s.ClearTodoWorkDate(item.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = open()
			defer s.Close()
			got, err = s.GetTodo(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.WorkDate != nil {
				t.Fatalf("cleared work date returned after reopen: %+v", got)
			}
		})
	}
}
