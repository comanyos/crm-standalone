package api

import (
	"testing"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
)

// TestMergeAccountsRPC exercises the RPC-layer field resolution and audit
// visibility contract, above and beyond the store-layer parity test in
// internal/domain/merge_accounts_test.go.
func TestMergeAccountsRPC(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-merge-rpc", "org-merge-rpc")

	survivor, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", Tier: "silver", OwnerId: "alice"},
	}))
	if err != nil {
		t.Fatalf("create survivor: %v", err)
	}
	duplicate, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme Inc", Tier: "gold", OwnerId: "bob"},
	}))
	if err != nil {
		t.Fatalf("create duplicate: %v", err)
	}

	res, err := s.MergeAccounts(ctx, connect.NewRequest(&crmv1.MergeAccountsRequest{
		SurvivorId: survivor.Msg.GetAccount().GetId(), DuplicateId: duplicate.Msg.GetAccount().GetId(),
		FieldResolutions: map[string]string{"tier": "duplicate"},
	}))
	if err != nil {
		t.Fatalf("MergeAccounts: %v", err)
	}
	if res.Msg.GetAccount().GetTier() != "gold" {
		t.Errorf("tier = %q, want gold", res.Msg.GetAccount().GetTier())
	}
	if res.Msg.GetMerge().GetSurvivorAccountId() != survivor.Msg.GetAccount().GetId() {
		t.Errorf("merge.survivor_account_id = %q, want %q", res.Msg.GetMerge().GetSurvivorAccountId(), survivor.Msg.GetAccount().GetId())
	}

	// The duplicate account is really gone.
	if _, err := s.GetAccount(ctx, connect.NewRequest(&crmv1.GetAccountRequest{Id: duplicate.Msg.GetAccount().GetId()})); err == nil {
		t.Error("duplicate account still exists after merge")
	}

	// The audit trail is visible from both ids.
	fromSurvivor, err := s.ListAccountMerges(ctx, connect.NewRequest(&crmv1.ListAccountMergesRequest{
		AccountId: survivor.Msg.GetAccount().GetId(),
	}))
	if err != nil || len(fromSurvivor.Msg.GetMerges()) != 1 {
		t.Fatalf("ListAccountMerges(survivor) = %+v, %v", fromSurvivor, err)
	}
	fromDuplicate, err := s.ListAccountMerges(ctx, connect.NewRequest(&crmv1.ListAccountMergesRequest{
		AccountId: duplicate.Msg.GetAccount().GetId(),
	}))
	if err != nil || len(fromDuplicate.Msg.GetMerges()) != 1 {
		t.Fatalf("ListAccountMerges(duplicate id) = %+v, %v", fromDuplicate, err)
	}
}

// TestMergeAccountsRejectsInvalidFieldResolution proves a caller cannot name
// a field the domain layer does not consider mergeable — the RPC boundary
// must surface ErrInvalid as InvalidArgument, not a raw internal error.
func TestMergeAccountsRejectsInvalidFieldResolution(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-merge-rpc-invalid", "org-merge-rpc-invalid")

	survivor, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "alice"},
	}))
	if err != nil {
		t.Fatalf("create survivor: %v", err)
	}
	duplicate, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme Inc", OwnerId: "bob"},
	}))
	if err != nil {
		t.Fatalf("create duplicate: %v", err)
	}

	_, err = s.MergeAccounts(ctx, connect.NewRequest(&crmv1.MergeAccountsRequest{
		SurvivorId: survivor.Msg.GetAccount().GetId(), DuplicateId: duplicate.Msg.GetAccount().GetId(),
		FieldResolutions: map[string]string{"custom_fields": "duplicate"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err: %v)", connect.CodeOf(err), err)
	}
}
