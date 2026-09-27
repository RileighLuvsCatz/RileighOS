package api

// Wire shapes shared by the server (decode) and the HTTP client (encode).
// Every JSON request/response body in the API uses one of these, so both
// sides stay boring and consistent by construction.

// ErrorBody is the single error response shape: {"error": "message"}.
type ErrorBody struct {
	Error string `json:"error"`
}

// ContentBody is the create-request shape: POST /todos and POST /notes
// both take {"content": "..."}.
type ContentBody struct {
	Content string `json:"content"`
}

// TodoPatch is the PATCH /todos/{id} request shape. Done is a pointer so
// a missing field is distinguishable from an explicit false.
type TodoPatch struct {
	Done *bool `json:"done"`
}

// NameBody is the POST /checkoffs request shape.
type NameBody struct {
	Name string `json:"name"`
}

// CheckBody is the check/uncheck request shape. An empty day means today;
// an explicit day enables backfill and deterministic streak tests.
type CheckBody struct {
	Day string `json:"day"`
}

// CourseResolve is the PATCH /canvas/courses/{id} request shape. Excluded
// is a pointer so a missing field is distinguishable from an explicit false.
type CourseResolve struct {
	Excluded *bool `json:"excluded"`
}

// SyncRequest is the POST /canvas/sync request shape: course ID (as a JSON
// string key) -> excluded, plus the import mode ("manual" skips unconfirmed
// courses, "auto" imports them but keeps them pending). Absent decisions
// leave the course pending; an empty mode defaults to manual, and an empty
// or missing body means manual with no decisions.
type SyncRequest struct {
	Mode      string          `json:"mode"`
	Decisions map[string]bool `json:"decisions"`
}
