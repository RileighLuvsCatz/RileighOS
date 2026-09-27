package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"rdb/rileighos/internal/api"
	"rdb/rileighos/internal/client"
	"rdb/rileighos/internal/models"
)

const canvasTestSecret = "SECRET-TOKEN-xyz"

// apiTestFake is a minimal scripted Canvas used by the API tests: two
// courses, one submittable + one none-type + one unpublished + one
// already-submitted assignment in ART, one lab in COMP SCI. It lives here
// (rather than reusing the canvas package's fake) because test helpers
// don't cross package boundaries.
type apiTestFake struct {
	token string
}

type apiTestSubmission struct {
	WorkflowState string `json:"workflow_state"`
}

type apiTestAssignment struct {
	ID              int64              `json:"id"`
	Name            string             `json:"name"`
	Published       bool               `json:"published"`
	SubmissionTypes []string           `json:"submission_types"`
	DueAt           *string            `json:"due_at"`
	HTMLURL         string             `json:"html_url"`
	UpdatedAt       string             `json:"updated_at"`
	Submission      *apiTestSubmission `json:"submission"`
}

func strptr(s string) *string { return &s }

func (f *apiTestFake) serve(t *testing.T) string {
	t.Helper()
	due := "2026-10-01T23:59:00-05:00"
	byCourse := map[int64][]apiTestAssignment{
		1: {
			{ID: 101, Name: "Essay", Published: true, SubmissionTypes: []string{"online_text_entry"}, DueAt: strptr(due), HTMLURL: "http://canvas/101", UpdatedAt: "v1", Submission: &apiTestSubmission{WorkflowState: "unsubmitted"}},
			{ID: 102, Name: "Reading", Published: true, SubmissionTypes: []string{"none"}, HTMLURL: "http://canvas/102", UpdatedAt: "v1"},
			{ID: 103, Name: "Draft", Published: false, SubmissionTypes: []string{"online_text_entry"}, DueAt: strptr(due), HTMLURL: "http://canvas/103", UpdatedAt: "v1", Submission: &apiTestSubmission{WorkflowState: "unsubmitted"}},
			{ID: 104, Name: "Old quiz", Published: true, SubmissionTypes: []string{"online_quiz"}, DueAt: strptr(due), HTMLURL: "http://canvas/104", UpdatedAt: "v1", Submission: &apiTestSubmission{WorkflowState: "submitted"}},
			{ID: 105, Name: "Overdue worksheet", Published: true, SubmissionTypes: []string{"online_text_entry"}, DueAt: strptr("2026-09-20T23:59:00-05:00"), HTMLURL: "http://canvas/105", UpdatedAt: "v1", Submission: &apiTestSubmission{WorkflowState: "unsubmitted"}},
		},
		2: {
			{ID: 201, Name: "Lab 3", Published: true, SubmissionTypes: []string{"online_upload"}, DueAt: strptr(due), HTMLURL: "http://canvas/201", UpdatedAt: "v1", Submission: &apiTestSubmission{WorkflowState: "unsubmitted"}},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeAPIJSON(w, []map[string]any{
			{"id": 1, "name": "Drawing I", "course_code": "ART 150", "workflow_state": "available"},
			{"id": 2, "name": "Data Structures", "course_code": "COMP SCI 337", "workflow_state": "available"},
		})
	})
	mux.HandleFunc("/api/v1/courses/", func(w http.ResponseWriter, r *http.Request) {
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
		list := byCourse[id]
		if list == nil {
			list = []apiTestAssignment{}
		}
		writeAPIJSON(w, list)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
}

func writeAPIJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	data, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

func withCanvasEnv(t *testing.T, base string) {
	t.Helper()
	t.Setenv("RILEIGHOS_CANVAS_TOKEN", canvasTestSecret)
	if base == "" {
		t.Setenv("RILEIGHOS_CANVAS_BASE_URL", "http://127.0.0.1:1")
	} else {
		t.Setenv("RILEIGHOS_CANVAS_BASE_URL", base)
	}
}

func assertNoLeak(t *testing.T, data []byte) {
	t.Helper()
	if strings.Contains(string(data), canvasTestSecret) {
		t.Fatalf("response must never echo the token: %s", data)
	}
}

func TestCanvasNotConfigured(t *testing.T) {
	t.Setenv("RILEIGHOS_CANVAS_TOKEN", "")
	base := openTestServer(t, "sqlite")
	for _, tc := range []struct{ method, url, body string }{
		{http.MethodGet, base + "/canvas/courses", ""},
		{http.MethodPost, base + "/canvas/sync", `{}`},
	} {
		status, data := doRaw(t, tc.method, tc.url, tc.body)
		if status != http.StatusNotImplemented {
			t.Fatalf("%s %s: want 501, got %d (%s)", tc.method, tc.url, status, data)
		}
		if se := decodeBody[api.ErrorBody](t, data); !strings.Contains(se.Error, "RILEIGHOS_CANVAS_TOKEN") {
			t.Fatalf("want a helpful missing-token error, got %s", data)
		}
	}
}

func TestCanvasSyncAPI(t *testing.T) {
	fake := &apiTestFake{token: canvasTestSecret}
	withCanvasEnv(t, fake.serve(t))
	base := openTestServer(t, "sqlite")

	// Courses refresh live and arrive pending.
	status, data := doRaw(t, http.MethodGet, base+"/canvas/courses", "")
	if status != http.StatusOK {
		t.Fatalf("GET courses: want 200, got %d (%s)", status, data)
	}
	assertNoLeak(t, data)
	courses := decodeBody[[]models.CanvasCourse](t, data)
	if len(courses) != 2 || !courses[0].PendingConfirm || !courses[1].PendingConfirm {
		t.Fatalf("want 2 pending courses, got %+v", courses)
	}

	// Sync with no decisions imports nothing, reports pending.
	status, data = doRaw(t, http.MethodPost, base+"/canvas/sync", `{}`)
	if status != http.StatusOK {
		t.Fatalf("POST sync: want 200, got %d (%s)", status, data)
	}
	assertNoLeak(t, data)
	if res := decodeBody[models.SyncResult](t, data); res.Imported != 0 || len(res.PendingCourses) != 2 {
		t.Fatalf("want 0 imported 2 pending, got %+v", res)
	}

	// Sync with decisions imports (art todos + note + overdue, comp lab).
	status, data = doRaw(t, http.MethodPost, base+"/canvas/sync", `{"decisions":{"1":false,"2":false}}`)
	if status != http.StatusOK {
		t.Fatalf("POST sync decided: want 200, got %d (%s)", status, data)
	}
	if res := decodeBody[models.SyncResult](t, data); res.Imported != 4 || res.Skipped != 2 {
		t.Fatalf("want 4 imported 2 skipped, got %+v", res)
	}

	// Resolve one course excluded, then back. Excluding detaches its
	// imported items to local (art has 3: essay, overdue, reading note).
	status, data = doRaw(t, http.MethodPatch, base+"/canvas/courses/1", `{"excluded":true}`)
	if status != http.StatusOK {
		t.Fatalf("PATCH course: want 200, got %d (%s)", status, data)
	}
	resolved := decodeBody[models.CourseResolveResult](t, data)
	if !resolved.Course.Excluded || resolved.Course.PendingConfirm {
		t.Fatalf("want excluded confirmed course, got %+v", resolved)
	}
	if resolved.DetachedTodos != 2 || resolved.DetachedNotes != 1 {
		t.Fatalf("want 2+1 detached, got %+v", resolved)
	}
	if status, _ := doRaw(t, http.MethodPatch, base+"/canvas/courses/1", `{"excluded":false}`); status != http.StatusOK {
		t.Fatalf("PATCH include: want 200, got %d", status)
	}
}

func TestCanvasAPIBadRequests(t *testing.T) {
	fake := &apiTestFake{token: canvasTestSecret}
	withCanvasEnv(t, fake.serve(t))
	base := openTestServer(t, "sqlite")
	cases := []struct {
		name       string
		method     string
		url        string
		body       string
		wantStatus int
	}{
		{"sync bad decision id", http.MethodPost, base + "/canvas/sync", `{"decisions":{"abc":false}}`, http.StatusBadRequest},
		{"sync bad json", http.MethodPost, base + "/canvas/sync", `not json`, http.StatusBadRequest},
		{"resolve missing excluded", http.MethodPatch, base + "/canvas/courses/1", `{}`, http.StatusBadRequest},
		{"resolve bad json", http.MethodPatch, base + "/canvas/courses/1", `not json`, http.StatusBadRequest},
		{"resolve non-numeric id", http.MethodPatch, base + "/canvas/courses/abc", `{"excluded":true}`, http.StatusBadRequest},
		{"resolve missing id", http.MethodPatch, base + "/canvas/courses/999", `{"excluded":true}`, http.StatusNotFound},
		{"wrong method on sync", http.MethodGet, base + "/canvas/sync", "", http.StatusMethodNotAllowed},
		{"wrong method on courses", http.MethodPost, base + "/canvas/courses", "", http.StatusMethodNotAllowed},
		{"wrong method on member", http.MethodGet, base + "/canvas/courses/1", "", http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, data := doRaw(t, tc.method, tc.url, tc.body)
			if status != tc.wantStatus {
				t.Fatalf("%s %s: want %d, got %d (%s)", tc.method, tc.url, tc.wantStatus, status, data)
			}
			assertNoLeak(t, data)
			if status >= 400 {
				if se := decodeBody[api.ErrorBody](t, data); strings.TrimSpace(se.Error) == "" {
					t.Fatalf("want non-empty error, got %s", data)
				}
			}
		})
	}
}

func TestCanvasUpstreamDown(t *testing.T) {
	withCanvasEnv(t, "") // token set, base unreachable
	base := openTestServer(t, "sqlite")
	for _, tc := range []struct{ method, url, body string }{
		{http.MethodGet, base + "/canvas/courses", ""},
		{http.MethodPost, base + "/canvas/sync", `{}`},
	} {
		status, data := doRaw(t, tc.method, tc.url, tc.body)
		if status != http.StatusBadGateway {
			t.Fatalf("%s %s: want 502, got %d (%s)", tc.method, tc.url, status, data)
		}
		assertNoLeak(t, data)
	}
}

// TestClientCanvasRoundTrip runs the CLI-facing Canvas contract through the
// HTTP client: courses, resolve, sync with decisions, then verify imports.
func TestClientCanvasRoundTrip(t *testing.T) {
	fake := &apiTestFake{token: canvasTestSecret}
	withCanvasEnv(t, fake.serve(t))
	c := client.NewClient(openTestServer(t, "sqlite"))
	defer c.Close()

	courses, err := c.GetCanvasCourses()
	if err != nil {
		t.Fatal(err)
	}
	if len(courses) != 2 {
		t.Fatalf("want 2 courses, got %+v", courses)
	}

	var artID int64
	for _, course := range courses {
		if course.Code == "ART 150" {
			artID = course.CourseID
		}
	}
	if artID == 0 {
		t.Fatalf("no ART 150 in %+v", courses)
	}
	if _, err := c.ResolveCanvasCourse(artID, true); err != nil {
		t.Fatal(err)
	}
	res, err := c.SyncCanvas(models.SyncModeManual, models.SyncDecisions{artID: true, 2: false})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 { // only the COMP SCI lab; ART excluded
		t.Fatalf("want 1 imported, got %+v", res)
	}
	todos, err := c.GetTodos()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, td := range todos {
		if strings.HasPrefix(td.Content, "[COMP SCI 337]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a [COMP SCI 337] todo, got %+v", todos)
	}
	if _, err := c.ResolveCanvasCourse(999, true); err == nil {
		t.Fatal("want error resolving unknown course over HTTP")
	}
}

func TestCanvasStatus(t *testing.T) {
	// Unconfigured servers answer configured=false (never 501): the
	// status probe must work precisely when Canvas is absent.
	t.Setenv("RILEIGHOS_CANVAS_TOKEN", "")
	base := openTestServer(t, "sqlite")
	status, data := doRaw(t, http.MethodGet, base+"/canvas/status", "")
	if status != http.StatusOK {
		t.Fatalf("GET status: want 200, got %d (%s)", status, data)
	}
	st := decodeBody[models.CanvasStatus](t, data)
	if st.Configured || st.LastSyncAt != nil {
		t.Fatalf("want unconfigured never-synced, got %+v", st)
	}

	// Configured with a sync behind it: last_sync_at present.
	fake := &apiTestFake{token: canvasTestSecret}
	withCanvasEnv(t, fake.serve(t))
	base = openTestServer(t, "sqlite")
	if status, _ := doRaw(t, http.MethodPost, base+"/canvas/sync", `{"decisions":{"1":false,"2":false}}`); status != http.StatusOK {
		t.Fatalf("POST sync: want 200, got %d", status)
	}
	status, data = doRaw(t, http.MethodGet, base+"/canvas/status", "")
	if status != http.StatusOK {
		t.Fatalf("GET status: want 200, got %d (%s)", status, data)
	}
	assertNoLeak(t, data)
	st = decodeBody[models.CanvasStatus](t, data)
	if !st.Configured || st.LastSyncAt == nil {
		t.Fatalf("want configured with last sync, got %+v", st)
	}

	if status, _ := doRaw(t, http.MethodPost, base+"/canvas/status", ""); status != http.StatusMethodNotAllowed {
		t.Fatalf("POST status: want 405, got %d", status)
	}
}

func TestCanvasSyncBadMode(t *testing.T) {
	fake := &apiTestFake{token: canvasTestSecret}
	withCanvasEnv(t, fake.serve(t))
	base := openTestServer(t, "sqlite")
	status, data := doRaw(t, http.MethodPost, base+"/canvas/sync", `{"mode":"sometimes"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("POST sync bad mode: want 400, got %d (%s)", status, data)
	}
	assertNoLeak(t, data)
}
