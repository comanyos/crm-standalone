package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Account deduplication and merge (M4 — Data productivity).
//
// CreateAccount/UpdateAccount already flag a likely duplicate by exact
// website-domain match (account_hierarchy.go, DuplicateOfAccountID) — this
// file is what a reviewer does once they have decided two flagged accounts
// really are the same company: pick a survivor, resolve each differing
// field, and fold the duplicate's relationships into the survivor.
//
// WHY THE DUPLICATE ROW IS DELETED, NOT ARCHIVED. Accounts have no archived
// state the way Pipelines and custom fields do — DeleteAccount is already a
// hard delete in this store. A merge keeps that convention rather than
// inventing a new one: once every relationship below has been repointed to
// the survivor, the duplicate row carries nothing an archive would preserve.
//
// WHY THERE IS NO UNDO. Reversing a merge would mean recreating the
// duplicate account with its exact prior field values, and then deciding —
// for every contact, opportunity, task, note, activity and tag assignment
// migrated below — which ones to hand back. Some of those may have been
// edited, tagged again, or gained new children since the merge ran, and the
// merge audit row does not track object-level state finely enough to
// reverse those changes safely. Rather than build an undo that silently
// mishandles that case, this version does not offer one. AccountMergeRecord
// is kept forever specifically so a human can reconstruct what happened and
// redo the split by hand if it is ever needed.
//
// RELATIONSHIP MIGRATION. Every one of the duplicate's children moves to
// the survivor: contacts, opportunities, child accounts (parent_id), the
// Activity/Task/Note polymorphic subject edge, tag assignments (dropped
// instead of duplicated if the survivor already carries the same tag), and
// AccountOwnershipHistory rows (so "who owned this, and when" stays answerable
// across the merge). See MemStore/PgStore's MergeAccounts for the exact
// statements.

// mergeableAccountFields is the allowlist of Account fields a merge may
// resolve per field. ID, timestamps, CustomFields and the dedup flag itself
// are not resolvable: custom fields have their own coercion/authorization
// path this operation does not replicate, and the dedup flag is
// recomputed fresh by the ordinary write path the next time anything about
// the survivor changes, not hand-set by a merge.
var mergeableAccountFields = map[string]bool{
	"name": true, "industry": true, "tier": true, "owner_id": true,
	"website": true, "annual_revenue": true, "parent_id": true,
}

// SortedMergeableAccountFields returns the allowlist, sorted, for the
// authoring UI to offer.
func SortedMergeableAccountFields() []string {
	out := make([]string, 0, len(mergeableAccountFields))
	for f := range mergeableAccountFields {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Field-resolution values: which side of the merge a field's value comes
// from. A field absent from FieldResolutions defaults to keeping the
// survivor's existing value, so a caller only names the fields they are
// changing.
const (
	ResolutionSurvivor  = "survivor"
	ResolutionDuplicate = "duplicate"
)

// MergeAccountsInput names everything one merge needs to decide.
type MergeAccountsInput struct {
	SurvivorID       string
	DuplicateID      string
	FieldResolutions map[string]string
	MergedBy         string
}

// Validate rejects a merge request that cannot be applied.
func (in MergeAccountsInput) Validate() error {
	if strings.TrimSpace(in.SurvivorID) == "" || strings.TrimSpace(in.DuplicateID) == "" {
		return fmt.Errorf("%w: a merge needs both a survivor and a duplicate account id", ErrInvalid)
	}
	if in.SurvivorID == in.DuplicateID {
		return fmt.Errorf("%w: an account cannot be merged into itself", ErrInvalid)
	}
	for field, resolution := range in.FieldResolutions {
		if !mergeableAccountFields[field] {
			return fmt.Errorf("%w: %q is not a field a merge can resolve (allowed: %s)",
				ErrInvalid, field, strings.Join(SortedMergeableAccountFields(), ", "))
		}
		if resolution != ResolutionSurvivor && resolution != ResolutionDuplicate {
			return fmt.Errorf("%w: %q is not survivor or duplicate for field %q", ErrInvalid, resolution, field)
		}
	}
	return nil
}

// resolveAccountFields applies in.FieldResolutions to survivor in place,
// pulling each named field from duplicate. It is the one place that knows
// every mergeable field's Go type, so PgStore and MemStore cannot resolve a
// field differently.
func resolveAccountFields(survivor, duplicate *Account, resolutions map[string]string) {
	if resolutions["name"] == ResolutionDuplicate {
		survivor.Name = duplicate.Name
	}
	if resolutions["industry"] == ResolutionDuplicate {
		survivor.Industry = duplicate.Industry
	}
	if resolutions["tier"] == ResolutionDuplicate {
		survivor.Tier = duplicate.Tier
	}
	if resolutions["owner_id"] == ResolutionDuplicate {
		survivor.OwnerID = duplicate.OwnerID
	}
	if resolutions["website"] == ResolutionDuplicate {
		survivor.Website = duplicate.Website
	}
	if resolutions["annual_revenue"] == ResolutionDuplicate {
		survivor.AnnualRevenue = duplicate.AnnualRevenue
	}
	if resolutions["parent_id"] == ResolutionDuplicate {
		survivor.ParentID = duplicate.ParentID
	}
	// A survivor must never end up as its own parent — the one shape of
	// cycle a merge could introduce, if the duplicate happened to be the
	// survivor's own parent and the caller chose the duplicate's parent_id.
	if survivor.ParentID == survivor.ID {
		survivor.ParentID = ""
	}
}

// AccountMergeRecord is one immutable audit row: what was merged, by whom,
// how each field was resolved, and how many rows of each related kind moved.
// Never updated or deleted once written — see this file's package doc
// comment for why a merge has no undo.
type AccountMergeRecord struct {
	ID                    string
	SurvivorAccountID     string
	DuplicateAccountID    string
	MergedBy              string
	FieldResolutions      map[string]string
	RelationshipsMigrated map[string]int
	MergedAt              time.Time
}

// EntityID returns the record's identifier.
func (e *AccountMergeRecord) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *AccountMergeRecord) SetEntityID(id string) { e.ID = id }

// SetTimestamps is a no-op beyond MergedAt, which the writer stamps
// explicitly: a merge record is written once and never touched again, so
// "created" and "merged" are the same instant and there is no "updated".
func (e *AccountMergeRecord) SetTimestamps(_, _ time.Time) {}

// Validate rejects a merge record that cannot be stored. Field-level
// validation already happened in MergeAccountsInput.Validate before the
// merge ran; this only guards the shape of the row itself.
func (e *AccountMergeRecord) Validate() error {
	if strings.TrimSpace(e.SurvivorAccountID) == "" || strings.TrimSpace(e.DuplicateAccountID) == "" {
		return fmt.Errorf("%w: an account merge record needs both account ids", ErrInvalid)
	}
	if e.SurvivorAccountID == e.DuplicateAccountID {
		return fmt.Errorf("%w: an account merge record cannot name the same account twice", ErrInvalid)
	}
	return nil
}

func cloneAccountMergeRecord(v *AccountMergeRecord) *AccountMergeRecord {
	if v == nil {
		return nil
	}
	c := *v
	c.FieldResolutions = make(map[string]string, len(v.FieldResolutions))
	for k, val := range v.FieldResolutions {
		c.FieldResolutions[k] = val
	}
	c.RelationshipsMigrated = make(map[string]int, len(v.RelationshipsMigrated))
	for k, val := range v.RelationshipsMigrated {
		c.RelationshipsMigrated[k] = val
	}
	return &c
}
