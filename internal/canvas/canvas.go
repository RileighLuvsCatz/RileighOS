package canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"rdb/rileighos/internal/models"
)

// defaultCanvasBaseURL is the production Canvas domain. Tests override it
// via RILEIGHOS_CANVAS_BASE_URL; the token always comes from
// RILEIGHOS_CANVAS_TOKEN and never appears in URLs, logs, or errors.
const defaultCanvasBaseURL = "https://uwmil.instructure.com"

// ErrCanvasUpstream marks failures talking to Canvas (network, auth, bad
// status, decode, pagination). Servers map it to 502 Bad Gateway; anything
// else surfacing from a sync is a store failure and keeps its own mapping.
var ErrCanvasUpstream = errors.New("canvas upstream")

// CanvasClient is a minimal read-only Canvas LMS client: active courses,
// then assignments per course with the current user's submission included.
// Everything is stdlib net/http — no new dependencies for one token header
// and two paginated list endpoints.
//
// The token travels in the Authorization header only. Error paths
// deliberately never echo it.
type CanvasClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewCanvasClient builds a client for baseURL (e.g. defaultCanvasBaseURL)
// authenticating as the owner of token.
func NewCanvasClient(baseURL, token string) *CanvasClient {
	return &CanvasClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// NewCanvasClientFromEnv builds the Canvas client from the environment. The
// token (RILEIGHOS_CANVAS_TOKEN) is required; the base URL
// (RILEIGHOS_CANVAS_BASE_URL) defaults to production. A missing token is a
// configuration error, not an upstream failure.
func NewCanvasClientFromEnv() (*CanvasClient, error) {
	token := os.Getenv("RILEIGHOS_CANVAS_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("canvas not configured: set RILEIGHOS_CANVAS_TOKEN")
	}
	base := os.Getenv("RILEIGHOS_CANVAS_BASE_URL")
	if base == "" {
		base = defaultCanvasBaseURL
	}
	return NewCanvasClient(base, token), nil
}

// CanvasCourseInfo is one active course as Canvas reports it.
type CanvasCourseInfo struct {
	ID    int64
	Code  string
	Name  string
	State string
}

// CanvasAssignmentInfo is one assignment plus the current user's submission
// (nil when Canvas returns none, which means unsubmitted).
type CanvasAssignmentInfo struct {
	ID              int64
	Name            string
	Published       bool
	SubmissionTypes []string
	GradingType     string
	DueAt           *time.Time
	URL             string
	UpdatedAt       string
	Submitted       bool
}

// canvasTime parses Canvas timestamps ("2006-01-02T15:04:05-07:00").
// Null or empty stays zero; DueAt is nil in that case.
type canvasTime struct {
	time.Time
}

// UnmarshalJSON implements json.Unmarshaler.
func (ct *canvasTime) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	ct.Time = t
	return nil
}

// MarshalJSON implements json.Marshaler, emitting the same string shape
// Canvas sends. Tests use this to serve realistic fake responses.
func (ct canvasTime) MarshalJSON() ([]byte, error) {
	if ct.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(ct.Format(time.RFC3339))
}

type canvasCourseJSON struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	CourseCode    string `json:"course_code"`
	WorkflowState string `json:"workflow_state"`
}

type canvasSubmissionJSON struct {
	WorkflowState string `json:"workflow_state"`
}

type canvasAssignmentJSON struct {
	ID              int64                 `json:"id"`
	Name            string                `json:"name"`
	Published       bool                  `json:"published"`
	SubmissionTypes []string              `json:"submission_types"`
	GradingType     string                `json:"grading_type"`
	DueAt           *canvasTime           `json:"due_at"`
	HTMLURL         string                `json:"html_url"`
	UpdatedAt       string                `json:"updated_at"`
	Submission      *canvasSubmissionJSON `json:"submission"`
}

// submittedStates are submission workflow_states that mean the student is
// done: submitted, graded, or awaiting review. Anything else (including a
// missing submission object) means open.
func submittedState(state string) bool {
	switch state {
	case "submitted", "graded", "pending_review":
		return true
	default:
		return false
	}
}

// submittable reports whether an assignment asks the student to submit
// anything. A "not_graded" anywhere means reference material, never work:
// either submission_types containing only "none"/"not_graded"/empty, or a
// grading_type of "not_graded". Every other type — online uploads,
// discussions, quizzes, on-paper work — is a todo.
func submittable(types []string, gradingType string) bool {
	if gradingType == "not_graded" {
		return false
	}
	for _, t := range types {
		if t != "" && t != "none" && t != "not_graded" {
			return true
		}
	}
	return false
}

// get fetches one Canvas API page, decoding out. The token is header-only;
// status errors name the endpoint, never the credential.
func (c *CanvasClient) get(path string, out any) (next string, err error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("canvas: build request: %v: %w", err, ErrCanvasUpstream)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("canvas: cannot reach %s: %v: %w", c.baseURL, err, ErrCanvasUpstream)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("canvas: token rejected (%d) — check RILEIGHOS_CANVAS_TOKEN: %w", resp.StatusCode, ErrCanvasUpstream)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("canvas: %s returned %d: %w", path, resp.StatusCode, ErrCanvasUpstream)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("canvas: read %s: %v: %w", path, err, ErrCanvasUpstream)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return "", fmt.Errorf("canvas: decode %s: %v: %w", path, err, ErrCanvasUpstream)
	}
	return nextPage(resp.Header.Get("Link")), nil
}

// nextPage extracts the rel="next" URL from a Canvas Link header, or ""
// when there are no more pages.
func nextPage(link string) string {
	for _, part := range strings.Split(link, ",") {
		seg := strings.Split(strings.TrimSpace(part), ";")
		if len(seg) != 2 {
			continue
		}
		if strings.TrimSpace(seg[1]) != `rel="next"` {
			continue
		}
		return strings.Trim(strings.TrimSpace(seg[0]), "<>")
	}
	return ""
}

// GetCourses lists active enrollments, following pagination. The returned
// infos carry upstream facts only; gating state (excluded/pending) lives
// in the Store, never here.
func (c *CanvasClient) GetCourses() ([]CanvasCourseInfo, error) {
	q := url.Values{}
	q.Set("enrollment_state", "active")
	q.Set("per_page", "100")
	var out []CanvasCourseInfo
	path := "/api/v1/courses?" + q.Encode()
	for pages := 0; path != ""; pages++ {
		if pages >= 50 {
			return nil, fmt.Errorf("canvas: courses pagination exceeded 50 pages: %w", ErrCanvasUpstream)
		}
		var raw []canvasCourseJSON
		next, err := c.get(path, &raw)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			out = append(out, CanvasCourseInfo{ID: r.ID, Code: r.CourseCode, Name: r.Name, State: r.WorkflowState})
		}
		path = ""
		if next != "" {
			// Canvas next URLs are absolute; strip back to a path so the
			// test fake (any host) works the same as production.
			if u, err := url.Parse(next); err == nil {
				path = u.RequestURI()
			}
		}
	}
	return out, nil
}

// GetAssignments lists one course's assignments with the current user's
// submission included, following pagination.
func (c *CanvasClient) GetAssignments(courseID int64) ([]CanvasAssignmentInfo, error) {
	q := url.Values{}
	q.Set("per_page", "100")
	q.Add("include[]", "submission")
	var out []CanvasAssignmentInfo
	path := fmt.Sprintf("/api/v1/courses/%d/assignments?%s", courseID, q.Encode())
	for pages := 0; path != ""; pages++ {
		if pages >= 50 {
			return nil, fmt.Errorf("canvas: assignments pagination exceeded 50 pages: %w", ErrCanvasUpstream)
		}
		var raw []canvasAssignmentJSON
		next, err := c.get(path, &raw)
		if err != nil {
			return nil, err
		}
		for _, r := range raw {
			info := CanvasAssignmentInfo{
				ID:              r.ID,
				Name:            r.Name,
				Published:       r.Published,
				SubmissionTypes: r.SubmissionTypes,
				GradingType:     r.GradingType,
				URL:             r.HTMLURL,
				UpdatedAt:       r.UpdatedAt,
			}
			if r.DueAt != nil && !r.DueAt.IsZero() {
				d := r.DueAt.Time
				info.DueAt = &d
			}
			if r.Submission != nil {
				info.Submitted = submittedState(r.Submission.WorkflowState)
			}
			out = append(out, info)
		}
		path = ""
		if next != "" {
			if u, err := url.Parse(next); err == nil {
				path = u.RequestURI()
			}
		}
	}
	return out, nil
}

// MapAssignment converts one assignment to a CanvasItem. The "[CODE] "
// title prefix is applied here so todos, notes, and today all render the
// course identically during the pre-tags interim.
func MapAssignment(course CanvasCourseInfo, a CanvasAssignmentInfo) models.CanvasItem {
	return models.CanvasItem{
		AssignmentID: a.ID,
		CourseID:     course.ID,
		CourseCode:   course.Code,
		Title:        "[" + course.Code + "] " + a.Name,
		DueAt:        a.DueAt,
		URL:          a.URL,
		UpdatedAt:    a.UpdatedAt,
		Submittable:  submittable(a.SubmissionTypes, a.GradingType),
		Submitted:    a.Submitted,
	}
}
