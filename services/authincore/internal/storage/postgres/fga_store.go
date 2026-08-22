package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/authincore/authincore/internal/fga"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FGAStore implements fga.Store against the relation_tuples table.
type FGAStore struct{ db *pgxpool.Pool }

func NewFGAStore(db *pgxpool.Pool) *FGAStore { return &FGAStore{db: db} }

func (s *FGAStore) Write(ctx context.Context, orgID uuid.UUID, t fga.Tuple) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO relation_tuples
		  (org_id, object_type, object_id, relation, subject_type, subject_id, subject_relation)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT DO NOTHING`,
		orgID, t.ObjectType, t.ObjectID, t.Relation,
		t.SubjectType, t.SubjectID, t.SubjectRelation,
	)
	if err != nil {
		return fmt.Errorf("fga write: %w", err)
	}
	return nil
}

func (s *FGAStore) Delete(ctx context.Context, orgID uuid.UUID, t fga.Tuple) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM relation_tuples
		WHERE org_id=$1
		  AND object_type=$2 AND object_id=$3
		  AND relation=$4
		  AND subject_type=$5 AND subject_id=$6
		  AND subject_relation=$7`,
		orgID, t.ObjectType, t.ObjectID, t.Relation,
		t.SubjectType, t.SubjectID, t.SubjectRelation,
	)
	return err
}

func (s *FGAStore) DirectTuples(ctx context.Context, orgID uuid.UUID,
	objectType, objectID, relation, subjectType, subjectID string,
) ([]fga.Tuple, error) {
	// Build query dynamically — only filter on non-empty fields.
	clauses := []string{"org_id = $1"}
	args := []any{orgID}
	n := 2

	add := func(col, val string) {
		if val != "" {
			clauses = append(clauses, fmt.Sprintf("%s = $%d", col, n))
			args = append(args, val)
			n++
		}
	}
	add("object_type", objectType)
	add("object_id", objectID)
	add("relation", relation)
	add("subject_type", subjectType)
	add("subject_id", subjectID)

	rows, err := s.db.Query(ctx,
		`SELECT object_type, object_id, relation, subject_type, subject_id, subject_relation
		 FROM relation_tuples WHERE `+strings.Join(clauses, " AND "),
		args...)
	if err != nil {
		return nil, fmt.Errorf("fga query: %w", err)
	}
	defer rows.Close()

	var tuples []fga.Tuple
	for rows.Next() {
		var t fga.Tuple
		if err := rows.Scan(&t.ObjectType, &t.ObjectID, &t.Relation,
			&t.SubjectType, &t.SubjectID, &t.SubjectRelation); err != nil {
			return nil, err
		}
		tuples = append(tuples, t)
	}
	return tuples, rows.Err()
}

func (s *FGAStore) DeleteObject(ctx context.Context, orgID uuid.UUID, objectType, objectID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM relation_tuples WHERE org_id=$1 AND object_type=$2 AND object_id=$3`,
		orgID, objectType, objectID)
	return err
}

func (s *FGAStore) DeleteSubject(ctx context.Context, orgID uuid.UUID, subjectType, subjectID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM relation_tuples WHERE org_id=$1 AND subject_type=$2 AND subject_id=$3`,
		orgID, subjectType, subjectID)
	return err
}
