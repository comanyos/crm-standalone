package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Note is a first-class entity rather than an Activity kind (task.md §5.1).
//
// The reason is the same one that separates Task, arrived at from the other
// direction. An Activity is immutable because it records what happened, and
// a correction is a new Activity. A Note is a working document: someone
// writes down what they understand about an account, and then fixes it as
// their understanding improves. Forcing that through an immutable log
// produces a timeline of six near-identical notes where the reader wanted
// one, and no way to tell which is current.
//
// So a Note is editable, and EditedAt records that it was — which is the
// piece of the audit trail worth keeping once immutability is given up.
type Note struct {
	ID   string `json:"id"`
	Body string `json:"body"`

	// SubjectType and SubjectID are required. A note is always about
	// something; a note about nothing is a document, and this is not a
	// document store.
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`

	AuthorID  string    `json:"author_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// EditedAt is set the first time the body actually changes and left
	// alone by a write that changes nothing — a client that always sends the
	// whole record back must not make every save look like an edit.
	EditedAt time.Time `json:"edited_at"`
}

const maxNoteBodyLen = 50_000

// EntityID returns the record's identifier.
func (e *Note) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *Note) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *Note) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Created returns the record's creation time.
func (e *Note) Created() time.Time { return e.CreatedAt }

// Validate rejects a Note that cannot be stored.
func (e *Note) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no note", ErrInvalid)
	}
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Errorf("%w: Note.body is required", ErrInvalid)
	}
	if len(e.Body) > maxNoteBodyLen {
		return fmt.Errorf("%w: Note.body exceeds %d characters", ErrInvalid, maxNoteBodyLen)
	}
	if strings.TrimSpace(e.SubjectType) == "" || strings.TrimSpace(e.SubjectID) == "" {
		return fmt.Errorf("%w: Note.subject_type and Note.subject_id are required", ErrInvalid)
	}
	return nil
}

func cloneNote(v *Note) *Note {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// applyNoteEdit carries the immutable parts of a stored note across an
// update and records whether the body actually changed.
//
// Subject and author are not editable. Moving a note to a different record
// would rewrite history that other people have already read, and reassigning
// authorship is the one thing an audit trail must not permit — a note is
// evidence of who said what.
func applyNoteEdit(v *Note, existing *Note, now time.Time) {
	v.SubjectType = existing.SubjectType
	v.SubjectID = existing.SubjectID
	v.AuthorID = existing.AuthorID
	v.CreatedAt = existing.CreatedAt
	v.EditedAt = existing.EditedAt
	if v.Body != existing.Body {
		v.EditedAt = now
	}
}

// NoteFilter narrows ListNotes. Every field is optional.
type NoteFilter struct {
	SubjectType string
	SubjectID   string
	AuthorID    string
}

// Validate rejects a filter that cannot be applied.
func (f NoteFilter) Validate() error {
	if (f.SubjectType == "") != (f.SubjectID == "") {
		return fmt.Errorf("%w: subject_type and subject_id must be set together", ErrInvalid)
	}
	if f.SubjectType != "" && !knownSubjectTypes[f.SubjectType] {
		return fmt.Errorf("%w: %q is not a known subject type", ErrInvalid, f.SubjectType)
	}
	return nil
}

// matches reports whether a note satisfies the filter. Shared with MemStore
// so its behaviour cannot drift from PgStore's SQL.
func (f NoteFilter) matches(v *Note) bool {
	if f.SubjectType != "" && (v.SubjectType != f.SubjectType || v.SubjectID != f.SubjectID) {
		return false
	}
	if f.AuthorID != "" && v.AuthorID != f.AuthorID {
		return false
	}
	return true
}

// requireNoteSubject requires a note's subject to resolve inside the caller's
// tenant. Unlike a Task's, it is mandatory.
func requireNoteSubject(ctx context.Context, r subjectReader, scope Scope, subjectType, subjectID string) error {
	return requireSubject(ctx, r, scope, "Note", subjectType, subjectID)
}
