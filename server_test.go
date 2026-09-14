package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// testBackends mirrors openTestStores in store_test.go: every API test
// below runs against both storage backends through the HTTP layer.
var testBackends = []string{"json", "sqlite"}

// openTestServer wraps a fresh backend of the given kind in a real
// (httptest) HTTP server and returns its URL.
func openTestServer(t *testing.T, backend string) string {
	t.Helper()
	var (
		s   Store
		err error
	)
	switch backend {
	case "json":
		s, err = OpenJSONStore(filepath.Join(t.TempDir(), "rileighos.json"))
	case "sqlite":
		s, err = OpenSQLiteStore(filepath.Join(t.TempDir(), "rileighos.db"))
	default:
		t.Fatalf("unknown backend %q", backend)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewServer(NewServer(s).Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

// doRaw issues one raw HTTP request and returns status + body bytes.
func doRaw(t *testing.T, method, url, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusNoContent && !strings.Contains(ct, "application/json") {
		t.Fatalf("%s %s: want JSON content type, got %q", method, url, ct)
	}
	return resp.StatusCode, data
}

func decodeBody[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %s: %v", strings.TrimSpace(string(data)), err)
	}
	return v
}

func TestHealth(t *testing.T) {
	for _, backend := range testBackends {
		t.Run(backend, func(t *testing.T) {
			status, data := doRaw(t, http.MethodGet, openTestServer(t, backend)+"/healthz", "")
			if status != http.StatusOK {
				t.Fatalf("want 200, got %d (%s)", status, data)
			}
			got := decodeBody[map[string]bool](t, data)
			if !got["ok"] {
				t.Fatalf("want {\"ok\": true}, got %s", data)
			}
		})
	}
}

func TestTodoAPI(t *testing.T) {
	for _, backend := range testBackends {
		t.Run(backend, func(t *testing.T) {
			base := openTestServer(t, backend)

			// Create.
			status, data := doRaw(t, http.MethodPost, base+"/todos", `{"content":"buy milk"}`)
			if status != http.StatusCreated {
				t.Fatalf("POST: want 201, got %d (%s)", status, data)
			}
			created := decodeBody[Todo](t, data)
			if created.ID <= 0 || created.Content != "buy milk" || created.Done {
				t.Fatalf("unexpected created todo: %+v", created)
			}

			// Fetch one.
			status, data = doRaw(t, http.MethodGet, fmt.Sprintf("%s/todos/%d", base, created.ID), "")
			if status != http.StatusOK {
				t.Fatalf("GET one: want 200, got %d (%s)", status, data)
			}
			if got := decodeBody[Todo](t, data); got != created {
				t.Fatalf("GET one: want %+v, got %+v", created, got)
			}

			// List.
			status, data = doRaw(t, http.MethodGet, base+"/todos", "")
			if status != http.StatusOK {
				t.Fatalf("GET list: want 200, got %d (%s)", status, data)
			}
			if got := decodeBody[[]Todo](t, data); len(got) != 1 || got[0] != created {
				t.Fatalf("GET list: want [%+v], got %+v", created, got)
			}

			// Mark done, then undone.
			status, data = doRaw(t, http.MethodPatch, fmt.Sprintf("%s/todos/%d", base, created.ID), `{"done":true}`)
			if status != http.StatusOK {
				t.Fatalf("PATCH done: want 200, got %d (%s)", status, data)
			}
			if got := decodeBody[Todo](t, data); !got.Done {
				t.Fatalf("PATCH done: want done=true, got %+v", got)
			}
			status, data = doRaw(t, http.MethodPatch, fmt.Sprintf("%s/todos/%d", base, created.ID), `{"done":false}`)
			if status != http.StatusOK {
				t.Fatalf("PATCH undone: want 200, got %d (%s)", status, data)
			}
			if got := decodeBody[Todo](t, data); got.Done {
				t.Fatalf("PATCH undone: want done=false, got %+v", got)
			}

			// Delete, then confirm it is gone from all read paths.
			if status, _ := doRaw(t, http.MethodDelete, fmt.Sprintf("%s/todos/%d", base, created.ID), ""); status != http.StatusNoContent {
				t.Fatalf("DELETE: want 204, got %d", status)
			}
			for _, tc := range []struct{ method, url string }{
				{http.MethodGet, fmt.Sprintf("%s/todos/%d", base, created.ID)},
				{http.MethodPatch, fmt.Sprintf("%s/todos/%d", base, created.ID)},
				{http.MethodDelete, fmt.Sprintf("%s/todos/%d", base, created.ID)},
			} {
				body := ""
				if tc.method == http.MethodPatch {
					body = `{"done":true}`
				}
				status, data := doRaw(t, tc.method, tc.url, body)
				if status != http.StatusNotFound {
					t.Fatalf("%s %s: want 404, got %d (%s)", tc.method, tc.url, status, data)
				}
			}
		})
	}
}

func TestNoteAPI(t *testing.T) {
	for _, backend := range testBackends {
		t.Run(backend, func(t *testing.T) {
			base := openTestServer(t, backend)

			status, data := doRaw(t, http.MethodPost, base+"/notes", `{"content":"idea: build a thing"}`)
			if status != http.StatusCreated {
				t.Fatalf("POST: want 201, got %d (%s)", status, data)
			}
			created := decodeBody[Note](t, data)
			if created.ID <= 0 || created.Content != "idea: build a thing" {
				t.Fatalf("unexpected created note: %+v", created)
			}

			status, data = doRaw(t, http.MethodGet, fmt.Sprintf("%s/notes/%d", base, created.ID), "")
			if status != http.StatusOK {
				t.Fatalf("GET one: want 200, got %d (%s)", status, data)
			}
			if got := decodeBody[Note](t, data); got != created {
				t.Fatalf("GET one: want %+v, got %+v", created, got)
			}

			status, data = doRaw(t, http.MethodGet, base+"/notes", "")
			if status != http.StatusOK {
				t.Fatalf("GET list: want 200, got %d (%s)", status, data)
			}
			if got := decodeBody[[]Note](t, data); len(got) != 1 || got[0] != created {
				t.Fatalf("GET list: want [%+v], got %+v", created, got)
			}

			if status, _ := doRaw(t, http.MethodDelete, fmt.Sprintf("%s/notes/%d", base, created.ID), ""); status != http.StatusNoContent {
				t.Fatalf("DELETE: want 204, got %d", status)
			}
			if status, data := doRaw(t, http.MethodGet, fmt.Sprintf("%s/notes/%d", base, created.ID), ""); status != http.StatusNotFound {
				t.Fatalf("GET after delete: want 404, got %d (%s)", status, data)
			}
		})
	}
}

func TestAPIBadRequests(t *testing.T) {
	base := openTestServer(t, "sqlite") // input validation is backend-independent
	cases := []struct {
		name       string
		method     string
		url        string
		body       string
		wantStatus int
	}{
		{"todo empty content", http.MethodPost, base + "/todos", `{"content":""}`, http.StatusBadRequest},
		{"todo blank content", http.MethodPost, base + "/todos", `{"content":"   "}`, http.StatusBadRequest},
		{"todo bad json", http.MethodPost, base + "/todos", `not json`, http.StatusBadRequest},
		{"todo non-numeric id", http.MethodGet, base + "/todos/abc", "", http.StatusBadRequest},
		{"todo zero id", http.MethodGet, base + "/todos/0", "", http.StatusBadRequest},
		{"todo extra path segment", http.MethodGet, base + "/todos/1/extra", "", http.StatusNotFound},
		{"todo missing id", http.MethodGet, base + "/todos/999", "", http.StatusNotFound},
		{"patch missing done", http.MethodPatch, base + "/todos/1", `{}`, http.StatusBadRequest},
		{"patch bad json", http.MethodPatch, base + "/todos/1", `not json`, http.StatusBadRequest},
		{"patch missing id", http.MethodPatch, base + "/todos/999", `{"done":true}`, http.StatusNotFound},
		{"delete missing id", http.MethodDelete, base + "/todos/999", "", http.StatusNotFound},
		{"note empty content", http.MethodPost, base + "/notes", `{"content":""}`, http.StatusBadRequest},
		{"note bad json", http.MethodPost, base + "/notes", `not json`, http.StatusBadRequest},
		{"note missing id", http.MethodGet, base + "/notes/999", "", http.StatusNotFound},
		{"note delete missing id", http.MethodDelete, base + "/notes/999", "", http.StatusNotFound},
		{"wrong method on collection", http.MethodPut, base + "/todos", "", http.StatusMethodNotAllowed},
		{"wrong method on member", http.MethodPost, base + "/todos/1", "", http.StatusMethodNotAllowed},
		{"notes have no patch", http.MethodPatch, base + "/notes/1", `{"content":"x"}`, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, data := doRaw(t, tc.method, tc.url, tc.body)
			if status != tc.wantStatus {
				t.Fatalf("%s %s: want %d, got %d (%s)", tc.method, tc.url, tc.wantStatus, status, data)
			}
			// Every error must use the shared shape.
			if status >= 400 {
				se := decodeBody[serverError](t, data)
				if strings.TrimSpace(se.Error) == "" {
					t.Fatalf("%s %s: want non-empty error message, got %s", tc.method, tc.url, data)
				}
			}
		})
	}
}

// TestClientRoundTrip exercises the full Store contract through the HTTP
// client against a live server — the same operations the CLI performs.
func TestClientRoundTrip(t *testing.T) {
	for _, backend := range testBackends {
		t.Run(backend, func(t *testing.T) {
			c := NewClient(openTestServer(t, backend))
			defer c.Close()

			a, err := c.AddTodo("buy milk")
			if err != nil {
				t.Fatal(err)
			}
			b, err := c.AddTodo("write report")
			if err != nil {
				t.Fatal(err)
			}
			if a.CreatedAt.IsZero() || b.CreatedAt.IsZero() {
				t.Fatalf("want timestamps preserved over HTTP: %+v %+v", a, b)
			}
			todos, err := c.GetTodos()
			if err != nil {
				t.Fatal(err)
			}
			if len(todos) != 2 {
				t.Fatalf("want 2 todos, got %+v", todos)
			}
			if err := c.MarkTodoDone(a.ID); err != nil {
				t.Fatal(err)
			}
			if got, err := c.GetTodo(a.ID); err != nil || !got.Done {
				t.Fatalf("want todo %d done, got %+v, err %v", a.ID, got, err)
			}
			if err := c.MarkTodoUndone(a.ID); err != nil {
				t.Fatal(err)
			}
			if err := c.DeleteTodo(b.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := c.GetTodo(b.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}

			n, err := c.AddNote("idea: build a thing")
			if err != nil {
				t.Fatal(err)
			}
			if got, err := c.GetNote(n.ID); err != nil || got.Content != n.Content {
				t.Fatalf("want note %+v, got %+v, err %v", n, got, err)
			}
			if err := c.DeleteNote(n.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := c.GetNote(n.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
			if _, err := c.AddTodo("   "); err == nil {
				t.Fatal("want error for blank todo over HTTP")
			}
		})
	}
}

// TestClientServerDown pins the Phase 2 error-handling requirement: when the
// server is not running, the client must say so plainly instead of leaking
// a bare "connection refused".
func TestClientServerDown(t *testing.T) {
	ts := httptest.NewServer(NewServer(mustOpenSQLite(t)))
	url := ts.URL
	ts.Close() // nothing listening anymore

	c := NewClient(url)
	defer c.Close()
	_, err := c.GetTodos()
	if err == nil {
		t.Fatal("want error when server is down")
	}
	if !strings.Contains(err.Error(), "cannot reach server") || !strings.Contains(err.Error(), "rileighos serve") {
		t.Fatalf("want a helpful is-it-running error, got: %v", err)
	}
}

func mustOpenSQLite(t *testing.T) Store {
	t.Helper()
	s, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "down.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCheckoffAPI(t *testing.T) {
	for _, backend := range testBackends {
		t.Run(backend, func(t *testing.T) {
			base := openTestServer(t, backend)

			// Create.
			status, data := doRaw(t, http.MethodPost, base+"/checkoffs", `{"name":"exercise"}`)
			if status != http.StatusCreated {
				t.Fatalf("POST: want 201, got %d (%s)", status, data)
			}
			created := decodeBody[Checkoff](t, data)
			if created.ID <= 0 || created.Name != "exercise" {
				t.Fatalf("unexpected created checkoff: %+v", created)
			}
			url := fmt.Sprintf("%s/checkoffs/%d", base, created.ID)

			// Fetch one, unchecked: zero streak.
			status, data = doRaw(t, http.MethodGet, url, "")
			if status != http.StatusOK {
				t.Fatalf("GET one: want 200, got %d (%s)", status, data)
			}
			view := decodeBody[CheckoffView](t, data)
			if view.Streak != 0 || view.CheckedToday || len(view.Days) != 0 {
				t.Fatalf("want fresh zero view, got %+v", view)
			}

			// Check two explicit days (deterministic streak without
			// waiting for real consecutive days).
			for _, day := range []string{"2026-09-12", "2026-09-13"} {
				status, data = doRaw(t, http.MethodPost, url+"/check", fmt.Sprintf(`{"day":%q}`, day))
				if status != http.StatusOK {
					t.Fatalf("POST check %s: want 200, got %d (%s)", day, status, data)
				}
			}
			// Double-check is idempotent, not an error.
			if status, _ := doRaw(t, http.MethodPost, url+"/check", `{"day":"2026-09-13"}`); status != http.StatusOK {
				t.Fatalf("POST re-check: want 200, got %d", status)
			}
			status, data = doRaw(t, http.MethodGet, url, "")
			view = decodeBody[CheckoffView](t, data)
			if status != http.StatusOK || len(view.Days) != 2 {
				t.Fatalf("want 2 days after idempotent checks, got %+v (%s)", view, data)
			}

			// Uncheck one day.
			if status, _ := doRaw(t, http.MethodDelete, url+"/check", `{"day":"2026-09-12"}`); status != http.StatusOK {
				t.Fatalf("DELETE check: want 200, got %d", status)
			}
			status, data = doRaw(t, http.MethodGet, url, "")
			if view = decodeBody[CheckoffView](t, data); len(view.Days) != 1 {
				t.Fatalf("want 1 day after uncheck, got %+v", view)
			}

			// List shows the habit; delete removes it and its days.
			status, data = doRaw(t, http.MethodGet, base+"/checkoffs", "")
			if status != http.StatusOK {
				t.Fatalf("GET list: want 200, got %d (%s)", status, data)
			}
			if got := decodeBody[[]Checkoff](t, data); len(got) != 1 {
				t.Fatalf("want 1 checkoff, got %+v", got)
			}
			if status, _ := doRaw(t, http.MethodDelete, url, ""); status != http.StatusNoContent {
				t.Fatalf("DELETE: want 204, got %d", status)
			}
			if status, data := doRaw(t, http.MethodGet, url, ""); status != http.StatusNotFound {
				t.Fatalf("GET after delete: want 404, got %d (%s)", status, data)
			}
		})
	}
}

func TestTodayAPI(t *testing.T) {
	for _, backend := range testBackends {
		t.Run(backend, func(t *testing.T) {
			base := openTestServer(t, backend)

			if status, data := doRaw(t, http.MethodPost, base+"/todos", `{"content":"open task"}`); status != http.StatusCreated {
				t.Fatalf("POST todo: want 201, got %d (%s)", status, data)
			}
			status, data := doRaw(t, http.MethodPost, base+"/todos", `{"content":"done task"}`)
			if status != http.StatusCreated {
				t.Fatalf("POST todo: want 201, got %d (%s)", status, data)
			}
			doneID := decodeBody[Todo](t, data).ID
			if status, _ := doRaw(t, http.MethodPatch, fmt.Sprintf("%s/todos/%d", base, doneID), `{"done":true}`); status != http.StatusOK {
				t.Fatalf("PATCH done: want 200, got %d", status)
			}
			status, data = doRaw(t, http.MethodPost, base+"/checkoffs", `{"name":"exercise"}`)
			if status != http.StatusCreated {
				t.Fatalf("POST checkoff: want 201, got %d (%s)", status, data)
			}
			habitID := decodeBody[Checkoff](t, data).ID
			// Empty body means today — the CLI's check path.
			if status, _ := doRaw(t, http.MethodPost, fmt.Sprintf("%s/checkoffs/%d/check", base, habitID), ""); status != http.StatusOK {
				t.Fatalf("POST check today: want 200, got %d", status)
			}

			status, data = doRaw(t, http.MethodGet, base+"/today", "")
			if status != http.StatusOK {
				t.Fatalf("GET /today: want 200, got %d (%s)", status, data)
			}
			view := decodeBody[TodayView](t, data)
			if view.Date != Today() {
				t.Fatalf("want date %s, got %s", Today(), view.Date)
			}
			if len(view.OpenTodos) != 1 || view.OpenTodos[0].Content != "open task" {
				t.Fatalf("want only the open todo, got %+v", view.OpenTodos)
			}
			if len(view.Checkoffs) != 1 {
				t.Fatalf("want 1 checkoff, got %+v", view.Checkoffs)
			}
			got := view.Checkoffs[0]
			if !got.CheckedToday || got.Streak != 1 {
				t.Fatalf("want checked-today streak 1, got %+v", got)
			}
			// Wrong method still uses the shared JSON error shape.
			if status, _ := doRaw(t, http.MethodPost, base+"/today", ""); status != http.StatusMethodNotAllowed {
				t.Fatalf("POST /today: want 405, got %d", status)
			}
		})
	}
}

func TestCheckoffBadRequests(t *testing.T) {
	base := openTestServer(t, "sqlite") // input validation is backend-independent
	cases := []struct {
		name       string
		method     string
		url        string
		body       string
		wantStatus int
	}{
		{"empty name", http.MethodPost, base + "/checkoffs", `{"name":""}`, http.StatusBadRequest},
		{"blank name", http.MethodPost, base + "/checkoffs", `{"name":"   "}`, http.StatusBadRequest},
		{"bad json", http.MethodPost, base + "/checkoffs", `not json`, http.StatusBadRequest},
		{"missing id", http.MethodGet, base + "/checkoffs/999", "", http.StatusNotFound},
		{"non-numeric id", http.MethodGet, base + "/checkoffs/abc", "", http.StatusBadRequest},
		{"delete missing id", http.MethodDelete, base + "/checkoffs/999", "", http.StatusNotFound},
		{"check missing id", http.MethodPost, base + "/checkoffs/999/check", `{"day":"2026-09-14"}`, http.StatusNotFound},
		{"check bad day", http.MethodPost, base + "/checkoffs/1/check", `{"day":"yesterday"}`, http.StatusBadRequest},
		{"check impossible day", http.MethodPost, base + "/checkoffs/1/check", `{"day":"2026-02-30"}`, http.StatusBadRequest},
		{"check bad json", http.MethodPost, base + "/checkoffs/1/check", `not json`, http.StatusBadRequest},
		{"uncheck bad day", http.MethodDelete, base + "/checkoffs/1/check", `{"day":"13-09-2026"}`, http.StatusBadRequest},
		{"wrong method on collection", http.MethodPut, base + "/checkoffs", "", http.StatusMethodNotAllowed},
		{"wrong method on member", http.MethodPatch, base + "/checkoffs/1", "", http.StatusMethodNotAllowed},
		{"wrong method on check", http.MethodGet, base + "/checkoffs/1/check", "", http.StatusMethodNotAllowed},
		{"deep path", http.MethodGet, base + "/checkoffs/1/check/extra", "", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, data := doRaw(t, tc.method, tc.url, tc.body)
			if status != tc.wantStatus {
				t.Fatalf("%s %s: want %d, got %d (%s)", tc.method, tc.url, tc.wantStatus, status, data)
			}
			if status >= 400 {
				se := decodeBody[serverError](t, data)
				if strings.TrimSpace(se.Error) == "" {
					t.Fatalf("%s %s: want non-empty error message, got %s", tc.method, tc.url, data)
				}
			}
		})
	}
}

// TestClientCheckoffRoundTrip runs the checkoff contract plus /today
// through the HTTP client, mirroring what the new CLI commands do.
func TestClientCheckoffRoundTrip(t *testing.T) {
	for _, backend := range testBackends {
		t.Run(backend, func(t *testing.T) {
			c := NewClient(openTestServer(t, backend))
			defer c.Close()

			habit, err := c.AddCheckoff("exercise")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.AddCheckoff(""); err == nil {
				t.Fatal("want error for blank checkoff over HTTP")
			}
			if err := c.CheckDay(habit.ID, "2026-09-12"); err != nil {
				t.Fatal(err)
			}
			if err := c.CheckDay(habit.ID, ""); err != nil { // empty means today
				t.Fatal(err)
			}
			days, err := c.GetCheckoffDays(habit.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(days) != 2 || days[len(days)-1] != Today() {
				t.Fatalf("want 2 days ending today, got %v", days)
			}
			if err := c.UncheckDay(habit.ID, "2026-09-12"); err != nil {
				t.Fatal(err)
			}
			if err := c.CheckDay(999, "2026-09-14"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}

			view, err := c.GetToday()
			if err != nil {
				t.Fatal(err)
			}
			if view.Date != Today() || len(view.Checkoffs) != 1 {
				t.Fatalf("unexpected today view: %+v", view)
			}
			got := view.Checkoffs[0]
			if !got.CheckedToday || got.Streak != 1 {
				t.Fatalf("want checked-today streak 1, got %+v", got)
			}

			if err := c.DeleteCheckoff(habit.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := c.GetCheckoff(habit.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
		})
	}
}
