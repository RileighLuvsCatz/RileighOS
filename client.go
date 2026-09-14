package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
var _ Store = (*Client)(nil)

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

// serverError decodes the server's {"error": "..."} body for diagnostics.
type serverError struct {
	Error string `json:"error"`
}

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
		var se serverError
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &se) == nil && se.Error != "" {
			msg = se.Error
		}
		if resp.StatusCode == http.StatusNotFound {
			// Preserve ErrNotFound across the network so callers can
			// keep using errors.Is exactly as with a local backend.
			return fmt.Errorf("%s: %w", msg, ErrNotFound)
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

func (c *Client) AddTodo(content string) (Todo, error) {
	var t Todo
	if err := c.do(http.MethodPost, "/todos", contentBody{Content: content}, &t); err != nil {
		return Todo{}, err
	}
	return t, nil
}

func (c *Client) GetTodos() ([]Todo, error) {
	todos := []Todo{}
	if err := c.do(http.MethodGet, "/todos", nil, &todos); err != nil {
		return nil, err
	}
	return todos, nil
}

func (c *Client) GetTodo(id int) (Todo, error) {
	var t Todo
	if err := c.do(http.MethodGet, fmt.Sprintf("/todos/%d", id), nil, &t); err != nil {
		return Todo{}, err
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
	return c.do(http.MethodPatch, fmt.Sprintf("/todos/%d", id), todoPatch{Done: &done}, nil)
}

func (c *Client) DeleteTodo(id int) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/todos/%d", id), nil, nil)
}

// --- notes ---

func (c *Client) AddNote(content string) (Note, error) {
	var n Note
	if err := c.do(http.MethodPost, "/notes", contentBody{Content: content}, &n); err != nil {
		return Note{}, err
	}
	return n, nil
}

func (c *Client) GetNotes() ([]Note, error) {
	notes := []Note{}
	if err := c.do(http.MethodGet, "/notes", nil, &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

func (c *Client) GetNote(id int) (Note, error) {
	var n Note
	if err := c.do(http.MethodGet, fmt.Sprintf("/notes/%d", id), nil, &n); err != nil {
		return Note{}, err
	}
	return n, nil
}

func (c *Client) DeleteNote(id int) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/notes/%d", id), nil, nil)
}
