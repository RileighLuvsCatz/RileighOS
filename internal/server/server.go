package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"rdb/rileighos/internal/api"
	"rdb/rileighos/internal/canvas"
	"rdb/rileighos/internal/models"
	"rdb/rileighos/internal/store"
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
//	GET    /checkoffs      list all checkoffs
//	POST   /checkoffs      create a checkoff, body {"name": "..."} -> 201
//	GET    /checkoffs/{id} fetch one checkoff (with days, streak, checked_today)
//	DELETE /checkoffs/{id} delete a checkoff and its days -> 204
//	POST   /checkoffs/{id}/check   check a day, body {} or {"day": "YYYY-MM-DD"} -> 200
//	DELETE /checkoffs/{id}/check   uncheck a day, same body shape -> 200
//	GET    /today          "what does my day look like": open todos plus
//	                       check-offs with streaks, in one call
//	GET    /canvas/courses refresh the tracked course list from Canvas
//	                       (new courses arrive pending_confirm)
//	PATCH  /canvas/courses/{id} resolve one course, body {"excluded": bool};
//	                       answers the updated course plus detached counts
//	POST   /canvas/sync    import published assignments, body
//	                       {"mode": "manual"|"auto", "decisions": {"<course_id>": bool}}
//	                       mapping course ID -> excluded; absent entries stay
//	                       pending (manual) or import flagged pending (auto)
//	GET    /canvas/status  sync probe: {"configured": bool, "last_sync_at": ...},
//	                       never imports, never 501s
//
// Canvas routes need RILEIGHOS_CANVAS_TOKEN (and optionally
// RILEIGHOS_CANVAS_BASE_URL, defaulting to uwmil.instructure.com). Without
// a token they answer 501: deployed but not configured. Upstream Canvas
// failures answer 502; the token never appears in any response.
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
	store store.FullStore
	mux   *http.ServeMux
}

// NewServer builds the route table around store. The server itself is an
// http.Handler, so the caller just serves it, e.g.
// http.ListenAndServe(addr, NewServer(store)).
func NewServer(store store.FullStore) *Server {
	s := &Server{store: store, mux: http.NewServeMux()}
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/todos", s.handleTodos)
	s.mux.HandleFunc("/todos/", s.handleTodoByID)
	s.mux.HandleFunc("/notes", s.handleNotes)
	s.mux.HandleFunc("/notes/", s.handleNoteByID)
	s.mux.HandleFunc("/checkoffs", s.handleCheckoffs)
	s.mux.HandleFunc("/checkoffs/", s.handleCheckoffByID)
	s.mux.HandleFunc("/today", s.handleToday)
	s.mux.HandleFunc("/canvas/courses", s.handleCanvasCourses)
	s.mux.HandleFunc("/canvas/courses/", s.handleCanvasCourseByID)
	s.mux.HandleFunc("/canvas/sync", s.handleCanvasSync)
	s.mux.HandleFunc("/canvas/status", s.handleCanvasStatus)
	return s
}

// Handler exposes the route table as an http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

// ServeHTTP makes Server itself an http.Handler, so it can be passed
// directly to http.ListenAndServe or httptest.NewServer.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoding to the network cannot fail in any way the client could act
	// on, and the headers are already sent, so there is nothing useful to
	// do with the error beyond ignoring it.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, api.ErrorBody{Error: fmt.Sprintf(format, args...)})
}

// writeStoreError maps Store failures to status codes: unknown IDs become
// 404 (mirroring ErrNotFound), everything else is a 500.
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "%s", err)
		return
	}
	writeError(w, http.StatusInternalServerError, "%s", err)
}

// decodeContent parses a {"content": "..."} body, capping its size so a
// misbehaving client cannot make us buffer unbounded input.
func decodeContent(r *http.Request) (string, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	var b api.ContentBody
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
	var p api.TodoPatch
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

// --- checkoffs ---

// handleCheckoffs dispatches the /checkoffs collection route.
func (s *Server) handleCheckoffs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		checkoffs, err := s.store.GetCheckoffs()
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, checkoffs)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
		var b api.NameBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: %s", err)
			return
		}
		c, err := s.store.AddCheckoff(b.Name)
		if err != nil {
			if strings.TrimSpace(b.Name) == "" {
				writeError(w, http.StatusBadRequest, "%s", err)
				return
			}
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	default:
		methodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

// handleCheckoffByID dispatches /checkoffs/{id} and /checkoffs/{id}/check.
func (s *Server) handleCheckoffByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/checkoffs/")
	parts := strings.Split(rest, "/")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] != "check") {
		writeError(w, http.StatusNotFound, "no such checkoff route %q", r.URL.Path)
		return
	}
	id, ok := pathID(w, parts[0], "checkoff")
	if !ok {
		return
	}
	if len(parts) == 2 {
		s.handleCheck(w, r, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		view, err := s.viewCheckoff(id, store.Today())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodDelete:
		if err := s.store.DeleteCheckoff(id); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, r, http.MethodGet, http.MethodDelete)
	}
}

// handleCheck dispatches the /checkoffs/{id}/check sub-route: POST checks
// a day (idempotent), DELETE unchecks it. Both answer with the updated
// view so the client does not need a second GET.
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request, id int) {
	switch r.Method {
	case http.MethodPost, http.MethodDelete:
		// Validated and dispatched below.
	default:
		methodNotAllowed(w, r, http.MethodPost, http.MethodDelete)
		return
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	var b api.CheckBody
	// An empty body just means "today" — but it must still be valid JSON
	// when present, so only tolerate EOF, not garbage.
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err)
		return
	}
	day := b.Day
	if day == "" {
		day = store.Today()
	}
	if !store.ValidDay(day) {
		writeError(w, http.StatusBadRequest, "invalid day %q: want YYYY-MM-DD", b.Day)
		return
	}
	var err error
	if r.Method == http.MethodPost {
		err = s.store.CheckDay(id, day)
	} else {
		err = s.store.UncheckDay(id, day)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	view, err := s.viewCheckoff(id, store.Today())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// viewCheckoff builds the derived view for one checkoff as of today.
func (s *Server) viewCheckoff(id int, today string) (models.CheckoffView, error) {
	c, err := s.store.GetCheckoff(id)
	if err != nil {
		return models.CheckoffView{}, err
	}
	days, err := s.store.GetCheckoffDays(id)
	if err != nil {
		return models.CheckoffView{}, err
	}
	checked := false
	for _, d := range days {
		if d == today {
			checked = true
			break
		}
	}
	return models.CheckoffView{
		Checkoff:     c,
		Days:         days,
		Streak:       store.CurrentStreak(days, today),
		CheckedToday: checked,
	}, nil
}

// handleToday answers "what does my day look like": open todos plus every
// check-off with its streak, assembled by the store as of the
// server-local date — so the CLI gets the same answer over HTTP that a
// local backend would give directly.
func (s *Server) handleToday(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	view, err := s.store.GetToday()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
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

// --- canvas ---

// writeCanvasError maps sync failures: upstream Canvas problems become 502,
// store failures keep the shared mapping. Canvas errors never contain the
// token by construction (see CanvasClient).
func writeCanvasError(w http.ResponseWriter, err error) {
	if errors.Is(err, canvas.ErrCanvasUpstream) {
		writeError(w, http.StatusBadGateway, "%s", err)
		return
	}
	writeStoreError(w, err)
}

// handleCanvasCourses serves GET /canvas/courses: refresh the tracked
// course list from Canvas (new courses arrive pending_confirm) and return
// the full gating state for the CLI to prompt over.
func (s *Server) handleCanvasCourses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	cv, err := canvas.NewCanvasClientFromEnv()
	if err != nil {
		writeError(w, http.StatusNotImplemented, "%s", err)
		return
	}
	upstream, err := cv.GetCourses()
	if err != nil {
		writeCanvasError(w, err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, info := range upstream {
		if _, err := s.store.UpsertCanvasCourse(models.CanvasCourse{
			CourseID: info.ID, Code: info.Code, Name: info.Name,
			Excluded: false, PendingConfirm: true, LastSeen: now,
		}); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	courses, err := s.store.GetCanvasCourses()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, courses)
}

// handleCanvasCourseByID serves PATCH /canvas/courses/{id}: record the
// user's include/exclude decision, clearing pending_confirm.
func (s *Server) handleCanvasCourseByID(w http.ResponseWriter, r *http.Request) {
	raw, ok := memberID(w, r.URL.Path, "/canvas/courses/", "canvas course")
	if !ok {
		return
	}
	if r.Method != http.MethodPatch {
		methodNotAllowed(w, r, http.MethodPatch)
		return
	}
	id, ok := pathID(w, raw, "canvas course")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	var b api.CourseResolve
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err)
		return
	}
	if b.Excluded == nil {
		writeError(w, http.StatusBadRequest, `nothing to update: body must set "excluded"`)
		return
	}
	if err := s.store.ResolveCanvasCourse(int64(id), *b.Excluded); err != nil {
		writeStoreError(w, err)
		return
	}
	var detachedTodos, detachedNotes int
	if *b.Excluded {
		// Excluding converts already-imported items to local ones; the
		// course stops importing but nothing the user has is deleted.
		var err error
		detachedTodos, detachedNotes, err = s.store.DetachCanvasCourse(int64(id))
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	courses, err := s.store.GetCanvasCourses()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, c := range courses {
		if c.CourseID == int64(id) {
			writeJSON(w, http.StatusOK, models.CourseResolveResult{
				Course: c, DetachedTodos: detachedTodos, DetachedNotes: detachedNotes,
			})
			return
		}
	}
	writeStoreError(w, fmt.Errorf("canvas course %d: %w", id, store.ErrNotFound))
}

// handleCanvasSync serves POST /canvas/sync: apply decisions, then import.
// It answers with the SyncResult summary the CLI prints.
func (s *Server) handleCanvasSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	cv, err := canvas.NewCanvasClientFromEnv()
	if err != nil {
		writeError(w, http.StatusNotImplemented, "%s", err)
		return
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	var b api.SyncRequest
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err)
		return
	}
	mode := models.SyncModeManual
	if b.Mode != "" {
		mode = models.SyncMode(b.Mode)
		if mode != models.SyncModeManual && mode != models.SyncModeAuto {
			writeError(w, http.StatusBadRequest, "invalid mode %q: want \"manual\" or \"auto\"", b.Mode)
			return
		}
	}
	decisions := models.SyncDecisions{}
	for raw, excluded := range b.Decisions {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid course id %q: must be a positive number", raw)
			return
		}
		decisions[id] = excluded
	}
	res, err := canvas.RunCanvasSync(s.store, cv, mode, decisions)
	if err != nil {
		writeCanvasError(w, err)
		return
	}
	if res.PendingCourses == nil {
		res.PendingCourses = []models.CanvasCourse{}
	}
	writeJSON(w, http.StatusOK, res)
}

// handleCanvasStatus serves GET /canvas/status: the cheap probe the CLI
// uses to decide whether an on-open auto-sync is due. It never imports and
// never requires a token: unconfigured servers answer configured=false.
func (s *Server) handleCanvasStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	status := models.CanvasStatus{Configured: os.Getenv("RILEIGHOS_CANVAS_TOKEN") != ""}
	if last, err := s.store.GetLastCanvasSync(); err != nil {
		writeStoreError(w, err)
		return
	} else if !last.IsZero() {
		status.LastSyncAt = &last
	}
	writeJSON(w, http.StatusOK, status)
}
