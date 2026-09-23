package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rdb/rileighos/internal/api"
	"rdb/rileighos/internal/models"
	"rdb/rileighos/internal/store"
)

// Client is a Store backed by a RileighOS server over HTTP. It exists so the
// CLI never touches storage directly: every command becomes one HTTP
// request, and swapping which machine serves the data is just a URL change
// (localhost today, the Pi's Tailscale address in Phase 3).
//
// Client satisfies the Store interface (Close is a no-op — there is no local
// state to release), which means the CLI command handlers in main.go work
// unchanged whether they are handed a local backend or this client.
type Client struct {
	baseURL string
	http    *http.Client
}

// Compile-time check that Client satisfies Store.
var _ store.Store = (*Client)(nil)

// CanvasSyncer is the CLI-facing Canvas surface: course gating state and
// one-shot import, all served by the Pi over HTTP. The remaining Canvas
// store methods (dedup lookups, course upserts, sync timestamps) live
// server-side only, so Client deliberately does not implement FullStore.
type CanvasSyncer interface {
	GetCanvasCourses() ([]models.CanvasCourse, error)
	SyncCanvas(models.SyncDecisions) (models.SyncResult, error)
	ResolveCanvasCourse(int64, bool) (models.CanvasCourse, error)
}

// Compile-time check that Client satisfies CanvasSyncer.
var _ CanvasSyncer = (*Client)(nil)

// NewClient builds a client for the server at baseURL,
// e.g. NewClient("http://localhost:8080").
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Close is a no-op; it exists so Client satisfies Store uniformly.
func (c *Client) Close() error { return nil }

// --- request plumbing ---

// do builds and executes one request. On a 4xx/5xx it returns the server's
// error message; if the server cannot be reached at all, the error says so
// plainly and hints at `rileighos serve` instead of dumping a bare
// "connection refused".
func (c *Client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach server at %s (is it running? try `rileighos serve`): %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var se api.ErrorBody
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &se) == nil && se.Error != "" {
			msg = se.Error
		}
		if resp.StatusCode == http.StatusNotFound {
			// Preserve store.ErrNotFound across the network so callers can
			// keep using errors.Is exactly as with a local backend.
			return fmt.Errorf("%s: %w", msg, store.ErrNotFound)
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// --- todos ---

func (c *Client) AddTodo(content string) (models.Todo, error) {
	var t models.Todo
	if err := c.do(http.MethodPost, "/todos", api.ContentBody{Content: content}, &t); err != nil {
		return models.Todo{}, err
	}
	return t, nil
}

func (c *Client) GetTodos() ([]models.Todo, error) {
	todos := []models.Todo{}
	if err := c.do(http.MethodGet, "/todos", nil, &todos); err != nil {
		return nil, err
	}
	return todos, nil
}

func (c *Client) GetTodo(id int) (models.Todo, error) {
	var t models.Todo
	if err := c.do(http.MethodGet, fmt.Sprintf("/todos/%d", id), nil, &t); err != nil {
		return models.Todo{}, err
	}
	return t, nil
}

func (c *Client) MarkTodoDone(id int) error {
	return c.setTodoDone(id, true)
}

func (c *Client) MarkTodoUndone(id int) error {
	return c.setTodoDone(id, false)
}

func (c *Client) setTodoDone(id int, done bool) error {
	return c.do(http.MethodPatch, fmt.Sprintf("/todos/%d", id), api.TodoPatch{Done: &done}, nil)
}

func (c *Client) DeleteTodo(id int) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/todos/%d", id), nil, nil)
}

// --- notes ---

func (c *Client) AddNote(content string) (models.Note, error) {
	var n models.Note
	if err := c.do(http.MethodPost, "/notes", api.ContentBody{Content: content}, &n); err != nil {
		return models.Note{}, err
	}
	return n, nil
}

func (c *Client) GetNotes() ([]models.Note, error) {
	notes := []models.Note{}
	if err := c.do(http.MethodGet, "/notes", nil, &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

func (c *Client) GetNote(id int) (models.Note, error) {
	var n models.Note
	if err := c.do(http.MethodGet, fmt.Sprintf("/notes/%d", id), nil, &n); err != nil {
		return models.Note{}, err
	}
	return n, nil
}

func (c *Client) DeleteNote(id int) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/notes/%d", id), nil, nil)
}

// --- checkoffs ---

func (c *Client) AddCheckoff(name string) (models.Checkoff, error) {
	var out models.Checkoff
	if err := c.do(http.MethodPost, "/checkoffs", api.NameBody{Name: name}, &out); err != nil {
		return models.Checkoff{}, err
	}
	return out, nil
}

func (c *Client) GetCheckoffs() ([]models.Checkoff, error) {
	checkoffs := []models.Checkoff{}
	if err := c.do(http.MethodGet, "/checkoffs", nil, &checkoffs); err != nil {
		return nil, err
	}
	return checkoffs, nil
}

func (c *Client) GetCheckoff(id int) (models.Checkoff, error) {
	view, err := c.GetCheckoffView(id)
	if err != nil {
		return models.Checkoff{}, err
	}
	return view.Checkoff, nil
}

// GetCheckoffView fetches the full derived view (days, streak,
// checked-today). It is a client-only helper — the Store interface deals
// in Checkoff plus GetCheckoffDays — for commands that show one habit.
func (c *Client) GetCheckoffView(id int) (models.CheckoffView, error) {
	var view models.CheckoffView
	if err := c.do(http.MethodGet, fmt.Sprintf("/checkoffs/%d", id), nil, &view); err != nil {
		return models.CheckoffView{}, err
	}
	return view, nil
}

func (c *Client) DeleteCheckoff(id int) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/checkoffs/%d", id), nil, nil)
}

func (c *Client) CheckDay(id int, day string) error {
	return c.do(http.MethodPost, fmt.Sprintf("/checkoffs/%d/check", id), api.CheckBody{Day: day}, nil)
}

func (c *Client) UncheckDay(id int, day string) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/checkoffs/%d/check", id), api.CheckBody{Day: day}, nil)
}

func (c *Client) GetCheckoffDays(id int) ([]string, error) {
	view, err := c.GetCheckoffView(id)
	if err != nil {
		return nil, err
	}
	return view.Days, nil
}

func (c *Client) GetToday() (models.TodayView, error) {
	var view models.TodayView
	if err := c.do(http.MethodGet, "/today", nil, &view); err != nil {
		return models.TodayView{}, err
	}
	return view, nil
}

// --- canvas ---

func (c *Client) GetCanvasCourses() ([]models.CanvasCourse, error) {
	courses := []models.CanvasCourse{}
	if err := c.do(http.MethodGet, "/canvas/courses", nil, &courses); err != nil {
		return nil, err
	}
	return courses, nil
}

// SyncCanvas runs one import with decisions mapping course ID -> excluded.
// Courses absent from decisions stay pending.
func (c *Client) SyncCanvas(decisions models.SyncDecisions) (models.SyncResult, error) {
	raw := map[string]bool{}
	for id, excluded := range decisions {
		raw[strconv.FormatInt(id, 10)] = excluded
	}
	var res models.SyncResult
	if err := c.do(http.MethodPost, "/canvas/sync", api.SyncRequest{Decisions: raw}, &res); err != nil {
		return models.SyncResult{}, err
	}
	return res, nil
}

func (c *Client) ResolveCanvasCourse(courseID int64, excluded bool) (models.CanvasCourse, error) {
	var course models.CanvasCourse
	if err := c.do(http.MethodPatch, fmt.Sprintf("/canvas/courses/%d", courseID), api.CourseResolve{Excluded: &excluded}, &course); err != nil {
		return models.CanvasCourse{}, err
	}
	return course, nil
}
