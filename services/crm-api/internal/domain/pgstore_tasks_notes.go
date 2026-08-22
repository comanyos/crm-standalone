package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// PgStore's half of Task and Note (task.md §5.1).
//
// The ordering here has to reproduce taskLess in memstore_tasks_notes.go
// exactly, including the rule that an undated task sorts last. Two stores
// that disagree about order produce a pagination bug that only appears
// against Postgres, which is the environment where it is hardest to see.

const taskColumns = `id, title, description, status, priority, assignee_id, due_at,
	subject_type, subject_id, completed_at, completed_by, created_by, created_at, updated_at`

func scanTask(row pgx.Row) (*Task, error) {
	v := &Task{}
	var dueAt, completedAt *time.Time
	err := row.Scan(&v.ID, &v.Title, &v.Description, &v.Status, &v.Priority, &v.AssigneeID,
		&dueAt, &v.SubjectType, &v.SubjectID, &completedAt, &v.CompletedBy,
		&v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if dueAt != nil {
		v.DueAt = *dueAt
	}
	if completedAt != nil {
		v.CompletedAt = *completedAt
	}
	return v, nil
}

// nullTimeValue sends NULL for the zero time. due_at and completed_at are
// genuinely absent rather than "year one", and the table's CHECK constraints
// are written against NULL.
func nullTimeValue(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// CreateTask stores a new Task for the scope's tenant.
func (s *PgStore) CreateTask(ctx context.Context, scope Scope, v *Task) (*Task, error) {
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
	now := time.Now().UTC()
	if v.Status == TaskCompleted {
		applyTaskCompletion(v, v.CreatedBy, now)
	}
	if v.ID == "" {
		v.ID = newID()
	}
	v.CreatedAt, v.UpdatedAt = now, now

	_, err := s.db.Exec(ctx, scope,
		`INSERT INTO tasks (tenant_id, id, title, description, status, priority, assignee_id,
		     due_at, subject_type, subject_id, completed_at, completed_by, created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		scope.TenantID, v.ID, v.Title, v.Description, v.Status, v.Priority, v.AssigneeID,
		nullTimeValue(v.DueAt), v.SubjectType, v.SubjectID,
		nullTimeValue(v.CompletedAt), v.CompletedBy, v.CreatedBy, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	return cloneTask(v), nil
}

// GetTask reads one Task belonging to the scope's tenant.
func (s *PgStore) GetTask(ctx context.Context, scope Scope, id string) (*Task, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	return scanTask(s.db.QueryRow(ctx, scope,
		`SELECT `+taskColumns+` FROM tasks WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id))
}

// ListTasks returns one page of Tasks matching the filter.
func (s *PgStore) ListTasks(ctx context.Context, scope Scope, page Page, filter TaskFilter, sortBy TaskSort) ([]*Task, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	if err := filter.Validate(); err != nil {
		return nil, "", err
	}

	where := []string{"tenant_id = $1"}
	args := []any{scope.TenantID}
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if filter.AssigneeID != "" {
		add("assignee_id = $%d", filter.AssigneeID)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if filter.SubjectType != "" {
		add("subject_type = $%d", filter.SubjectType)
		add("subject_id = $%d", filter.SubjectID)
	}
	if filter.OpenOnly {
		where = append(where, "status IN ('open', 'in_progress')")
	}
	if !filter.DueBefore.IsZero() {
		// due_at IS NOT NULL is implied by the comparison, but stated so the
		// planner can use the partial index and so the intent survives a
		// later edit: an undated task is never overdue.
		add("(due_at IS NOT NULL AND due_at < $%d)", filter.DueBefore)
	}

	// Keyset pagination. The ORDER BY and the cursor predicate have to be the
	// same shape, or a page boundary silently skips or repeats rows.
	orderBy := "due_at ASC NULLS LAST, id ASC"
	if sortBy == SortTaskCreatedDesc {
		orderBy = "created_at DESC, id DESC"
	}
	if page.Token != "" {
		sortValue, afterID, ok := decodeCursor(page.Token)
		if !ok {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		if sortBy == SortTaskCreatedDesc {
			after, err := time.Parse(time.RFC3339Nano, sortValue)
			if err != nil {
				return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
			}
			args = append(args, after, afterID)
			where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
		} else if sortValue == "1" {
			// Past every dated task: only undated ones remain, ordered by id.
			args = append(args, afterID)
			where = append(where, fmt.Sprintf("(due_at IS NULL AND id > $%d)", len(args)))
		} else {
			after, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(sortValue, "0"))
			if err != nil {
				return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
			}
			args = append(args, after, afterID)
			where = append(where, fmt.Sprintf(
				"(due_at IS NULL OR (due_at, id) > ($%d, $%d))", len(args)-1, len(args)))
		}
	}

	args = append(args, page.size()+1)
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+taskColumns+` FROM tasks WHERE `+strings.Join(where, " AND ")+
			` ORDER BY `+orderBy+fmt.Sprintf(" LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, "", fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	out := make([]*Task, 0, page.size())
	for rows.Next() {
		v, err := scanTask(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	next := ""
	if len(out) > page.size() {
		out = out[:page.size()]
		last := out[len(out)-1]
		next = encodeCursor(taskSortValue(last, sortBy), last.ID)
	}
	return out, next, nil
}

// UpdateTask replaces an existing Task, keeping completion metadata under
// the store's control.
func (s *PgStore) UpdateTask(ctx context.Context, scope Scope, v *Task) (*Task, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.GetTask(ctx, scope, v.ID)
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
	now := time.Now().UTC()
	switch {
	case v.Status == TaskCompleted:
		applyTaskCompletion(v, existing.CompletedBy, now)
	case existing.Status == TaskCompleted:
		v.CompletedAt = time.Time{}
		v.CompletedBy = ""
	}
	v.UpdatedAt = now

	tag, err := s.db.Exec(ctx, scope,
		`UPDATE tasks SET title=$3, description=$4, status=$5, priority=$6, assignee_id=$7,
		     due_at=$8, subject_type=$9, subject_id=$10, completed_at=$11, completed_by=$12,
		     updated_at=$13
		 WHERE tenant_id=$1 AND id=$2`,
		scope.TenantID, v.ID, v.Title, v.Description, v.Status, v.Priority, v.AssigneeID,
		nullTimeValue(v.DueAt), v.SubjectType, v.SubjectID,
		nullTimeValue(v.CompletedAt), v.CompletedBy, v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetTask(ctx, scope, v.ID)
}

// DeleteTask removes a Task belonging to the scope's tenant.
func (s *PgStore) DeleteTask(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope, `DELETE FROM tasks WHERE tenant_id=$1 AND id=$2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CompleteTask marks a Task done, idempotently.
func (s *PgStore) CompleteTask(ctx context.Context, scope Scope, id, actor string) (*Task, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	existing, err := s.GetTask(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	if existing.Status == TaskCompleted {
		return existing, nil
	}
	now := time.Now().UTC()
	tag, err := s.db.Exec(ctx, scope,
		`UPDATE tasks SET status='completed', completed_at=$3, completed_by=$4, updated_at=$3
		 WHERE tenant_id=$1 AND id=$2`,
		scope.TenantID, id, now, actor)
	if err != nil {
		return nil, fmt.Errorf("complete task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetTask(ctx, scope, id)
}

// ReopenTask returns a Task to open and clears its completion record.
func (s *PgStore) ReopenTask(ctx context.Context, scope Scope, id string) (*Task, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	existing, err := s.GetTask(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	if existing.Status == TaskOpen {
		return existing, nil
	}
	tag, err := s.db.Exec(ctx, scope,
		`UPDATE tasks SET status='open', completed_at=NULL, completed_by='', updated_at=$3
		 WHERE tenant_id=$1 AND id=$2`,
		scope.TenantID, id, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("reopen task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetTask(ctx, scope, id)
}

const noteColumns = `id, body, subject_type, subject_id, author_id, created_at, updated_at, edited_at`

func scanNote(row pgx.Row) (*Note, error) {
	v := &Note{}
	var editedAt *time.Time
	err := row.Scan(&v.ID, &v.Body, &v.SubjectType, &v.SubjectID, &v.AuthorID,
		&v.CreatedAt, &v.UpdatedAt, &editedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if editedAt != nil {
		v.EditedAt = *editedAt
	}
	return v, nil
}

// CreateNote stores a new Note for the scope's tenant.
func (s *PgStore) CreateNote(ctx context.Context, scope Scope, v *Note) (*Note, error) {
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
	if v.ID == "" {
		v.ID = newID()
	}
	now := time.Now().UTC()
	v.CreatedAt, v.UpdatedAt = now, now

	_, err := s.db.Exec(ctx, scope,
		`INSERT INTO notes (tenant_id, id, body, subject_type, subject_id, author_id, created_at, updated_at, edited_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULL)`,
		scope.TenantID, v.ID, v.Body, v.SubjectType, v.SubjectID, v.AuthorID, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create note: %w", err)
	}
	return cloneNote(v), nil
}

// GetNote reads one Note belonging to the scope's tenant.
func (s *PgStore) GetNote(ctx context.Context, scope Scope, id string) (*Note, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	return scanNote(s.db.QueryRow(ctx, scope,
		`SELECT `+noteColumns+` FROM notes WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id))
}

// ListNotes returns one page of Notes matching the filter, newest first.
func (s *PgStore) ListNotes(ctx context.Context, scope Scope, page Page, filter NoteFilter) ([]*Note, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	if err := filter.Validate(); err != nil {
		return nil, "", err
	}

	where := []string{"tenant_id = $1"}
	args := []any{scope.TenantID}
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if filter.SubjectType != "" {
		add("subject_type = $%d", filter.SubjectType)
		add("subject_id = $%d", filter.SubjectID)
	}
	if filter.AuthorID != "" {
		add("author_id = $%d", filter.AuthorID)
	}
	if page.Token != "" {
		sortValue, afterID, ok := decodeCursor(page.Token)
		if !ok {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		after, err := time.Parse(time.RFC3339Nano, sortValue)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		args = append(args, after, afterID)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}

	args = append(args, page.size()+1)
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+noteColumns+` FROM notes WHERE `+strings.Join(where, " AND ")+
			` ORDER BY created_at DESC, id DESC`+fmt.Sprintf(" LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, "", fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()

	out := make([]*Note, 0, page.size())
	for rows.Next() {
		v, err := scanNote(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	next := ""
	if len(out) > page.size() {
		out = out[:page.size()]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt.UTC().Format(time.RFC3339Nano), last.ID)
	}
	return out, next, nil
}

// UpdateNote edits a Note's body, carrying subject and author across.
func (s *PgStore) UpdateNote(ctx context.Context, scope Scope, v *Note) (*Note, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	existing, err := s.GetNote(ctx, scope, v.ID)
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Note %s was modified since it was last read", ErrConflict, v.ID)
	}
	now := time.Now().UTC()
	applyNoteEdit(v, existing, now)
	if err := v.Validate(); err != nil {
		return nil, err
	}
	v.UpdatedAt = now

	tag, err := s.db.Exec(ctx, scope,
		`UPDATE notes SET body=$3, updated_at=$4, edited_at=$5 WHERE tenant_id=$1 AND id=$2`,
		scope.TenantID, v.ID, v.Body, v.UpdatedAt, nullTimeValue(v.EditedAt))
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetNote(ctx, scope, v.ID)
}

// DeleteNote removes a Note belonging to the scope's tenant.
func (s *PgStore) DeleteNote(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope, `DELETE FROM notes WHERE tenant_id=$1 AND id=$2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
