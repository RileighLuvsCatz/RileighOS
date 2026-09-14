package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Server wraps a Store in a small JSON-over-HTTP API using only net/http
// (no framework). The route table is deliberately boring:
//
//	GET    /healthz        liveness check, {"ok": true}
//	GET    /todos          list all todos
//	POST   /todos          create a todo, body {"content": "..."} -> 201
//	GET    /todos/{id}     fetch one todo
//	PATCH  /todos/{id}     flip done state, body {"done": true|false}
//	DELETE /todos/{id}     delete a todo -> 204
//	GET    /notes          list all notes
//	POST   /notes          create a note, body {"content": "..."} -> 201
//	GET    /notes/{id}     fetch one note
//	DELETE /notes/{id}     delete a note -> 204
//
// Notes intentionally have no PATCH route: they carry no done state and
// the Store has no content-update operation, so there is nothing mutable
// to patch. Likewise PATCH on a todo only flips done — content edits
// would need a new Store method and are out of scope for this phase.
//
// Error responses are always {"error": "message"} with 400 for bad input,
// 404 when the ID does not exist (matching ErrNotFound), and 405 for a
// wrong method. Methods are dispatched by hand (rather than with the mux's
// "METHOD /path" patterns) precisely so that even the 405s use this shape.
type Server struct {
	store Store
	mux   *http.ServeMux
}

// NewServer builds the route table around store. The server itself is an
// http.Handler, so the caller just serves it, e.g.
// http.ListenAndServe(addr, NewServer(store)).
func NewServer(store Store) *Server {
	s := &Server{store: store, mux: http.NewServeMux()}
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/todos", s.handleTodos)
	s.mux.HandleFunc("/todos/", s.handleTodoByID)
	s.mux.HandleFunc("/notes", s.handleNotes)
	s.mux.HandleFunc("/notes/", s.handleNoteByID)
	return s
}

// Handler exposes the route table as an http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

// ServeHTTP makes Server itself an http.Handler, so it can be passed
// directly to http.ListenAndServe or httptest.NewServer.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// --- helpers ---

// errorBody is the single error response shape used by every endpoint.
type errorBody struct {
	Error string `json:"error"`
}

// contentBody is the single create-request shape: POST /todos and
// POST /notes both take {"content": "..."}.
type contentBody struct {
	Content string `json:"content"`
}

// todoPatch is the PATCH /todos/{id} request shape. Done is a pointer so
// a missing field is distinguishable from an explicit false.
type todoPatch struct {
	Done *bool `json:"done"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoding to the network cannot fail in any way the client could act
	// on, and the headers are already sent, so there is nothing useful to
	// do with the error beyond ignoring it.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, errorBody{Error: fmt.Sprintf(format, args...)})
}

// writeStoreError maps Store failures to status codes: unknown IDs become
// 404 (mirroring ErrNotFound), everything else is a 500.
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "%s", err)
		return
	}
	writeError(w, http.StatusInternalServerError, "%s", err)
}

// decodeContent parses a {"content": "..."} body, capping its size so a
// misbehaving client cannot make us buffer unbounded input.
func decodeContent(r *http.Request) (string, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	var b contentBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		return "", err
	}
	return b.Content, nil
}

// pathID extracts and validates the {id} tail of a /todos/{id} or
// /notes/{id} URL. Non-numeric IDs are a 400 (the client built a bad URL),
// while well-formed-but-absent IDs surface later as 404.
func pathID(w http.ResponseWriter, raw, kind string) (int, bool) {
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid %s id %q: must be a positive number", kind, raw)
		return 0, false
	}
	return id, true
}

// memberID splits "/todos/123" (prefix "/todos/") into "123", rejecting
// deeper paths like "/todos/1/extra" as a 404 — there is no such route.
func memberID(w http.ResponseWriter, path, prefix, kind string) (string, bool) {
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		writeError(w, http.StatusNotFound, "no such %s route %q", kind, path)
		return "", false
	}
	return raw, true
}

// methodNotAllowed answers with the shared JSON error shape plus an Allow
// header, so clients can see what the route does accept.
func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow ...string) {
	w.Header().Set("Allow", strings.Join(allow, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method %s not allowed (want %s)", r.Method, strings.Join(allow, " or "))
}

// --- health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- todos ---

// handleTodos dispatches the /todos collection route.
func (s *Server) handleTodos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListTodos(w, r)
	case http.MethodPost:
		s.handleCreateTodo(w, r)
	default:
		methodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

// handleTodoByID dispatches the /todos/{id} member route.
func (s *Server) handleTodoByID(w http.ResponseWriter, r *http.Request) {
	raw, ok := memberID(w, r.URL.Path, "/todos/", "todo")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodPatch, http.MethodDelete:
		// Validated and dispatched below.
	default:
		methodNotAllowed(w, r, http.MethodGet, http.MethodPatch, http.MethodDelete)
		return
	}
	id, ok := pathID(w, raw, "todo")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getTodo(w, id)
	case http.MethodPatch:
		s.patchTodo(w, r, id)
	case http.MethodDelete:
		if err := s.store.DeleteTodo(id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleListTodos(w http.ResponseWriter, _ *http.Request) {
	todos, err := s.store.GetTodos()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, todos)
}

func (s *Server) handleCreateTodo(w http.ResponseWriter, r *http.Request) {
	content, err := decodeContent(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err)
		return
	}
	t, err := s.store.AddTodo(content)
	if err != nil {
		// AddTodo only fails validation (blank content) or storage
		// errors; blank content is a client mistake, so it is a 400.
		if strings.TrimSpace(content) == "" {
			writeError(w, http.StatusBadRequest, "%s", err)
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) getTodo(w http.ResponseWriter, id int) {
	t, err := s.store.GetTodo(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) patchTodo(w http.ResponseWriter, r *http.Request, id int) {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	var p todoPatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err)
		return
	}
	if p.Done == nil {
		writeError(w, http.StatusBadRequest, `nothing to update: body must set "done"`)
		return
	}
	var err error
	if *p.Done {
		err = s.store.MarkTodoDone(id)
	} else {
		err = s.store.MarkTodoUndone(id)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Return the updated todo so the client does not need a second GET.
	s.getTodo(w, id)
}

// --- notes ---

// handleNotes dispatches the /notes collection route.
func (s *Server) handleNotes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListNotes(w, r)
	case http.MethodPost:
		s.handleCreateNote(w, r)
	default:
		methodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

// handleNoteByID dispatches the /notes/{id} member route. There is no PATCH:
// notes carry no done state and the Store has no content-update operation,
// so there is nothing mutable to patch.
func (s *Server) handleNoteByID(w http.ResponseWriter, r *http.Request) {
	raw, ok := memberID(w, r.URL.Path, "/notes/", "note")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		// Validated and dispatched below.
	default:
		methodNotAllowed(w, r, http.MethodGet, http.MethodDelete)
		return
	}
	id, ok := pathID(w, raw, "note")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		n, err := s.store.GetNote(id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, n)
	case http.MethodDelete:
		if err := s.store.DeleteNote(id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleListNotes(w http.ResponseWriter, _ *http.Request) {
	notes, err := s.store.GetNotes()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notes)
}

func (s *Server) handleCreateNote(w http.ResponseWriter, r *http.Request) {
	content, err := decodeContent(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err)
		return
	}
	n, err := s.store.AddNote(content)
	if err != nil {
		if strings.TrimSpace(content) == "" {
			writeError(w, http.StatusBadRequest, "%s", err)
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}
