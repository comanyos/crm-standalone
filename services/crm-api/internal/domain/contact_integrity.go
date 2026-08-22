package domain

import (
	"context"
	"errors"
	"fmt"
)

// Contact referential integrity, role validation and dedup (B4 —
// docs/customer-os/PLAN.md).
//
// requireContactAccount follows requireAccount's idiom exactly
// (transitions.go — the Opportunity->Account referential check is the same
// shape): a dangling or cross-tenant account_id is ErrInvalid.
//
// requireContactRole and applyContactDuplicateFlag are the other two B4
// rules: role, when set, must be one of contactRoles (a plain validated
// field, not a workflow — empty is allowed, meaning "not yet known"); and an
// exact email match against another tenant-scoped Contact is flagged —
// never blocked — the same "flag, don't merge" convention lead_lifecycle.go
// uses for Lead.DuplicateOfContactID and account_hierarchy.go uses for
// Account.DuplicateOfAccountID.
//
// Deliberately NOT here: contact merge (Track D) and fuzzy dedup — see
// docs/customer-os/PLAN.md B4.

// Contact.Role values. This set is deliberately small and not exhaustive of
// every real-world buying-committee role; it exists to give the field a
// typed shape, not to encode a business taxonomy. Extend it here if the
// suite needs more.
const (
	ContactRoleBilling       = "billing"
	ContactRoleTechnical     = "technical"
	ContactRoleDecisionMaker = "decision_maker"
	ContactRoleOther         = "other"
)

// contactRoles is the known-value set requireContactRole checks against.
var contactRoles = map[string]bool{
	ContactRoleBilling:       true,
	ContactRoleTechnical:     true,
	ContactRoleDecisionMaker: true,
	ContactRoleOther:         true,
}

// requireContactRole fails when v.Role is set to something other than one
// of contactRoles. An empty Role is always allowed — the role at the
// account is not always known when a Contact is first created.
func requireContactRole(v *Contact) error {
	if v.Role == "" {
		return nil
	}
	if !contactRoles[v.Role] {
		return fmt.Errorf("%w: Contact.role %q is not a known role", ErrInvalid, v.Role)
	}
	return nil
}

// requireContactAccount fails when no Account names accountID in this
// tenant. An empty accountID fails the same way an unknown one does:
// GetAccount("") never matches a stored id, so this also makes account_id
// effectively required on every Contact write — the same behavior
// requireAccount already gives Opportunity.
func requireContactAccount(ctx context.Context, r accountReader, scope Scope, accountID string) error {
	_, err := r.GetAccount(ctx, scope, accountID)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: Contact.account_id %q has no account", ErrInvalid, accountID)
	}
	return err
}

// applyContactDuplicateFlag recomputes v.DuplicateOfContactID from the
// current state of the tenant's contacts. It is called on every Create and
// Update so the flag never goes stale: an email that stops matching
// (the other contact was deleted, or this contact's own email changed)
// clears the flag, and one that starts matching sets it. A contact never
// flags itself.
func applyContactDuplicateFlag(ctx context.Context, r contactByEmailFinder, scope Scope, v *Contact) error {
	v.DuplicateOfContactID = ""
	if v.Email == "" {
		return nil
	}
	match, err := r.FindContactByEmail(ctx, scope, v.Email)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if match.ID == v.ID {
		return nil
	}
	v.DuplicateOfContactID = match.ID
	return nil
}
