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
// Canvas assignments with no submittable type (`submission_types: ["none"]`
// or empty) import as notes; they carry the same provenance fields as todos
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

// TodayView is the answer to "what does my day look like": open todos
// plus every check-off with its streak, computed as of Date.
type TodayView struct {
	Date      string         `json:"date"`
	OpenTodos []Todo         `json:"open_todos"`
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
// store decides insert vs. update vs. skip. Submittable follows Canvas
// `submission_types` (only "none"/empty means not submittable); Submitted
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

// SyncResult summarizes one Canvas sync for CLI display and API responses.
type SyncResult struct {
	Imported       int            `json:"imported"`
	Updated        int            `json:"updated"`
	Completed      int            `json:"completed"`
	Reopened       int            `json:"reopened"`
	Skipped        int            `json:"skipped"`
	PendingCourses []CanvasCourse `json:"pending_courses"`
}
