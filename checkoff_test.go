package main

import (
	"errors"
	"testing"
)

func TestCurrentStreak(t *testing.T) {
	const today = "2026-09-14"
	cases := []struct {
		name  string
		days  []string
		today string
		want  int
	}{
		{"none checked", nil, today, 0},
		{"only today", []string{"2026-09-14"}, today, 1},
		{"today and yesterday", []string{"2026-09-13", "2026-09-14"}, today, 2},
		{"alive through today unchecked", []string{"2026-09-12", "2026-09-13"}, today, 2},
		{"gap yesterday breaks", []string{"2026-09-12", "2026-09-14"}, today, 1},
		{"gap today and yesterday breaks", []string{"2026-09-12"}, today, 0},
		{"long run", []string{"2026-09-08", "2026-09-09", "2026-09-10", "2026-09-11", "2026-09-12", "2026-09-13", "2026-09-14"}, today, 7},
		{"month boundary", []string{"2026-08-31", "2026-09-01"}, "2026-09-01", 2},
		{"year boundary", []string{"2025-12-31", "2026-01-01"}, "2026-01-01", 2},
		{"leap day", []string{"2024-02-28", "2024-02-29", "2024-03-01"}, "2024-03-01", 3},
		{"unsorted input", []string{"2026-09-14", "2026-09-12", "2026-09-13"}, today, 3},
		{"duplicates do not inflate", []string{"2026-09-13", "2026-09-13", "2026-09-14", "2026-09-14"}, today, 2},
		{"future days ignored", []string{"2026-09-14", "2026-09-15", "2026-09-16"}, today, 1},
		{"only future days", []string{"2026-09-15"}, today, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CurrentStreak(tc.days, tc.today); got != tc.want {
				t.Fatalf("CurrentStreak(%v, %s) = %d, want %d", tc.days, tc.today, got, tc.want)
			}
		})
	}
}

func TestValidDay(t *testing.T) {
	valid := []string{"2026-09-14", "2024-02-29", "2000-01-01"}
	for _, d := range valid {
		if !ValidDay(d) {
			t.Fatalf("ValidDay(%q) = false, want true", d)
		}
	}
	invalid := []string{"", "2026-9-4", "2026-13-01", "2026-02-30", "2023-02-29", "not-a-day", "2026-09-14T00:00:00Z", " 2026-09-14"}
	for _, d := range invalid {
		if ValidDay(d) {
			t.Fatalf("ValidDay(%q) = true, want false", d)
		}
	}
}

func TestCheckoffCRUD(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)

			a, err := s.AddCheckoff("exercise")
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.AddCheckoff("read")
			if err != nil {
				t.Fatal(err)
			}
			if a.ID == b.ID {
				t.Fatalf("ids must be unique, both were %d", a.ID)
			}
			if _, err := s.AddCheckoff("   "); err == nil {
				t.Fatal("want error for blank checkoff name")
			}

			checkoffs, err := s.GetCheckoffs()
			if err != nil {
				t.Fatal(err)
			}
			if len(checkoffs) != 2 {
				t.Fatalf("want 2 checkoffs, got %+v", checkoffs)
			}
			if got, err := s.GetCheckoff(a.ID); err != nil || got.Name != "exercise" {
				t.Fatalf("want checkoff %+v, got %+v, err %v", a, got, err)
			}

			// Check two days, then check one twice: idempotent, no dupes.
			for _, day := range []string{"2026-09-12", "2026-09-13", "2026-09-13"} {
				if err := s.CheckDay(a.ID, day); err != nil {
					t.Fatal(err)
				}
			}
			days, err := s.GetCheckoffDays(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(days) != 2 || days[0] != "2026-09-12" || days[1] != "2026-09-13" {
				t.Fatalf("want sorted unique days, got %v", days)
			}

			// Unchecking a checked day removes it; unchecking again or a
			// never-checked day is a silent no-op.
			if err := s.UncheckDay(a.ID, "2026-09-12"); err != nil {
				t.Fatal(err)
			}
			if err := s.UncheckDay(a.ID, "2026-09-12"); err != nil {
				t.Fatal(err)
			}
			if err := s.UncheckDay(a.ID, "2026-09-10"); err != nil {
				t.Fatal(err)
			}
			if days, err := s.GetCheckoffDays(a.ID); err != nil || len(days) != 1 || days[0] != "2026-09-13" {
				t.Fatalf("want [2026-09-13], got %v, err %v", days, err)
			}

			// Bad days fail on every mutation path.
			for _, day := range []string{"2026-13-01", "yesterday"} {
				if err := s.CheckDay(a.ID, day); err == nil {
					t.Fatalf("want error checking bad day %q", day)
				}
				if err := s.UncheckDay(a.ID, day); err == nil {
					t.Fatalf("want error unchecking bad day %q", day)
				}
			}

			// Deleting a habit takes its days with it.
			if err := s.DeleteCheckoff(a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetCheckoff(a.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
			if _, err := s.GetCheckoffDays(a.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound for days of deleted checkoff, got %v", err)
			}
			if checkoffs, err := s.GetCheckoffs(); err != nil || len(checkoffs) != 1 {
				t.Fatalf("want 1 checkoff left, got %+v, err %v", checkoffs, err)
			}
		})
	}
}

func TestCheckoffNotFound(t *testing.T) {
	cases := []struct {
		name string
		op   func(s Store) error
	}{
		{"get", func(s Store) error { _, err := s.GetCheckoff(999); return err }},
		{"days", func(s Store) error { _, err := s.GetCheckoffDays(999); return err }},
		{"check", func(s Store) error { return s.CheckDay(999, "2026-09-14") }},
		{"uncheck", func(s Store) error { return s.UncheckDay(999, "2026-09-14") }},
		{"delete", func(s Store) error { return s.DeleteCheckoff(999) }},
	}
	for backend, open := range openTestStores(t) {
		for _, tc := range cases {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				if err := tc.op(open(t)); !errors.Is(err, ErrNotFound) {
					t.Fatalf("want ErrNotFound, got %v", err)
				}
			})
		}
	}
}

func TestGetToday(t *testing.T) {
	for backend, open := range openTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s := open(t)

			done, err := s.AddTodo("already done")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.MarkTodoDone(done.ID); err != nil {
				t.Fatal(err)
			}
			openTodo, err := s.AddTodo("still open")
			if err != nil {
				t.Fatal(err)
			}
			habit, err := s.AddCheckoff("exercise")
			if err != nil {
				t.Fatal(err)
			}
			// checked yesterday: streak alive, today unchecked.
			yesterday, ok := prevDay(Today())
			if !ok {
				t.Fatal("cannot step back from today")
			}
			if err := s.CheckDay(habit.ID, yesterday); err != nil {
				t.Fatal(err)
			}

			view, err := s.GetToday()
			if err != nil {
				t.Fatal(err)
			}
			if view.Date != Today() {
				t.Fatalf("want date %s, got %s", Today(), view.Date)
			}
			if len(view.OpenTodos) != 1 || view.OpenTodos[0].ID != openTodo.ID {
				t.Fatalf("want only open todo %+v, got %+v", openTodo, view.OpenTodos)
			}
			if len(view.Checkoffs) != 1 {
				t.Fatalf("want 1 checkoff, got %+v", view.Checkoffs)
			}
			got := view.Checkoffs[0]
			if got.Streak != 1 || got.CheckedToday {
				t.Fatalf("want streak 1 unchecked today, got %+v", got)
			}
			if len(got.Days) != 1 || got.Days[0] != yesterday {
				t.Fatalf("want days [%s], got %v", yesterday, got.Days)
			}
		})
	}
}
