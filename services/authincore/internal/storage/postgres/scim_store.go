package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/scim"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// SCIMUserStore implements scim.UserStore against the scim_users table.
type SCIMUserStore struct{ db *pgxpool.Pool }

func NewSCIMUserStore(db *pgxpool.Pool) *SCIMUserStore { return &SCIMUserStore{db: db} }

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *SCIMUserStore) Create(ctx context.Context, orgID uuid.UUID, u *scim.User) (*scim.User, error) {
	u.ID = uuid.New().String()
	emails, _ := json.Marshal(u.Emails)
	raw, _ := json.Marshal(u)
	now := time.Now().UTC()
	_, err := s.db.Exec(ctx,
		`INSERT INTO scim_users
		   (id, org_id, external_id, username, display_name, emails, active, raw, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`,
		u.ID, orgID, optionalString(u.ExternalID), u.UserName, u.DisplayName, emails, u.Active, raw, now)
	if err != nil {
		return nil, fmt.Errorf("scim create user: %w", err)
	}
	return u, nil
}

func (s *SCIMUserStore) ByID(ctx context.Context, orgID uuid.UUID, id string) (*scim.User, error) {
	return s.scanUser(s.db.QueryRow(ctx,
		`SELECT id, COALESCE(external_id, ''), username, display_name, emails, active
		 FROM scim_users WHERE org_id=$1 AND id=$2`, orgID, id))
}

func (s *SCIMUserStore) ByExternalID(ctx context.Context, orgID uuid.UUID, externalID string) (*scim.User, error) {
	return s.scanUser(s.db.QueryRow(ctx,
		`SELECT id, COALESCE(external_id, ''), username, display_name, emails, active
		 FROM scim_users WHERE org_id=$1 AND external_id=$2`, orgID, externalID))
}

func (s *SCIMUserStore) ByUserName(ctx context.Context, orgID uuid.UUID, userName string) (*scim.User, error) {
	return s.scanUser(s.db.QueryRow(ctx,
		`SELECT id, COALESCE(external_id, ''), username, display_name, emails, active
		 FROM scim_users WHERE org_id=$1 AND username=$2`, orgID, userName))
}

func (s *SCIMUserStore) Replace(ctx context.Context, orgID uuid.UUID, u *scim.User) (*scim.User, error) {
	emails, _ := json.Marshal(u.Emails)
	raw, _ := json.Marshal(u)
	_, err := s.db.Exec(ctx,
		`UPDATE scim_users
		 SET username=$3, display_name=$4, emails=$5, active=$6, raw=$7, updated_at=now()
		 WHERE org_id=$1 AND id=$2`,
		orgID, u.ID, u.UserName, u.DisplayName, emails, u.Active, raw)
	if err != nil {
		return nil, fmt.Errorf("scim replace user: %w", err)
	}
	return u, nil
}

func (s *SCIMUserStore) SetActive(ctx context.Context, orgID uuid.UUID, id string, active bool) error {
	_, err := s.db.Exec(ctx,
		`UPDATE scim_users SET active=$3, updated_at=now() WHERE org_id=$1 AND id=$2`,
		orgID, id, active)
	return err
}

func (s *SCIMUserStore) Delete(ctx context.Context, orgID uuid.UUID, id string) error {
	ct, err := s.db.Exec(ctx,
		`DELETE FROM scim_users WHERE org_id=$1 AND id=$2`, orgID, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("scim user not found")
	}
	return nil
}

func (s *SCIMUserStore) List(ctx context.Context, orgID uuid.UUID, filter *scim.Filter, startIndex, count int) ([]*scim.User, int, error) {
	filterClause, filterArgs := filter.SQLClause(4)
	args := append([]any{orgID, count, startIndex - 1}, filterArgs...)
	rows, err := s.db.Query(ctx,
		`SELECT id, COALESCE(external_id, ''), username, display_name, emails, active
		 FROM scim_users WHERE org_id=$1 `+filterClause+`
		 ORDER BY username LIMIT $2 OFFSET $3`,
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var users []*scim.User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	var total int
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM scim_users WHERE org_id=$1`, orgID).Scan(&total)
	return users, total, rows.Err()
}

func (s *SCIMUserStore) scanUser(row interface{ Scan(...any) error }) (*scim.User, error) {
	var u scim.User
	var emailsJSON []byte
	if err := row.Scan(&u.ID, &u.ExternalID, &u.UserName, &u.DisplayName, &emailsJSON, &u.Active); err != nil {
		return nil, fmt.Errorf("scim scan user: %w", err)
	}
	_ = json.Unmarshal(emailsJSON, &u.Emails)
	u.Schemas = []string{"urn:ietf:params:scim:schemas:core:2.0:User"}
	return &u, nil
}

// SCIMGroupStore implements scim.GroupStore against the scim_groups table.
type SCIMGroupStore struct{ db *pgxpool.Pool }

func NewSCIMGroupStore(db *pgxpool.Pool) *SCIMGroupStore { return &SCIMGroupStore{db: db} }

func (s *SCIMGroupStore) Create(ctx context.Context, orgID uuid.UUID, g *scim.Group) (*scim.Group, error) {
	g.ID = uuid.New().String()
	_, err := s.db.Exec(ctx,
		`INSERT INTO scim_groups (id, org_id, external_id, display_name)
		 VALUES ($1,$2,$3,$4)`,
		g.ID, orgID, optionalString(g.ExternalID), g.DisplayName)
	if err != nil {
		return nil, fmt.Errorf("scim create group: %w", err)
	}
	return g, nil
}

func (s *SCIMGroupStore) ByID(ctx context.Context, orgID uuid.UUID, id string) (*scim.Group, error) {
	var g scim.Group
	err := s.db.QueryRow(ctx,
		`SELECT id, COALESCE(external_id, ''), display_name FROM scim_groups WHERE org_id=$1 AND id=$2`,
		orgID, id).Scan(&g.ID, &g.ExternalID, &g.DisplayName)
	if err != nil {
		return nil, fmt.Errorf("scim group not found: %w", err)
	}
	g.Schemas = []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}
	g.Members, _ = s.listMembers(ctx, g.ID)
	return &g, nil
}

func (s *SCIMGroupStore) Replace(ctx context.Context, orgID uuid.UUID, g *scim.Group) (*scim.Group, error) {
	_, err := s.db.Exec(ctx,
		`UPDATE scim_groups SET display_name=$3, updated_at=now() WHERE org_id=$1 AND id=$2`,
		orgID, g.ID, g.DisplayName)
	if err != nil {
		return nil, fmt.Errorf("scim replace group: %w", err)
	}
	return g, nil
}

func (s *SCIMGroupStore) Delete(ctx context.Context, orgID uuid.UUID, id string) error {
	ct, err := s.db.Exec(ctx,
		`DELETE FROM scim_groups WHERE org_id=$1 AND id=$2`, orgID, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("scim group not found")
	}
	return nil
}

func (s *SCIMGroupStore) List(ctx context.Context, orgID uuid.UUID, _ *scim.Filter, startIndex, count int) ([]*scim.Group, int, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, COALESCE(external_id, ''), display_name FROM scim_groups WHERE org_id=$1
		 ORDER BY display_name LIMIT $2 OFFSET $3`,
		orgID, count, startIndex-1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var groups []*scim.Group
	for rows.Next() {
		var g scim.Group
		if err := rows.Scan(&g.ID, &g.ExternalID, &g.DisplayName); err != nil {
			return nil, 0, err
		}
		g.Schemas = []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}
		groups = append(groups, &g)
	}
	var total int
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM scim_groups WHERE org_id=$1`, orgID).Scan(&total)
	return groups, total, rows.Err()
}

func (s *SCIMGroupStore) SetMembers(ctx context.Context, _ uuid.UUID, groupID string, memberIDs []string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM scim_group_members WHERE group_id=$1`, groupID)
	if err != nil {
		return err
	}
	for _, mid := range memberIDs {
		if _, err := s.db.Exec(ctx,
			`INSERT INTO scim_group_members (group_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			groupID, mid); err != nil {
			return fmt.Errorf("scim add member: %w", err)
		}
	}
	return nil
}

func (s *SCIMGroupStore) listMembers(ctx context.Context, groupID string) ([]scim.GroupMember, error) {
	rows, err := s.db.Query(ctx,
		`SELECT u.id, u.display_name FROM scim_users u
		 JOIN scim_group_members m ON m.user_id = u.id
		 WHERE m.group_id=$1`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []scim.GroupMember
	for rows.Next() {
		var m scim.GroupMember
		_ = rows.Scan(&m.Value, &m.Display)
		members = append(members, m)
	}
	return members, rows.Err()
}

// SCIMTokenValidator validates SCIM bearer tokens via bcrypt comparison.
type SCIMTokenValidator struct{ db *pgxpool.Pool }

func NewSCIMTokenValidator(db *pgxpool.Pool) *SCIMTokenValidator {
	return &SCIMTokenValidator{db: db}
}

func (v *SCIMTokenValidator) Validate(ctx context.Context, orgID uuid.UUID, token string) error {
	var hash string
	if err := v.db.QueryRow(ctx,
		`SELECT token_hash FROM scim_tokens WHERE org_id=$1`, orgID).Scan(&hash); err != nil {
		return fmt.Errorf("scim token not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)); err != nil {
		return fmt.Errorf("scim token invalid")
	}
	return nil
}

// IdentityTraitsReader reads traits for the SAML IdP assertion builder.
type IdentityTraitsReader struct{ db *pgxpool.Pool }

func NewIdentityTraitsReader(db *pgxpool.Pool) *IdentityTraitsReader {
	return &IdentityTraitsReader{db: db}
}

func (r *IdentityTraitsReader) TraitsForIdentity(ctx context.Context, identityID uuid.UUID) (map[string]string, error) {
	var raw []byte
	if err := r.db.QueryRow(ctx,
		`SELECT traits FROM identities WHERE id=$1`, identityID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("traits not found: %w", err)
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(all))
	for k, v := range all {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out, nil
}
