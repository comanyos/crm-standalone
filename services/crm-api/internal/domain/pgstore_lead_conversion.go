package domain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PgStore's half of ConvertLead (lead_conversion.go / B1). Unlike MemStore's
// sequential-calls-against-its-own-methods version, this one runs the whole
// operation inside a single database transaction via scopedDB.begin — the
// same seam pgstore.go's own doc comment on scopedDB.begin calls out as
// being for "the projection and outbox writes that have to land in the same
// transaction as the business row". Every statement below runs against that
// one tx directly (not through s.db.Exec/Query, which would each open their
// own transaction and defeat the point).
//
// The lead row is locked with SELECT ... FOR UPDATE for the duration of the
// transaction, so two concurrent ConvertLead calls against the same lead id
// serialize instead of racing.

// ConvertLead performs the B1 conversion inside one Postgres transaction.
// See lead_conversion.go for the idempotency argument; the Status check
// here is identical to MemStore's.
func (s *PgStore) ConvertLead(ctx context.Context, scope Scope, in ConvertLeadInput) (*ConvertLeadResult, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	tx, err := s.db.begin(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lead := &Lead{}
	err = scanLead(tx.QueryRow(ctx,
		`SELECT `+leadColumns+` FROM leads WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		scope.TenantID, in.LeadID), lead)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get lead for conversion: %w", err)
	}

	if lead.Status == LeadStatusConverted {
		result, err := s.txLoadConversionResult(ctx, tx, scope, lead)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		return result, nil
	}

	if err := checkLeadStatusTransition(lead.Status, LeadStatusConverted); err != nil {
		return nil, err
	}

	email, phone, err := requireLeadContactIdentity(lead)
	if err != nil {
		return nil, err
	}

	contact, account, err := s.txResolveConversionContactAndAccount(ctx, tx, scope, lead, email, phone)
	if err != nil {
		return nil, err
	}

	var opp *Opportunity
	if in.CreateOpportunity {
		opp, err = s.txCreateConversionOpportunity(ctx, tx, scope,
			conversionOpportunityName(in.OpportunityName, lead.Company), account.ID, in.OpportunityAmount)
		if err != nil {
			return nil, err
		}
	}

	lead.Status = LeadStatusConverted
	lead.ConvertedContactID = contact.ID
	lead.ConvertedAccountID = account.ID
	if opp != nil {
		lead.ConvertedOpportunityID = opp.ID
	}
	lead.UpdatedAt = nowStamp()

	if _, err := tx.Exec(ctx,
		`UPDATE leads SET status = $3, converted_contact_id = $4, converted_account_id = $5,
			converted_opportunity_id = $6, updated_at = $7
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, lead.ID, lead.Status, lead.ConvertedContactID, lead.ConvertedAccountID,
		lead.ConvertedOpportunityID, lead.UpdatedAt); err != nil {
		return nil, fmt.Errorf("mark lead converted: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &ConvertLeadResult{Lead: lead, Contact: contact, Account: account, Opportunity: opp}, nil
}

// txResolveConversionContactAndAccount mirrors MemStore's
// resolveConversionContactAndAccount exactly, against the open tx.
func (s *PgStore) txResolveConversionContactAndAccount(
	ctx context.Context, tx pgx.Tx, scope Scope, lead *Lead, email, phone string,
) (*Contact, *Account, error) {
	var existing *Contact
	var err error
	if email != "" {
		existing, err = s.txFindContactByEmail(ctx, tx, scope, email)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
	}
	if existing == nil && phone != "" {
		existing, err = s.txFindContactByPhone(ctx, tx, scope, phone)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
	}
	if existing != nil {
		account, aerr := s.txGetAccount(ctx, tx, scope, existing.AccountID)
		if aerr == nil {
			return existing, account, nil
		}
		if !errors.Is(aerr, ErrNotFound) {
			return nil, nil, aerr
		}
		account, err = s.txResolveOrCreateAccount(ctx, tx, scope, lead.Company, emailDomain(email), leadDisplayName(lead))
		if err != nil {
			return nil, nil, err
		}
		existing.AccountID = account.ID
		if _, err := tx.Exec(ctx,
			`UPDATE contacts SET account_id = $3, updated_at = $4 WHERE tenant_id = $1 AND id = $2`,
			scope.TenantID, existing.ID, account.ID, nowStamp()); err != nil {
			return nil, nil, fmt.Errorf("attach reused contact to account: %w", err)
		}
		return existing, account, nil
	}

	account, err := s.txResolveOrCreateAccount(ctx, tx, scope, lead.Company, emailDomain(email), leadDisplayName(lead))
	if err != nil {
		return nil, nil, err
	}
	contact, err := s.txCreateContact(ctx, tx, scope, lead, email, phone, account.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("create conversion contact: %w", err)
	}
	return contact, account, nil
}

// txResolveOrCreateAccount mirrors MemStore's resolveOrCreateConversionAccount.
func (s *PgStore) txResolveOrCreateAccount(
	ctx context.Context, tx pgx.Tx, scope Scope, company, domain, fallback string,
) (*Account, error) {
	if domain != "" {
		acct, err := s.txFindAccountByDomain(ctx, tx, scope, domain)
		if err == nil {
			return acct, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	acct, err := s.txCreateAccount(ctx, tx, scope, newConversionAccount(company, domain, fallback))
	if err != nil {
		return nil, fmt.Errorf("create conversion account: %w", err)
	}
	return acct, nil
}

func (s *PgStore) txFindContactByEmail(ctx context.Context, tx pgx.Tx, scope Scope, email string) (*Contact, error) {
	v := &Contact{}
	err := scanContact(tx.QueryRow(ctx,
		`SELECT `+contactColumns+` FROM contacts
		 WHERE tenant_id = $1 AND lower(email) = lower($2)
		 ORDER BY id LIMIT 1`,
		scope.TenantID, email), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find contact by email: %w", err)
	}
	return v, nil
}

func (s *PgStore) txFindContactByPhone(ctx context.Context, tx pgx.Tx, scope Scope, phone string) (*Contact, error) {
	v := &Contact{}
	err := scanContact(tx.QueryRow(ctx,
		`SELECT `+contactColumns+` FROM contacts
		 WHERE tenant_id = $1 AND regexp_replace(phone, '[^0-9+]', '', 'g') = $2
		 ORDER BY id LIMIT 1`, scope.TenantID, phone), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find contact by phone: %w", err)
	}
	return v, nil
}

// txCreateContact mirrors MemStore's conversion-time contact creation: it
// skips role and duplicate_of_contact_id on purpose. role has no
// conversion-time source (the same asymmetry txCreateAccount already has
// with owner_id/duplicate_of_account_id), and duplicate_of_contact_id is
// guaranteed empty here — this path only runs when
// txResolveConversionContactAndAccount's own email lookup already came back
// ErrNotFound, so no other Contact can share this email to flag against.
func (s *PgStore) txCreateContact(
	ctx context.Context, tx pgx.Tx, scope Scope, lead *Lead, email, phone, accountID string,
) (*Contact, error) {
	v := &Contact{
		Email: email, Phone: phone, AccountID: accountID,
		FirstName: lead.FirstName, LastName: lead.LastName,
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, txReader{s: s, tx: tx}, scope, "contact", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	v.ID = newID()
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now
	_, err = tx.Exec(ctx,
		`INSERT INTO contacts (tenant_id, id, email, account_id, first_name, last_name, phone, title, role, duplicate_of_contact_id, created_at, updated_at, custom_fields)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, '', '', '', $8, $9, $10)`,
		scope.TenantID, v.ID, v.Email, v.AccountID, v.FirstName, v.LastName, v.Phone,
		v.CreatedAt, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("insert contact: %w", err)
	}
	return v, nil
}

func (s *PgStore) txGetAccount(ctx context.Context, tx pgx.Tx, scope Scope, id string) (*Account, error) {
	v := &Account{}
	err := scanAccount(tx.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}
	return v, nil
}

// txFindAccountByDomain mirrors PgStore.FindAccountByDomain, against the
// open tx instead of a fresh scopedDB call.
func (s *PgStore) txFindAccountByDomain(ctx context.Context, tx pgx.Tx, scope Scope, domain string) (*Account, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+accountColumns+` FROM accounts
		 WHERE tenant_id = $1
		 ORDER BY id`,
		scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("find account by domain: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		v := &Account{}
		if err := scanAccount(rows, v); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		if sameDomain(websiteDomain(v.Website), domain) {
			return v, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find account by domain: %w", err)
	}
	return nil, ErrNotFound
}

func (s *PgStore) txCreateAccount(ctx context.Context, tx pgx.Tx, scope Scope, v *Account) (*Account, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, txReader{s: s, tx: tx}, scope, "account", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	v.ID = newID()
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now
	_, err = tx.Exec(ctx,
		`INSERT INTO accounts (tenant_id, id, name, industry, tier, owner_id, website, annual_revenue, parent_id, created_at, updated_at, custom_fields)
		 VALUES ($1, $2, $3, '', '', '', $4, 0, '', $5, $6, $7)`,
		scope.TenantID, v.ID, v.Name, v.Website, v.CreatedAt, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("insert account: %w", err)
	}
	return v, nil
}

func (s *PgStore) txCreateConversionOpportunity(
	ctx context.Context, tx pgx.Tx, scope Scope, name, accountID string, amount float64,
) (*Opportunity, error) {
	v := &Opportunity{Name: name, AccountID: accountID, Amount: amount}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	// The same stage resolution CreateOpportunity performs, run against this
	// transaction: the tenant's default pipeline (provisioned here if it has
	// none yet) at its first open stage. Sharing it is what keeps a
	// conversion-created deal indistinguishable from a directly created one.
	if _, err := prepareOpportunityStage(ctx, txReader{s: s, tx: tx}, scope, v, nil); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, txReader{s: s, tx: tx}, scope, "opportunity", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	v.ID = newID()
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now
	// close_date is passed as a parameter, not a literal SQL NULL: it is a
	// zero-value time.Time here (the Opportunity is freshly created, open),
	// and pgx encodes that as a real timestamp rather than NULL — the same
	// convention PgStore.CreateOpportunity uses, so GetOpportunity's
	// non-pointer time.Time scan target never has to handle an actual NULL.
	_, err = tx.Exec(ctx,
		`INSERT INTO opportunities (tenant_id, id, name, account_id, stage, pipeline_id, stage_id, amount, probability, close_date, close_reason, created_at, updated_at, custom_fields)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, $9, '', $10, $11, $12)`,
		scope.TenantID, v.ID, v.Name, v.AccountID, v.Stage, v.PipelineID, v.StageID, v.Amount, v.CloseDate, v.CreatedAt, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("insert conversion opportunity: %w", err)
	}
	return v, nil
}

// txLoadConversionResult mirrors MemStore's loadConversionResult for the
// idempotent-retry path.
func (s *PgStore) txLoadConversionResult(ctx context.Context, tx pgx.Tx, scope Scope, lead *Lead) (*ConvertLeadResult, error) {
	contact, err := s.txGetContact(ctx, tx, scope, lead.ConvertedContactID)
	if err != nil {
		return nil, fmt.Errorf("load converted contact: %w", err)
	}
	account, err := s.txGetAccount(ctx, tx, scope, lead.ConvertedAccountID)
	if err != nil {
		return nil, fmt.Errorf("load converted account: %w", err)
	}
	var opp *Opportunity
	if lead.ConvertedOpportunityID != "" {
		opp, err = s.txGetOpportunity(ctx, tx, scope, lead.ConvertedOpportunityID)
		if err != nil {
			return nil, fmt.Errorf("load converted opportunity: %w", err)
		}
	}
	return &ConvertLeadResult{Lead: lead, Contact: contact, Account: account, Opportunity: opp}, nil
}

func (s *PgStore) txGetContact(ctx context.Context, tx pgx.Tx, scope Scope, id string) (*Contact, error) {
	v := &Contact{}
	err := scanContact(tx.QueryRow(ctx,
		`SELECT `+contactColumns+` FROM contacts
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get contact: %w", err)
	}
	return v, nil
}

func (s *PgStore) txGetOpportunity(ctx context.Context, tx pgx.Tx, scope Scope, id string) (*Opportunity, error) {
	v := &Opportunity{}
	err := scanOpportunity(tx.QueryRow(ctx,
		`SELECT `+opportunityColumns+` FROM opportunities WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get opportunity: %w", err)
	}
	return v, nil
}
