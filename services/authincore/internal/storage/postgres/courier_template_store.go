package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/authincore/authincore/internal/courier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CourierTemplateStore implements courier.TemplateLoader from the DB.
type CourierTemplateStore struct{ db *pgxpool.Pool }

func NewCourierTemplateStore(db *pgxpool.Pool) *CourierTemplateStore {
	return &CourierTemplateStore{db: db}
}

func (s *CourierTemplateStore) Load(ctx context.Context, orgID uuid.UUID, msgType courier.MessageType) (subject, body string, err error) {
	err = s.db.QueryRow(ctx,
		`SELECT subject, body FROM courier_templates
		 WHERE org_id=$1 AND type=$2`,
		orgID, string(msgType)).Scan(&subject, &body)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("no template for %s", msgType)
		}
		return "", "", fmt.Errorf("load courier template: %w", err)
	}
	return subject, body, nil
}
