package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rdb/rileighos/internal/models"
	"rdb/rileighos/internal/store"
)

// fakeCanvas serves scripted Canvas API responses for sync tests: courses,
// per-course assignments, and an auth check proving the token travels in
// the Authorization header. State mutators below simulate professor edits
// and student submissions between syncs.
type fakeCanvas struct {
	mu          sync.Mutex
	token       string
	courses     []canvasCourseJSON
	assignments map[int64][]canvasAssignmentJSON
}

func newFakeCanvas(token string) *fakeCanvas {
	due := func(s string) *canvasTime {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			panic(err)
		}
		return &canvasTime{Time: t}
	}
	sub := func(state string) *canvasSubmissionJSON {
		if state == "" {
			return nil
		}
		return &canvasSubmissionJSON{WorkflowState: state}
	}
	return &fakeCanvas{
		token: token,
		courses: []canvasCourseJSON{
			{ID: 1, Name: "Drawing I", CourseCode: "ART 150", WorkflowState: "available"},
			{ID: 2, Name: "Data Structures", CourseCode: "COMP SCI 337", WorkflowState: "available"},
		},
		assignments: map[int64][]canvasAssignmentJSON{
			1: {
				{ID: 101, Name: "Essay", Published: true, SubmissionTypes: []string{"online_text_entry"}, DueAt: due("2026-10-01T23:59:00-05:00"), HTMLURL: "http://canvas/101", UpdatedAt: "v1", Submission: sub("unsubmitted")},
				{ID: 102, Name: "Reading", Published: true, SubmissionTypes: []string{"none"}, DueAt: nil, HTMLURL: "http://canvas/102", UpdatedAt: "v1", Submission: nil},
				{ID: 103, Name: "Draft", Published: false, SubmissionTypes: []string{"online_text_entry"}, DueAt: due("2026-10-02T23:59:00-05:00"), HTMLURL: "http://canvas/103", UpdatedAt: "v1", Submission: sub("unsubmitted")},
				{ID: 104, Name: "Old quiz", Published: true, SubmissionTypes: []string{"online_quiz"}, DueAt: due("2026-08-01T23:59:00-05:00"), HTMLURL: "http://canvas/104", UpdatedAt: "v1", Submission: sub("submitted")},
				{ID: 105, Name: "Week 1. Read & Watch", Published: true, SubmissionTypes: []string{"not_graded"}, DueAt: nil, HTMLURL: "http://canvas/105", UpdatedAt: "v1", Submission: nil},
			},
			2: {
				{ID: 201, Name: "Lab 3", Published: true, SubmissionTypes: []string{"online_upload"}, DueAt: due("2026-10-03T23:59:00-05:00"), HTMLURL: "http://canvas/201", UpdatedAt: "v1", Submission: sub("unsubmitted")},
				{ID: 202, Name: "Syllabus", Published: true, SubmissionTypes: []string{"online_text_entry"}, GradingType: "not_graded", DueAt: nil, HTMLURL: "http://canvas/202", UpdatedAt: "v1", Submission: nil},
			},
		},
	}
}

func (f *fakeCanvas) serve(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeFakeJSON(w, f.courses)
	})
	mux.HandleFunc("/api/v1/courses/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/courses/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[1] != "assignments" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		list := f.assignments[id]
		if list == nil {
			list = []canvasAssignmentJSON{}
		}
		writeFakeJSON(w, list)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
}

func writeFakeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	data, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

// setSubmission flips one assignment's submission state between syncs.
func (f *fakeCanvas) setSubmission(courseID, assignID int64, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.assignments[courseID] {
		if a.ID == assignID {
			if state == "" {
				f.assignments[courseID][i].Submission = nil
			} else {
				f.assignments[courseID][i].Submission = &canvasSubmissionJSON{WorkflowState: state}
			}
			return
		}
	}
	panic(fmt.Sprintf("no assignment %d in course %d", assignID, courseID))
}

// setAssignment renames/retimes one assignment between syncs.
func (f *fakeCanvas) setAssignment(courseID, assignID int64, name, due, updated string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.assignments[courseID] {
		if a.ID == assignID {
			f.assignments[courseID][i].Name = name
			if due == "" {
				f.assignments[courseID][i].DueAt = nil
			} else {
				t, err := time.Parse(time.RFC3339, due)
				if err != nil {
					panic(err)
				}
				f.assignments[courseID][i].DueAt = &canvasTime{Time: t}
			}
			f.assignments[courseID][i].UpdatedAt = updated
			return
		}
	}
	panic(fmt.Sprintf("no assignment %d in course %d", assignID, courseID))
}

// setTypes flips submission_types between syncs (professor edits).
func (f *fakeCanvas) setTypes(courseID, assignID int64, types []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.assignments[courseID] {
		if a.ID == assignID {
			f.assignments[courseID][i].SubmissionTypes = types
			return
		}
	}
	panic(fmt.Sprintf("no assignment %d in course %d", assignID, courseID))
}

// setGradingType flips grading_type between syncs (professor edits).
func (f *fakeCanvas) setGradingType(courseID, assignID int64, gradingType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.assignments[courseID] {
		if a.ID == assignID {
			f.assignments[courseID][i].GradingType = gradingType
			return
		}
	}
	panic(fmt.Sprintf("no assignment %d in course %d", assignID, courseID))
}

// openCanvasTestStores mirrors the store package's conformance setup: every
// sync test below runs against both backends through store.FullStore.
func openCanvasTestStores(t *testing.T) map[string]func(t *testing.T) store.FullStore {
	t.Helper()
	return map[string]func(t *testing.T) store.FullStore{
		"json": func(t *testing.T) store.FullStore {
			t.Helper()
			s, err := store.OpenJSONStore(filepath.Join(t.TempDir(), "rileighos.json"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		},
		"sqlite": func(t *testing.T) store.FullStore {
			t.Helper()
			s, err := store.OpenSQLiteStore(filepath.Join(t.TempDir(), "rileighos.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		},
	}
}

func setupSyncTest(t *testing.T, backend string, open func(t *testing.T) store.FullStore) (store.FullStore, *CanvasClient, *fakeCanvas) {
	t.Helper()
	fake := newFakeCanvas("test-token")
	base := fake.serve(t)
	return open(t), NewCanvasClient(base, "test-token"), fake
}

func includeAll() models.SyncDecisions { return models.SyncDecisions{1: false, 2: false} }

func TestCanvasSyncImport(t *testing.T) {
	for backend, open := range openCanvasTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s, cv, _ := setupSyncTest(t, backend, open)
			res, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll())
			if err != nil {
				t.Fatal(err)
			}
			// Essay + Lab todos, Reading + Week 1 + Syllabus notes; Draft
			// unpublished and Old quiz already-submitted are skipped.
			if res.Imported != 5 || res.Skipped != 2 || len(res.PendingCourses) != 0 {
				t.Fatalf("unexpected result: %+v", res)
			}
			essay, err := s.GetTodoByCanvasID(101)
			if err != nil {
				t.Fatal(err)
			}
			if essay.Content != "[ART 150] Essay" || essay.Origin != "canvas" || essay.CanvasCourseCode != "ART 150" {
				t.Fatalf("bad mapping: %+v", essay)
			}
			if essay.DueAt == nil || essay.DueAt.Format("-07:00") != "-05:00" {
				t.Fatalf("due zone not preserved: %+v", essay.DueAt)
			}
			if _, err := s.GetNoteByCanvasID(102); err != nil {
				t.Fatalf("none-type must import as note: %v", err)
			}
			if note, err := s.GetNoteByCanvasID(105); err != nil {
				t.Fatalf("not_graded submission_types must import as note: %v", err)
			} else if note.Content != "[ART 150] Week 1. Read & Watch" || note.CanvasCourseCode != "ART 150" {
				t.Fatalf("bad not_graded mapping: %+v", note)
			}
			if note, err := s.GetNoteByCanvasID(202); err != nil {
				t.Fatalf("not_graded grading_type must import as note: %v", err)
			} else if note.Content != "[COMP SCI 337] Syllabus" || note.CanvasCourseCode != "COMP SCI 337" {
				t.Fatalf("bad grading_type mapping: %+v", note)
			}
			if _, err := s.GetTodoByCanvasID(104); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("already-submitted new items must skip, got %v", err)
			}
			if last, err := s.GetLastCanvasSync(); err != nil || last.IsZero() {
				t.Fatalf("last sync must be stamped, got %v, err %v", last, err)
			}
		})
	}
}

func TestCanvasSyncIdempotent(t *testing.T) {
	for backend, open := range openCanvasTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s, cv, _ := setupSyncTest(t, backend, open)
			if _, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll()); err != nil {
				t.Fatal(err)
			}
			res, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll())
			if err != nil {
				t.Fatal(err)
			}
			if res.Imported != 0 || res.Updated != 0 || res.Completed != 0 || res.Reopened != 0 || res.Skipped != 2 {
				t.Fatalf("re-sync must be a no-op besides known skips: %+v", res)
			}
		})
	}
}

func TestCanvasSyncStatusReflection(t *testing.T) {
	for backend, open := range openCanvasTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s, cv, fake := setupSyncTest(t, backend, open)
			if _, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll()); err != nil {
				t.Fatal(err)
			}
			fake.setSubmission(1, 101, "submitted")
			res, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll())
			if err != nil {
				t.Fatal(err)
			}
			if res.Completed != 1 {
				t.Fatalf("want 1 completed, got %+v", res)
			}
			if got, _ := s.GetTodoByCanvasID(101); !got.Done {
				t.Fatalf("submitted must mark done: %+v", got)
			}
			// Graded counts as submitted too.
			fake.setSubmission(1, 101, "graded")
			if res, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll()); err != nil || res.Completed != 0 {
				t.Fatalf("graded stays done quietly: %+v, err %v", res, err)
			}
			// Mirror: unsubmit reopens.
			fake.setSubmission(1, 101, "unsubmitted")
			res, err = RunCanvasSync(s, cv, models.SyncModeManual, includeAll())
			if err != nil {
				t.Fatal(err)
			}
			if res.Reopened != 1 {
				t.Fatalf("want 1 reopened, got %+v", res)
			}
			if got, _ := s.GetTodoByCanvasID(101); got.Done {
				t.Fatalf("unsubmit must reopen: %+v", got)
			}
		})
	}
}

func TestCanvasSyncUpdate(t *testing.T) {
	for backend, open := range openCanvasTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s, cv, fake := setupSyncTest(t, backend, open)
			if _, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll()); err != nil {
				t.Fatal(err)
			}
			fake.setAssignment(1, 101, "Essay (revised)", "2026-10-08T23:59:00-05:00", "v2")
			res, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll())
			if err != nil {
				t.Fatal(err)
			}
			if res.Updated != 1 {
				t.Fatalf("want 1 updated, got %+v", res)
			}
			got, _ := s.GetTodoByCanvasID(101)
			if got.Content != "[ART 150] Essay (revised)" || got.CanvasUpdatedAt != "v2" {
				t.Fatalf("update not applied: %+v", got)
			}
		})
	}
}

func TestCanvasSyncGating(t *testing.T) {
	for backend, open := range openCanvasTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s, cv, _ := setupSyncTest(t, backend, open)

			// No decisions: nothing imports, both courses pending.
			res, err := RunCanvasSync(s, cv, models.SyncModeManual, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Imported != 0 || len(res.PendingCourses) != 2 {
				t.Fatalf("want 2 pending, got %+v", res)
			}
			if todos, _ := s.GetTodos(); len(todos) != 0 {
				t.Fatalf("pending courses must not import: %+v", todos)
			}

			// Exclude ART and include COMP: only COMP SCI imports. Absent
			// entries stay pending, so COMP needs an explicit decision.
			res, err = RunCanvasSync(s, cv, models.SyncModeManual, models.SyncDecisions{1: true, 2: false})
			if err != nil {
				t.Fatal(err)
			}
			if res.Imported != 2 || len(res.PendingCourses) != 0 {
				t.Fatalf("want only COMP imported: %+v", res)
			}
			if _, err := s.GetTodoByCanvasID(101); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("excluded course must not import: %v", err)
			}

			// Decisions persist: later syncs never re-ask excluded courses.
			res, err = RunCanvasSync(s, cv, models.SyncModeManual, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range res.PendingCourses {
				if c.CourseID == 1 {
					t.Fatalf("excluded course must not return to pending: %+v", res)
				}
			}

			// Include ART later: its items import then.
			res, err = RunCanvasSync(s, cv, models.SyncModeManual, models.SyncDecisions{1: false})
			if err != nil {
				t.Fatal(err)
			}
			if res.Imported != 3 { // Essay todo + Reading + Week 1 notes (Draft unpublished, Old quiz submitted)
				t.Fatalf("want 2 imported on include, got %+v", res)
			}
		})
	}
}

func TestCanvasSyncTypeMigration(t *testing.T) {
	for backend, open := range openCanvasTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s, cv, fake := setupSyncTest(t, backend, open)
			if _, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll()); err != nil {
				t.Fatal(err)
			}
			fake.setTypes(1, 102, []string{"online_text_entry"})
			if _, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetNoteByCanvasID(102); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("migrated note must be gone: %v", err)
			}
			if _, err := s.GetTodoByCanvasID(102); err != nil {
				t.Fatalf("migrated todo must exist: %v", err)
			}
			// Flipping a todo to not_graded migrates it back to a note,
			// preserving content and course code.
			fake.setTypes(1, 101, []string{"not_graded"})
			fake.setGradingType(1, 101, "not_graded")
			if _, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetTodoByCanvasID(101); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("migrated todo must be gone: %v", err)
			}
			note, err := s.GetNoteByCanvasID(101)
			if err != nil {
				t.Fatalf("migrated note must exist: %v", err)
			}
			if note.Content != "[ART 150] Essay" || note.CanvasCourseCode != "ART 150" {
				t.Fatalf("migration must preserve content/course: %+v", note)
			}
		})
	}
}

func TestMapAssignment(t *testing.T) {
	course := CanvasCourseInfo{ID: 1, Code: "ART 150", Name: "Drawing I"}
	cases := []struct {
		name        string
		types       []string
		grading     string
		state       string
		submittable bool
		submitted   bool
	}{
		{"online work unsubmitted", []string{"online_text_entry"}, "", "unsubmitted", true, false},
		{"no submission type", []string{"none"}, "", "unsubmitted", false, false},
		{"empty types", nil, "", "unsubmitted", false, false},
		{"not_graded submission type", []string{"not_graded"}, "", "unsubmitted", false, false},
		{"not_graded grading type", []string{"online_text_entry"}, "not_graded", "unsubmitted", false, false},
		{"not_graded grading type empty types", nil, "not_graded", "unsubmitted", false, false},
		{"submitted", []string{"online_quiz"}, "", "submitted", true, true},
		{"graded", []string{"online_quiz"}, "", "graded", true, true},
		{"pending review", []string{"online_upload"}, "", "pending_review", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := CanvasAssignmentInfo{ID: 1, Name: "X", SubmissionTypes: tc.types, GradingType: tc.grading, Submitted: submittedState(tc.state)}
			item := MapAssignment(course, a)
			if item.Submittable != tc.submittable || item.Submitted != tc.submitted {
				t.Fatalf("got submittable=%v submitted=%v, want %v %v",
					item.Submittable, item.Submitted, tc.submittable, tc.submitted)
			}
			if item.Title != "[ART 150] X" || item.CourseCode != "ART 150" {
				t.Fatalf("bad title/course: %+v", item)
			}
		})
	}
}

func TestCanvasPagination(t *testing.T) {
	var ts *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", "<"+ts.URL+"/api/v1/courses?page=2>; rel=\"next\"")
			writeFakeJSON(w, []canvasCourseJSON{{ID: 1, Name: "A", CourseCode: "A 1"}})
			return
		}
		writeFakeJSON(w, []canvasCourseJSON{{ID: 2, Name: "B", CourseCode: "B 2"}})
	})
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cv := NewCanvasClient(ts.URL, "test-token")
	courses, err := cv.GetCourses()
	if err != nil {
		t.Fatal(err)
	}
	if len(courses) != 2 || courses[0].Code != "A 1" || courses[1].Code != "B 2" {
		t.Fatalf("pagination must collect all pages: %+v", courses)
	}
}

func TestCanvasAuthAndTokenHygiene(t *testing.T) {
	const secret = "SECRET-TOKEN-xyz"
	fake := newFakeCanvas(secret)
	base := fake.serve(t)

	// Wrong token maps to a helpful 401-shaped error, never a leak.
	cv := NewCanvasClient(base, "wrong")
	if _, err := cv.GetCourses(); err == nil || !errors.Is(err, ErrCanvasUpstream) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want upstream 401, got %v", err)
	}

	// Right token works and error strings never echo it.
	cv = NewCanvasClient(base, secret)
	if _, err := cv.GetCourses(); err != nil {
		t.Fatal(err)
	}
	bad := NewCanvasClient("http://127.0.0.1:1", secret)
	if _, err := bad.GetCourses(); err == nil || !errors.Is(err, ErrCanvasUpstream) {
		t.Fatalf("want upstream error, got %v", err)
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("error must never echo the token: %v", err)
	}
}

func TestCanvasSyncAuto(t *testing.T) {
	for backend, open := range openCanvasTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s, cv, _ := setupSyncTest(t, backend, open)

			// Auto mode imports unconfirmed courses but keeps them
			// pending, so the next manual sync still asks.
			res, err := RunCanvasSync(s, cv, models.SyncModeAuto, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Imported != 5 || len(res.PendingCourses) != 2 {
				t.Fatalf("want 5 imported 2 pending, got %+v", res)
			}
			if _, err := s.GetTodoByCanvasID(101); err != nil {
				t.Fatalf("auto must import: %v", err)
			}

			// Manual with no decisions still skips pending (nothing new).
			res, err = RunCanvasSync(s, cv, models.SyncModeManual, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Imported != 0 || len(res.PendingCourses) != 2 {
				t.Fatalf("manual must not import pending: %+v", res)
			}
		})
	}
}

func TestCanvasSyncDetachOnExclude(t *testing.T) {
	for backend, open := range openCanvasTestStores(t) {
		t.Run(backend, func(t *testing.T) {
			s, cv, _ := setupSyncTest(t, backend, open)
			if _, err := RunCanvasSync(s, cv, models.SyncModeManual, includeAll()); err != nil {
				t.Fatal(err)
			}
			// Mark the essay done first: detachment must preserve it.
			essay, err := s.GetTodoByCanvasID(101)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.MarkTodoDone(essay.ID); err != nil {
				t.Fatal(err)
			}

			res, err := RunCanvasSync(s, cv, models.SyncModeManual, models.SyncDecisions{1: true})
			if err != nil {
				t.Fatal(err)
			}
			if res.DetachedTodos != 1 || res.DetachedNotes != 2 {
				t.Fatalf("want 1 todo + 2 notes detached, got %+v", res)
			}
			todos, err := s.GetTodos()
			if err != nil {
				t.Fatal(err)
			}
			kept := 0
			for _, td := range todos {
				// ART items detach; COMP SCI stays linked.
				if td.CanvasCourseCode == "ART 150" {
					if td.CanvasAssignID != nil {
						t.Fatalf("detached todo still linked: %+v", td)
					}
					if td.Content == "[ART 150] Essay" {
						kept++
						if !td.Done {
							t.Fatalf("detachment must preserve done: %+v", td)
						}
					}
				} else if td.CanvasAssignID == nil {
					t.Fatalf("non-excluded todo must stay linked: %+v", td)
				}
			}
			if kept != 1 {
				t.Fatalf("detached content must survive, todos: %+v", todos)
			}
			if _, err := s.GetTodoByCanvasID(101); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("detached lookup must miss: %v", err)
			}
		})
	}
}

func TestSyncStale(t *testing.T) {
	now := time.Now()
	if !SyncStale(time.Time{}, now, time.Minute) {
		t.Fatal("never synced must be stale")
	}
	if !SyncStale(now.Add(-time.Hour), now, 15*time.Minute) {
		t.Fatal("old sync must be stale")
	}
	if SyncStale(now.Add(-time.Minute), now, 15*time.Minute) {
		t.Fatal("fresh sync must not be stale")
	}
}

func TestParseInterval(t *testing.T) {
	d, enabled, err := ParseInterval("", 30*time.Minute)
	if err != nil || !enabled || d != 30*time.Minute {
		t.Fatalf("empty must mean default: %v %v %v", d, enabled, err)
	}
	d, enabled, err = ParseInterval("10m", 30*time.Minute)
	if err != nil || !enabled || d != 10*time.Minute {
		t.Fatalf("explicit must parse: %v %v %v", d, enabled, err)
	}
	if _, enabled, err := ParseInterval("0", 30*time.Minute); err != nil || enabled {
		t.Fatalf("zero must disable: %v %v", enabled, err)
	}
	if _, _, err := ParseInterval("soon", 30*time.Minute); err == nil {
		t.Fatal("garbage must error")
	}
}

func TestSuggestWorkSessionsStub(t *testing.T) {
	if got := SuggestWorkSessions(nil, time.Now(), 7); got != nil {
		t.Fatalf("stub must return nil, got %+v", got)
	}
}

func TestFormatInterval(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Minute: "30m",
		time.Hour:        "1h",
		90 * time.Second: "1m30s",
	}
	for d, want := range cases {
		if got := FormatInterval(d); got != want {
			t.Fatalf("FormatInterval(%v) = %q, want %q", d, got, want)
		}
	}
}
