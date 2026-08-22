package domain

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// MemStore's half of Task and Note (task.md §5.1).
//
// Kept in its own file rather than appended to memstore.go: the ordering and
// cursor rules below are the ones PgStore's SQL has to agree with, and having
// them in one place makes the pair reviewable together.

// taskLess is the in-memory ordering, and it must match the ORDER BY in
// PgStore. Undated tasks sort last under the default order — a task with no
// due date is not urgent, and treating a zero time as "long ago" would put
// every undated task at the top of a work list.
func taskLess(sortBy TaskSort) func(a, b *Task) bool {
	if sortBy == SortTaskCreatedDesc {
		return func(a, b *Task) bool {
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.ID > b.ID
		}
	}
	return func(a, b *Task) bool {
		if a.DueAt.IsZero() != b.DueAt.IsZero() {
			return !a.DueAt.IsZero()
		}
		if !a.DueAt.Equal(b.DueAt) {
			return a.DueAt.Before(b.DueAt)
		}
		return a.ID < b.ID
	}
}

// taskSortValue is the cursor's leading component, encoded so that string
// comparison reproduces taskLess. The "0"/"1" prefix carries the
// dated-before-undated rule that a bare timestamp cannot express.
func taskSortValue(v *Task, sortBy TaskSort) string {
	if sortBy == SortTaskCreatedDesc {
		return v.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if v.DueAt.IsZero() {
		return "1"
	}
	return "0" + v.DueAt.UTC().Format(time.RFC3339Nano)
}

// taskCursorBoundary rebuilds the record a page token points just past.
func taskCursorBoundary(sortValue, id string, sortBy TaskSort) *Task {
	boundary := &Task{ID: id}
	if sortBy == SortTaskCreatedDesc {
		if t, err := time.Parse(time.RFC3339Nano, sortValue); err == nil {
			boundary.CreatedAt = t
		}
		return boundary
	}
	if len(sortValue) > 1 && sortValue[0] == '0' {
		if t, err := time.Parse(time.RFC3339Nano, sortValue[1:]); err == nil {
			boundary.DueAt = t
		}
	}
	return boundary
}

// CreateTask stores a new Task. A subject, if given, must resolve in this
// tenant; completion metadata is cleared, never accepted from the caller.
func (s *MemStore) CreateTask(ctx context.Context, scope Scope, v *Task) (*Task, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := requireTaskSubject(ctx, s, scope, v.SubjectType, v.SubjectID); err != nil {
		return nil, err
	}
	sanitizeTaskLifecycle(v, nil)
	// A task created directly as completed is legitimate — logging work
	// already done — so stamp it rather than rejecting it.
	if v.Status == TaskCompleted {
		applyTaskCompletion(v, v.CreatedBy, nowStamp())
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "task", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	return s.task.create(scope, v)
}

// GetTask returns one Task belonging to the scope's tenant.
func (s *MemStore) GetTask(_ context.Context, scope Scope, id string) (*Task, error) {
	return s.task.get(scope, id)
}

// ListTasks returns one page of Tasks matching the filter.
func (s *MemStore) ListTasks(_ context.Context, scope Scope, page Page, filter TaskFilter, sortBy TaskSort) ([]*Task, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	if err := filter.Validate(); err != nil {
		return nil, "", err
	}
	all, err := s.task.all(scope)
	if err != nil {
		return nil, "", err
	}

	less := taskLess(sortBy)
	rows := make([]*Task, 0, len(all))
	for _, t := range all {
		if filter.matches(t) {
			rows = append(rows, t)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })

	start := 0
	if page.Token != "" {
		sortValue, afterID, ok := decodeCursor(page.Token)
		if !ok {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		boundary := taskCursorBoundary(sortValue, afterID, sortBy)
		start = sort.Search(len(rows), func(i int) bool { return less(boundary, rows[i]) })
	}
	rows = rows[start:]

	limit := page.size()
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(taskSortValue(last, sortBy), last.ID)
	}
	return rows, next, nil
}

// UpdateTask replaces an existing Task, preserving the completion metadata
// the store owns. Setting status to completed through this path stamps the
// completion the same way CompleteTask does, so the two cannot disagree.
func (s *MemStore) UpdateTask(ctx context.Context, scope Scope, v *Task) (*Task, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.task.get(scope, v.ID)
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Task %s was modified since it was last read", ErrConflict, v.ID)
	}
	if err := requireTaskSubject(ctx, s, scope, v.SubjectType, v.SubjectID); err != nil {
		return nil, err
	}
	sanitizeTaskLifecycle(v, existing)
	v.CreatedBy = existing.CreatedBy
	now := nowStamp()
	switch {
	case v.Status == TaskCompleted:
		applyTaskCompletion(v, existing.CompletedBy, now)
	case existing.Status == TaskCompleted:
		// Moved out of completed by an ordinary edit: the completion record
		// has to go with it, or the task reports as finished while sitting
		// in an open list.
		v.CompletedAt = time.Time{}
		v.CompletedBy = ""
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "task", v.CustomFields, existing.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	return s.task.update(scope, v)
}

// DeleteTask removes a Task.
func (s *MemStore) DeleteTask(_ context.Context, scope Scope, id string) error {
	return s.task.delete(scope, id)
}

// CompleteTask marks a Task done. Idempotent: completing a completed task
// returns it unchanged rather than restamping who finished it.
func (s *MemStore) CompleteTask(_ context.Context, scope Scope, id, actor string) (*Task, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	existing, err := s.task.get(scope, id)
	if err != nil {
		return nil, err
	}
	if existing.Status == TaskCompleted {
		return existing, nil
	}
	updated := cloneTask(existing)
	applyTaskCompletion(updated, actor, nowStamp())
	return s.task.update(scope, updated)
}

// ReopenTask returns a Task to open and clears its completion record.
// Idempotent for a task that is already open.
func (s *MemStore) ReopenTask(_ context.Context, scope Scope, id string) (*Task, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	existing, err := s.task.get(scope, id)
	if err != nil {
		return nil, err
	}
	if existing.Status == TaskOpen {
		return existing, nil
	}
	updated := cloneTask(existing)
	applyTaskReopen(updated)
	return s.task.update(scope, updated)
}

// CreateNote stores a new Note. Its subject is mandatory and must resolve in
// this tenant.
func (s *MemStore) CreateNote(ctx context.Context, scope Scope, v *Note) (*Note, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := requireNoteSubject(ctx, s, scope, v.SubjectType, v.SubjectID); err != nil {
		return nil, err
	}
	v.EditedAt = time.Time{}
	custom, err := CoerceCustomValues(ctx, s, scope, "note", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	return s.note.create(scope, v)
}

// GetNote returns one Note belonging to the scope's tenant.
func (s *MemStore) GetNote(_ context.Context, scope Scope, id string) (*Note, error) {
	return s.note.get(scope, id)
}

// ListNotes returns one page of Notes matching the filter, newest first.
func (s *MemStore) ListNotes(_ context.Context, scope Scope, page Page, filter NoteFilter) ([]*Note, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	if err := filter.Validate(); err != nil {
		return nil, "", err
	}
	all, err := s.note.all(scope)
	if err != nil {
		return nil, "", err
	}

	rows := make([]*Note, 0, len(all))
	for _, n := range all {
		if filter.matches(n) {
			rows = append(rows, n)
		}
	}
	less := func(a, b *Note) bool {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	}
	sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })

	start := 0
	if page.Token != "" {
		sortValue, afterID, ok := decodeCursor(page.Token)
		if !ok {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		boundary := &Note{ID: afterID}
		if t, err := time.Parse(time.RFC3339Nano, sortValue); err == nil {
			boundary.CreatedAt = t
		}
		start = sort.Search(len(rows), func(i int) bool { return less(boundary, rows[i]) })
	}
	rows = rows[start:]

	limit := page.size()
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(last.CreatedAt.UTC().Format(time.RFC3339Nano), last.ID)
	}
	return rows, next, nil
}

// UpdateNote edits a Note's body. Subject and author are carried over from
// the stored record — see applyNoteEdit for why neither may move.
func (s *MemStore) UpdateNote(ctx context.Context, scope Scope, v *Note) (*Note, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	existing, err := s.note.get(scope, v.ID)
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Note %s was modified since it was last read", ErrConflict, v.ID)
	}
	applyNoteEdit(v, existing, nowStamp())
	if err := v.Validate(); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "note", v.CustomFields, existing.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	return s.note.update(scope, v)
}

// DeleteNote removes a Note.
func (s *MemStore) DeleteNote(_ context.Context, scope Scope, id string) error {
	return s.note.delete(scope, id)
}
