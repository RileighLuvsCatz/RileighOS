package canvas

import (
	"errors"
	"fmt"
	"time"

	"rdb/rileighos/internal/models"
	"rdb/rileighos/internal/store"
)

// RunCanvasSync is the single import path behind every trigger (manual
// command, on-open, ticker). It is idempotent: re-running with no upstream
// changes imports nothing, and it never deletes user data — assignments
// that vanish upstream are simply left alone.
//
// In manual mode unconfirmed courses are skipped and reported pending; in
// auto mode they import anyway but stay pending for later confirmation.
// Excluding a course (via decisions) additionally detaches its already
// imported items to plain local todos/notes.
//
// Flow: refresh the course list (new courses arrive pending_confirm),
// apply decisions, then import published assignments for included courses.
// Submitted-but-never-imported assignments are skipped (done history, not
// work); submitted state on an already-imported todo flips Done, and
// unsubmit reopens it (mirror).
func RunCanvasSync(s store.FullStore, cv *CanvasClient, mode models.SyncMode, decisions models.SyncDecisions) (models.SyncResult, error) {
	var res models.SyncResult

	courses, err := cv.GetCourses()
	if err != nil {
		return res, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, info := range courses {
		if _, err := s.UpsertCanvasCourse(models.CanvasCourse{
			CourseID: info.ID, Code: info.Code, Name: info.Name,
			Excluded: false, PendingConfirm: true, LastSeen: now,
		}); err != nil {
			return res, err
		}
		if excluded, ok := decisions[info.ID]; ok {
			if err := s.ResolveCanvasCourse(info.ID, excluded); err != nil {
				return res, err
			}
			if excluded {
				todos, notes, err := s.DetachCanvasCourse(info.ID)
				if err != nil {
					return res, err
				}
				res.DetachedTodos += todos
				res.DetachedNotes += notes
			}
		}
	}

	tracked, err := s.GetCanvasCourses()
	if err != nil {
		return res, err
	}
	seen := make(map[int64]bool, len(courses))
	for _, info := range courses {
		seen[info.ID] = true
	}
	for _, course := range tracked {
		if course.PendingConfirm {
			res.PendingCourses = append(res.PendingCourses, course)
			if mode == models.SyncModeManual {
				continue
			}
			// Auto mode imports unconfirmed courses but leaves them
			// pending, so the next manual sync still asks.
		}
		if course.Excluded {
			continue
		}
		if !seen[course.CourseID] {
			// Dropped upstream since last sync: keep local items, import nothing.
			continue
		}
		assignments, err := cv.GetAssignments(course.CourseID)
		if err != nil {
			return res, fmt.Errorf("canvas: assignments for %s: %w", course.Code, err)
		}
		for _, a := range assignments {
			if !a.Published {
				res.Skipped++
				continue
			}
			item := MapAssignment(CanvasCourseInfo{ID: course.CourseID, Code: course.Code, Name: course.Name}, a)
			if err := importItem(s, item, &res); err != nil {
				return res, err
			}
		}
	}

	if err := s.SetLastCanvasSync(time.Now().UTC()); err != nil {
		return res, err
	}
	return res, nil
}

// importItem routes one mapped assignment to the note or todo path,
// migrating across types when a professor flips submission_types.
// Dismissed assignment IDs are skipped first (counted as Skipped) and
// never import, even when no local copy exists.
func importItem(s store.FullStore, item models.CanvasItem, res *models.SyncResult) error {
	dismissed, err := s.IsCanvasDismissed(item.AssignmentID)
	if err != nil {
		return err
	}
	if dismissed {
		res.Skipped++
		return nil
	}
	todo, todoErr := s.GetTodoByCanvasID(item.AssignmentID)
	if todoErr != nil && !errors.Is(todoErr, store.ErrNotFound) {
		return todoErr
	}
	note, noteErr := s.GetNoteByCanvasID(item.AssignmentID)
	if noteErr != nil && !errors.Is(noteErr, store.ErrNotFound) {
		return noteErr
	}
	if item.Submittable {
		if noteErr == nil {
			// Was a note, now submittable: migrate across types.
			if err := s.DeleteNote(note.ID); err != nil {
				return err
			}
		}
		return importTodo(s, item, res, todo, todoErr == nil)
	}
	if todoErr == nil {
		// Was a todo, now reference-only: migrate across types.
		if err := s.DeleteTodo(todo.ID); err != nil {
			return err
		}
	}
	return importNote(s, item, res, note, noteErr == nil)
}

func importTodo(s store.FullStore, item models.CanvasItem, res *models.SyncResult, existing models.Todo, found bool) error {
	if !found {
		if item.Submitted {
			// Submitted before first sight: history, not work. Skip.
			res.Skipped++
			return nil
		}
		if _, err := s.AddCanvasTodo(item.Title, item.DueAt, item.AssignmentID, item.CourseCode, item.URL, item.UpdatedAt); err != nil {
			return err
		}
		res.Imported++
		return nil
	}
	if todoChanged(existing, item) {
		if err := s.UpdateCanvasTodo(existing.ID, item.Title, item.DueAt, item.CourseCode, item.URL, item.UpdatedAt); err != nil {
			return err
		}
		res.Updated++
	}
	switch {
	case item.Submitted && !existing.Done:
		if err := s.MarkTodoDone(existing.ID); err != nil {
			return err
		}
		res.Completed++
	case !item.Submitted && existing.Done:
		if err := s.MarkTodoUndone(existing.ID); err != nil {
			return err
		}
		res.Reopened++
	}
	return nil
}

func importNote(s store.FullStore, item models.CanvasItem, res *models.SyncResult, existing models.Note, found bool) error {
	if !found {
		if _, err := s.AddCanvasNote(item.Title, item.DueAt, item.AssignmentID, item.CourseCode, item.URL, item.UpdatedAt); err != nil {
			return err
		}
		res.Imported++
		return nil
	}
	if noteChanged(existing, item) {
		if err := s.UpdateCanvasNote(existing.ID, item.Title, item.DueAt, item.CourseCode, item.URL, item.UpdatedAt); err != nil {
			return err
		}
		res.Updated++
	}
	return nil
}

func dueEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func todoChanged(t models.Todo, item models.CanvasItem) bool {
	return t.Content != item.Title ||
		!dueEqual(t.DueAt, item.DueAt) ||
		t.CanvasCourseCode != item.CourseCode ||
		t.CanvasURL != item.URL ||
		t.CanvasUpdatedAt != item.UpdatedAt
}

func noteChanged(n models.Note, item models.CanvasItem) bool {
	return n.Content != item.Title ||
		!dueEqual(n.DueAt, item.DueAt) ||
		n.CanvasCourseCode != item.CourseCode ||
		n.CanvasURL != item.URL ||
		n.CanvasUpdatedAt != item.UpdatedAt
}
