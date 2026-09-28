package store

import (
	"fmt"
	"sort"

	"rdb/rileighos/internal/models"
)

// NormalizeWorkDate keeps stored work dates as calendar days. Only input
// accepts "today"; resolving it here uses the server's day boundary.
func NormalizeWorkDate(day string) (string, error) {
	if day == "today" {
		return Today(), nil
	}
	if !ValidDay(day) {
		return "", fmt.Errorf("invalid work date %q: want YYYY-MM-DD or today", day)
	}
	return day, nil
}

// groupTodayTodos is shared by both backends so the API's three sections
// always have the same, non-overlapping membership and order.
func groupTodayTodos(todos []models.Todo, today string) (overdue, planned, due []models.TodayTodo) {
	overdue = []models.TodayTodo{}
	planned = []models.TodayTodo{}
	due = []models.TodayTodo{}
	for _, t := range todos {
		if t.Done {
			continue
		}
		workDay := ""
		if t.WorkDate != nil {
			workDay = *t.WorkDate
		}
		deadlineDay := ""
		if t.DueAt != nil {
			deadlineDay = dueDay(t.DueAt)
		}
		item := models.TodayTodo{
			Todo:            t,
			DueDay:          deadlineDay,
			OverdueWorkDate: workDay != "" && workDay < today,
			OverdueDueDate:  deadlineDay != "" && deadlineDay < today,
		}
		switch {
		case item.OverdueWorkDate || item.OverdueDueDate:
			overdue = append(overdue, item)
		case deadlineDay == today:
			due = append(due, item)
		case workDay == today:
			planned = append(planned, item)
		}
	}
	// Missed deadlines come first, then missed work plans. Within each
	// group, the oldest relevant date comes first; IDs settle ties.
	sort.Slice(overdue, func(i, j int) bool {
		a, b := overdue[i], overdue[j]
		if a.OverdueDueDate != b.OverdueDueDate {
			return a.OverdueDueDate
		}
		day := func(x models.TodayTodo) string {
			if x.OverdueDueDate {
				return dueDay(x.DueAt)
			}
			return *x.WorkDate
		}
		if day(a) != day(b) {
			return day(a) < day(b)
		}
		return a.ID < b.ID
	})
	sort.Slice(planned, func(i, j int) bool { return planned[i].ID < planned[j].ID })
	sort.Slice(due, func(i, j int) bool {
		if !due[i].DueAt.Equal(*due[j].DueAt) {
			return due[i].DueAt.Before(*due[j].DueAt)
		}
		return due[i].ID < due[j].ID
	})
	return
}
