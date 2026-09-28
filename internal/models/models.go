package models

import "time"

// Todo is a single actionable item with a completion state.
// It is intentionally kept separate from Note: todos are for
// things to do, notes are for things to remember.
//
// DueAt is an optional full timestamp (Canvas `due_at` parity, and future
// notification scheduling). Origin is "" for local items or "canvas" for
// Canvas-imported ones; the Canvas* fields are only set when Origin is
// "canvas" and let re-syncs match, update, and reflect status without
// duplicating.
type Todo struct {
	ID               int        `json:"id"`
	Content          string     `json:"content"`
	Done             bool       `json:"done"`
	CreatedAt        time.Time  `json:"created_at"`
	DueAt            *time.Time `json:"due_at,omitempty"`
	Origin           string     `json:"origin,omitempty"`
	CanvasAssignID   *int64     `json:"canvas_assignment_id,omitempty"`
	CanvasCourseCode string     `json:"canvas_course_code,omitempty"`
	CanvasURL        string     `json:"canvas_url,omitempty"`
	CanvasUpdatedAt  string     `json:"canvas_updated_at,omitempty"`
}

// Note is a free-form piece of text with no completion state.
// Canvas assignments with no submittable type (`submission_types` of
// "none"/"not_graded"/empty, or `grading_type: "not_graded"`) import as
// notes; they carry the same provenance fields as todos
// so re-syncs can update them, but Done/submission state never applies.
type Note struct {
	ID               int        `json:"id"`
	Content          string     `json:"content"`
	CreatedAt        time.Time  `json:"created_at"`
	DueAt            *time.Time `json:"due_at,omitempty"`
	Origin           string     `json:"origin,omitempty"`
	CanvasAssignID   *int64     `json:"canvas_assignment_id,omitempty"`
	CanvasCourseCode string     `json:"canvas_course_code,omitempty"`
	CanvasURL        string     `json:"canvas_url,omitempty"`
	CanvasUpdatedAt  string     `json:"canvas_updated_at,omitempty"`
}

// Checkoff is a named daily habit: a "did I do X today" record type,
// separate from Todo (one-shot actionable items). The habit itself carries
// no state; each checked day is stored alongside it (see CheckoffStore),
// and streaks are derived from those days, never stored.
type Checkoff struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// CheckoffView is a checkoff plus everything derived from its checked
// days as of one date, so a single response answers "how am I doing".
type CheckoffView struct {
	Checkoff
	Days         []string `json:"days"`
	Streak       int      `json:"streak"`
	CheckedToday bool     `json:"checked_today"`
}

// TodayView is the answer to "what does my day look like": check-offs with
// their streaks plus open todos due today or overdue (undated and
// future-dated todos live in `todo list`, not here), computed as of Date.
type TodayView struct {
	Date      string         `json:"date"`
	DueTodos  []Todo         `json:"due_todos"`
	Checkoffs []CheckoffView `json:"checkoffs"`
}

// CanvasCourse is one Canvas course as tracked for import gating. A course
// the sync has never seen arrives as PendingConfirm; the user resolves it
// (include or exclude) once, and that decision persists in the DB so later
// syncs — manual, on-open, or ticker — never ask again.
type CanvasCourse struct {
	CourseID       int64  `json:"course_id"`
	Code           string `json:"code"`
	Name           string `json:"name"`
	Excluded       bool   `json:"excluded"`
	PendingConfirm bool   `json:"pending_confirm"`
	LastSeen       string `json:"last_seen,omitempty"`
}

// CanvasItem is one Canvas assignment mapped to local concepts, before the
// store decides insert vs. update vs. skip. Submittable is false when the
// assignment is reference material (`submission_types` only
// "none"/"not_graded"/empty, or `grading_type == "not_graded"`); Submitted
// follows the submission `workflow_state` (submitted, graded, or
// pending_review count as submitted).
type CanvasItem struct {
	AssignmentID int64
	CourseID     int64
	CourseCode   string
	Title        string
	DueAt        *time.Time
	URL          string
	UpdatedAt    string
	Submittable  bool
	Submitted    bool
}

// SyncDecisions resolves pending courses: course ID -> excluded.
// Absent entries leave the course as-is (still pending).
type SyncDecisions map[int64]bool

// SyncMode selects import strictness. Manual skips unconfirmed courses;
// auto imports them but keeps them pending for later confirmation.
type SyncMode string

const (
	SyncModeManual SyncMode = "manual"
	SyncModeAuto   SyncMode = "auto"
)

// SyncResult summarizes one Canvas sync for CLI display and API responses.
type SyncResult struct {
	Imported       int            `json:"imported"`
	Updated        int            `json:"updated"`
	Completed      int            `json:"completed"`
	Reopened       int            `json:"reopened"`
	Skipped        int            `json:"skipped"`
	DetachedTodos  int            `json:"detached_todos"`
	DetachedNotes  int            `json:"detached_notes"`
	PendingCourses []CanvasCourse `json:"pending_courses"`
}

// CanvasStatus is the GET /canvas/status probe: whether the server can talk
// to Canvas at all, and when it last did. The CLI uses it to decide if an
// on-open auto-sync is due; no import happens here.
type CanvasStatus struct {
	Configured bool       `json:"configured"`
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"`
}

// CourseResolveResult is the PATCH /canvas/courses/{id} response: the
// updated course plus how many imported items were converted to local when
// a course was excluded (zero when included).
type CourseResolveResult struct {
	Course        CanvasCourse `json:"course"`
	DetachedTodos int          `json:"detached_todos"`
	DetachedNotes int          `json:"detached_notes"`
}
