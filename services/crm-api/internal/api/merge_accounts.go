package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// Account deduplication and merge (M4 — data productivity,
// internal/domain/merge_accounts.go).

func accountMergeToProto(v *domain.AccountMergeRecord) *crmv1.AccountMergeRecord {
	if v == nil {
		return nil
	}
	migrated := make(map[string]int32, len(v.RelationshipsMigrated))
	for k, n := range v.RelationshipsMigrated {
		migrated[k] = int32(n)
	}
	return &crmv1.AccountMergeRecord{
		Id: v.ID, SurvivorAccountId: v.SurvivorAccountID, DuplicateAccountId: v.DuplicateAccountID,
		MergedBy: v.MergedBy, FieldResolutions: v.FieldResolutions, RelationshipsMigrated: migrated,
		MergedAt: optionalTimestamp(v.MergedAt),
	}
}

// MergeAccounts folds a duplicate account's relationships into a survivor.
// There is no undo — see internal/domain/merge_accounts.go — so this is a
// destructive, confirmed-by-the-caller action, the same posture DeleteAccount
// has, just with more to lose.
func (s *CRMService) MergeAccounts(
	ctx context.Context, req *connect.Request[crmv1.MergeAccountsRequest],
) (*connect.Response[crmv1.MergeAccountsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	account, record, err := s.store.MergeAccounts(ctx, scope, domain.MergeAccountsInput{
		SurvivorID: req.Msg.GetSurvivorId(), DuplicateID: req.Msg.GetDuplicateId(),
		FieldResolutions: req.Msg.GetFieldResolutions(), MergedBy: principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.accountForRead(ctx, scope, account)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.MergeAccountsResponse{
		Account: result, Merge: accountMergeToProto(record),
	}), nil
}

// ListAccountMerges returns every merge one account was ever part of.
func (s *CRMService) ListAccountMerges(
	ctx context.Context, req *connect.Request[crmv1.ListAccountMergesRequest],
) (*connect.Response[crmv1.ListAccountMergesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.store.ListAccountMerges(ctx, scope, req.Msg.GetAccountId())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.AccountMergeRecord, 0, len(records))
	for _, r := range records {
		out = append(out, accountMergeToProto(r))
	}
	return connect.NewResponse(&crmv1.ListAccountMergesResponse{Merges: out}), nil
}
