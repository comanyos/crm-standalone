package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Account hierarchy, ownership and dedup (B3 — docs/customer-os/PLAN.md).
//
// requireValidAccountParent follows the hcm OrgUnit acyclicity invariant
// (03-people-os/hcm/.../internal/domain/invariants.go) idiom exactly: one
// walk up the parent chain enforces both halves of the rule. A dangling or
// cross-tenant parent_id is ErrInvalid (the request names something that
// does not exist from this tenant's point of view); a cycle is
// ErrInvalidTransition (the request is well-formed but the resulting graph
// is not a tree) — hcm's own split, and its own sentinel, reused rather
// than inventing an Account-specific one.
//
// requireAccountOwner and applyAccountDuplicateFlag are the other two B3
// rules: owner_id is required (ErrInvalid, the same convention Account.name
// already uses in Validate()), and an exact Website-domain match against
// another tenant-scoped Account is flagged — never blocked — the same
// "flag, don't merge" convention lead_lifecycle.go uses for
// Lead.DuplicateOfContactID.
//
// Deliberately NOT here: account merge (Track D) and fuzzy/name-based
// dedup — see docs/customer-os/PLAN.md B3.

// requireAccountOwner fails when v.OwnerID is empty. owner_id is required on
// every Account, unlike Account.parent_id which is optional.
func requireAccountOwner(v *Account) error {
	if strings.TrimSpace(v.OwnerID) == "" {
		return fmt.Errorf("%w: Account.owner_id is required", ErrInvalid)
	}
	return nil
}

// requireValidAccountParent enforces both halves of the acyclicity
// invariant in one walk: the direct parent must exist in this tenant, and
// following ParentID upward from it must never reach selfID (the account
// being created or updated).
//
// selfID is empty on Create unless the caller supplied their own ID — in
// that case an account naming itself as its own parent is still caught,
// since the walk starts at parentID and checks against selfID on every hop.
func requireValidAccountParent(ctx context.Context, r accountReader, scope Scope, selfID, parentID string) error {
	if parentID == "" {
		return nil
	}
	if selfID != "" && parentID == selfID {
		return fmt.Errorf("%w: Account %q cannot be its own parent", ErrInvalidTransition, selfID)
	}

	seen := map[string]bool{}
	cur := parentID
	for cur != "" {
		if seen[cur] {
			// The existing data already loops; do not let a new edge attach
			// to it without saying so.
			return fmt.Errorf("%w: Account parent chain starting at %q loops before reaching a root", ErrInvalidTransition, parentID)
		}
		seen[cur] = true

		acct, err := r.GetAccount(ctx, scope, cur)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: Account.parent_id %q does not exist", ErrInvalid, parentID)
		}
		if err != nil {
			return err
		}
		if selfID != "" && acct.ParentID == selfID {
			return fmt.Errorf("%w: Account %q cannot be its own ancestor", ErrInvalidTransition, selfID)
		}
		cur = acct.ParentID
	}
	return nil
}

// accountByDomainFinder is what applyAccountDuplicateFlag needs from a
// store. Both MemStore and PgStore already implement it (store.go).
type accountByDomainFinder interface {
	FindAccountByDomain(ctx context.Context, scope Scope, domain string) (*Account, error)
}

// applyAccountDuplicateFlag recomputes v.DuplicateOfAccountID from the
// current state of the tenant's accounts. It is called on every Create and
// Update so the flag never goes stale: a domain that stops matching (the
// other account was deleted, or this account's own website changed) clears
// the flag, and one that starts matching sets it.
//
// The key is Website's domain — already what FindAccountByDomain matches
// on for B1's lead conversion — not a fuzzy name comparison. Matching only
// itself (an Update whose domain already belonged to this same account) is
// not a duplicate.
func applyAccountDuplicateFlag(ctx context.Context, r accountByDomainFinder, scope Scope, v *Account) error {
	v.DuplicateOfAccountID = ""
	domain := websiteDomain(v.Website)
	if domain == "" {
		return nil
	}
	match, err := r.FindAccountByDomain(ctx, scope, domain)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if match.ID == v.ID {
		return nil
	}
	v.DuplicateOfAccountID = match.ID
	return nil
}
