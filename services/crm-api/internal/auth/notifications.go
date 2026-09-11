package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AlertRecipientIDs resolves active members for operational CRM alerts. A new
// assigned lead goes to its owner; unassigned leads fall back to workspace
// admins/managers. Escalations include both the owner and those supervisors.
func (a *Authenticator) AlertRecipientIDs(
	ctx context.Context, organizationID, ownerID string, escalate bool,
) ([]string, error) {
	ownerID = strings.TrimSpace(ownerID)
	if a == nil {
		return nil, errors.New("auth: authenticator is required")
	}
	if a.disabled {
		if ownerID != "" {
			return []string{ownerID}, nil
		}
		return []string{devPrincipal.Subject}, nil
	}
	if a.service == nil {
		return nil, errors.New("auth: account directory unavailable")
	}
	repo, ok := a.service.repo.(interface {
		AlertRecipientIDs(context.Context, string, string, bool) ([]string, error)
	})
	if !ok {
		return nil, errors.New("auth: account directory does not support alerts")
	}
	return repo.AlertRecipientIDs(ctx, organizationID, ownerID, escalate)
}

// SendUserEmail delivers an operational message to an active user's verified
// account address through the same configured SMTP channel as login codes.
func (a *Authenticator) SendUserEmail(
	ctx context.Context, userID, subject, body string,
) error {
	if a == nil || a.disabled || a.service == nil || a.service.mailer == nil {
		return ErrLoginUnavailable
	}
	repo, ok := a.service.repo.(interface {
		UserEmailByID(context.Context, string) (string, error)
	})
	if !ok {
		return errors.New("auth: account directory does not support email lookup")
	}
	email, err := repo.UserEmailByID(ctx, userID)
	if err != nil {
		return err
	}
	return a.service.mailer.Send(ctx, email, subject, body)
}

// UserEmailByID returns an active user's address. It is intentionally not
// exported through the CRM API; only the notification sender consumes it.
func (r *postgresRepository) UserEmailByID(ctx context.Context, userID string) (string, error) {
	var email string
	err := r.pool.QueryRow(ctx, `
		SELECT email FROM auth_users WHERE id=$1 AND active`, userID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoAccount
	}
	if err != nil {
		return "", fmt.Errorf("auth: load alert recipient: %w", err)
	}
	return email, nil
}

func (r *postgresRepository) AlertRecipientIDs(
	ctx context.Context, organizationID, ownerID string, escalate bool,
) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT m.user_id
		  FROM auth_memberships m
		  JOIN auth_users u ON u.id=m.user_id AND u.active
		  JOIN auth_organizations o ON o.id=m.organization_id AND o.active
		 WHERE m.organization_id=$1 AND m.active
		   AND (
		       (m.user_id=$2 AND $2 <> '')
		       OR (($3 OR $2 = '') AND m.role_key IN ('crm_admin','crm_manager'))
		   )
		 ORDER BY m.user_id`, organizationID, ownerID, escalate)
	if err != nil {
		return nil, fmt.Errorf("auth: list alert recipients: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("auth: scan alert recipient: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list alert recipients: %w", err)
	}
	// A stale owner id must not make a new lead disappear. Fall back to the
	// same supervisor set used for an unassigned lead.
	if len(out) == 0 && ownerID != "" && !escalate {
		return r.AlertRecipientIDs(ctx, organizationID, "", false)
	}
	return out, nil
}
