package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Two-organization isolation proof at the database and store layers
// (task.md §1.3).
//
// pgstore_rls_test.go already proves the accounts policy filters and that its
// WITH CHECK half rejects a cross-tenant INSERT. This file widens that from
// one hand-named table to *every* tenant-owned table the catalog can find,
// and from raw SQL to the Store API a request actually travels through.
//
// The catalog-driven shape is deliberate. A hand-written list of tables goes
// stale the moment someone adds a migration, and the failure mode of a stale
// list is silent: the new table simply never gets asserted on. Asking
// Postgres which tables carry a tenant_id means a table added tomorrow
// without RLS fails these tests tomorrow, not after it leaks.

// tenantTables returns every ordinary table in the public schema that carries
// a tenant_id column — the definition of "tenant-owned" this suite uses.
func tenantTables(ctx context.Context, t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT c.relname
		   FROM pg_class c
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		   JOIN pg_attribute a ON a.attrelid = c.oid
		  WHERE n.nspname = 'public'
		    AND c.relkind = 'r'
		    AND a.attname = 'tenant_id'
		    AND a.attnum > 0
		    AND NOT a.attisdropped
		  ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("list tenant tables: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tenant tables: %v", err)
	}
	return out
}

// tenantFixture is one organization's worth of seeded CRM data: at least one
// row in every entity a tenant owns, so an isolation assertion cannot pass
// merely because there was nothing to leak.
type tenantFixture struct {
	scope         Scope
	accountID     string
	contactID     string
	leadID        string
	opportunityID string
	activityID    string
	pipelineID    string
	stageID       string
	taskID        string
	noteID        string
	eventID       string
}

// seedTenant writes a complete record set for one tenant, exercising the
// paths that also write the append-only history tables and the outbox.
func seedTenant(ctx context.Context, t *testing.T, s *PgStore, label string) tenantFixture {
	t.Helper()
	stamp := time.Now().UnixNano()
	f := tenantFixture{scope: Scope{
		TenantID: fmt.Sprintf("tenant-iso-%s-%d", label, stamp),
		OrgID:    fmt.Sprintf("org-iso-%s-%d", label, stamp),
	}}

	account, err := s.CreateAccount(ctx, f.scope, &Account{
		Name:    label + " Holdings",
		OwnerID: "owner-" + label,
		Website: fmt.Sprintf("https://%s-%d.example", label, stamp),
	})
	if err != nil {
		t.Fatalf("[%s] create account: %v", label, err)
	}
	f.accountID = account.ID

	// A real owner change, so account_ownership_history has a row to hide.
	account.OwnerID = "owner-" + label + "-second"
	if _, err := s.UpdateAccount(ctx, f.scope, account); err != nil {
		t.Fatalf("[%s] reassign account owner: %v", label, err)
	}

	contact, err := s.CreateContact(ctx, f.scope, &Contact{
		Email:     fmt.Sprintf("%s-%d@example.com", label, stamp),
		AccountID: f.accountID,
		FirstName: label,
		LastName:  "Buyer",
		Role:      "billing",
	})
	if err != nil {
		t.Fatalf("[%s] create contact: %v", label, err)
	}
	f.contactID = contact.ID

	lead, err := s.CreateLead(ctx, f.scope, &Lead{
		Company: label + " Prospect",
		Source:  "referral",
		OwnerID: "owner-" + label,
	})
	if err != nil {
		t.Fatalf("[%s] create lead: %v", label, err)
	}
	f.leadID = lead.ID

	opportunity, err := s.CreateOpportunity(ctx, f.scope, &Opportunity{
		Name:      label + " Renewal",
		AccountID: f.accountID,
		Amount:    50_000,
	})
	if err != nil {
		t.Fatalf("[%s] create opportunity: %v", label, err)
	}
	f.opportunityID = opportunity.ID

	// A real stage advance, so opportunity_stage_history has a row to hide.
	opportunity.Stage = StageQualification
	if _, err := s.UpdateOpportunity(ctx, f.scope, opportunity); err != nil {
		t.Fatalf("[%s] advance opportunity: %v", label, err)
	}

	activity, err := s.CreateActivity(ctx, f.scope, &Activity{
		Kind:        "note",
		SubjectType: ActivitySubjectAccount,
		SubjectID:   f.accountID,
		Notes:       label + " confidential negotiation note",
		ActorID:     "owner-" + label,
		OccurredAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("[%s] create activity: %v", label, err)
	}
	f.activityID = activity.ID

	task, err := s.CreateTask(ctx, f.scope, &Task{
		Title: label + " follow-up", Priority: TaskPriorityHigh,
		AssigneeID: "owner-" + label, SubjectType: SubjectAccount, SubjectID: f.accountID,
		DueAt: time.Now().UTC().Add(24 * time.Hour), CreatedBy: "owner-" + label,
	})
	if err != nil {
		t.Fatalf("[%s] create task: %v", label, err)
	}
	f.taskID = task.ID

	note, err := s.CreateNote(ctx, f.scope, &Note{
		Body:        label + " confidential note body",
		SubjectType: SubjectAccount, SubjectID: f.accountID, AuthorID: "owner-" + label,
	})
	if err != nil {
		t.Fatalf("[%s] create note: %v", label, err)
	}
	f.noteID = note.ID

	pipeline, err := s.CreatePipeline(ctx, f.scope, &Pipeline{Name: label + " Sales"})
	if err != nil {
		t.Fatalf("[%s] create pipeline: %v", label, err)
	}
	f.pipelineID = pipeline.ID

	stage, err := s.CreatePipelineStage(ctx, f.scope, &PipelineStage{
		PipelineID:   f.pipelineID,
		Name:         "Discovery",
		Position:     0,
		Probability:  10,
		TerminalType: "open",
	})
	if err != nil {
		t.Fatalf("[%s] create pipeline stage: %v", label, err)
	}
	f.stageID = stage.ID

	event, err := EnqueueEvent(ctx, s, f.scope, Envelope{
		ID:            fmt.Sprintf("evt-iso-%s-%d", label, stamp),
		Type:          "opportunity.created",
		TenantID:      f.scope.TenantID,
		CorrelationID: "corr-iso",
		SourceApp:     SourceAppCRM,
		EntityType:    "opportunity",
		EntityID:      f.opportunityID,
		SourceVersion: 1,
		Actor:         "owner-" + label,
		OccurredAt:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("[%s] enqueue outbox event: %v", label, err)
	}
	f.eventID = event.ID

	return f
}

// TestEveryTenantTableEnforcesRLS is the structural half of the proof: a
// table carrying tenant_id must have row-level security enabled, FORCEd (so
// the owning role is not exempt), and a policy with both a USING and a
// WITH CHECK expression (so reads are filtered *and* writes cannot be
// labelled with someone else's tenant).
func TestEveryTenantTableEnforcesRLS(t *testing.T) {
	_ = livePgStore(t) // applies migrations
	ctx := context.Background()
	pool := restrictedPool(t)

	tables := tenantTables(ctx, t, pool)
	if len(tables) == 0 {
		t.Fatal("found no tables carrying tenant_id — the catalog query is wrong, " +
			"and every assertion below would pass vacuously")
	}

	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			var enabled, forced bool
			if err := pool.QueryRow(ctx,
				`SELECT c.relrowsecurity, c.relforcerowsecurity
				   FROM pg_class c
				   JOIN pg_namespace n ON n.oid = c.relnamespace
				  WHERE n.nspname = 'public' AND c.relname = $1`,
				table).Scan(&enabled, &forced); err != nil {
				t.Fatalf("read RLS flags: %v", err)
			}
			if !enabled {
				t.Errorf("%s owns tenant data but does not have ROW LEVEL SECURITY enabled", table)
			}
			if !forced {
				t.Errorf("%s does not FORCE ROW LEVEL SECURITY — the table owner is exempt "+
					"from its own policy, so the policy proves nothing", table)
			}

			var withUsing, withCheck int
			if err := pool.QueryRow(ctx,
				`SELECT count(*) FILTER (WHERE qual IS NOT NULL),
				        count(*) FILTER (WHERE with_check IS NOT NULL)
				   FROM pg_policies
				  WHERE schemaname = 'public' AND tablename = $1`,
				table).Scan(&withUsing, &withCheck); err != nil {
				t.Fatalf("read policies: %v", err)
			}
			if withUsing == 0 {
				t.Errorf("%s has no policy with a USING expression — reads are not filtered by tenant", table)
			}
			if withCheck == 0 {
				t.Errorf("%s has no policy with a WITH CHECK expression — one tenant can write "+
					"rows labelled with another tenant's id", table)
			}
		})
	}
}

// countRows runs one count on the restricted (crm_app) connection, binding
// tenantID the way scopedDB does. An empty tenantID binds nothing, which is
// the shape of an application code path that forgot to set the scope.
func countRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table, tenantID string) int {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin on restricted pool: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if tenantID != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
			t.Fatalf("set app.tenant_id: %v", err)
		}
	}
	var n int
	// The table name comes from the catalog, not from any caller input.
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM "`+table+`"`).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestUnscopedQuerySeesNothingInAnyTenantTable is the behavioural half: with
// real rows present in both tenants, a connection that binds no tenant — the
// bug an unscoped query would introduce — reads zero rows from every
// tenant-owned table, and a connection bound to tenant B reads none of
// tenant A's.
func TestUnscopedQuerySeesNothingInAnyTenantTable(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	pool := restrictedPool(t)

	a := seedTenant(ctx, t, s, "alpha")
	b := seedTenant(ctx, t, s, "beta")

	for _, table := range tenantTables(ctx, t, pool) {
		t.Run(table, func(t *testing.T) {
			ownRows := countRows(ctx, t, pool, table, a.scope.TenantID)
			if ownRows == 0 {
				t.Skipf("tenant alpha owns no rows in %s; nothing to leak from this table", table)
			}
			if n := countRows(ctx, t, pool, table, b.scope.TenantID); n >= ownRows+1 {
				t.Errorf("tenant beta reads %d rows of %s while alpha owns %d — counts overlap", n, table, ownRows)
			}
			if n := countRows(ctx, t, pool, table, ""); n != 0 {
				t.Errorf("an unscoped connection reads %d rows from %s; want 0 — "+
					"a query that forgets its tenant returns other tenants' data", n, table)
			}
			if n := countRows(ctx, t, pool, table, "tenant-that-does-not-exist"); n != 0 {
				t.Errorf("an unknown tenant reads %d rows from %s; want 0", n, table)
			}
		})
	}
}

// TestCrossTenantEntityAccessIsDenied walks the Store API the way a request
// does. Tenant beta names each of tenant alpha's record ids directly and must
// be told the record does not exist — not that it is forbidden, which would
// itself confirm the id is real.
func TestCrossTenantEntityAccessIsDenied(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	a := seedTenant(ctx, t, s, "victim")
	b := seedTenant(ctx, t, s, "attacker")

	notFound := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: tenant beta reached tenant alpha's record", what)
			return
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: error = %v, want ErrNotFound (any other answer confirms the id exists)", what, err)
		}
	}

	t.Run("reads", func(t *testing.T) {
		_, err := s.GetAccount(ctx, b.scope, a.accountID)
		notFound(t, "GetAccount", err)
		_, err = s.GetContact(ctx, b.scope, a.contactID)
		notFound(t, "GetContact", err)
		_, err = s.GetLead(ctx, b.scope, a.leadID)
		notFound(t, "GetLead", err)
		_, err = s.GetOpportunity(ctx, b.scope, a.opportunityID)
		notFound(t, "GetOpportunity", err)
		_, err = s.GetActivity(ctx, b.scope, a.activityID)
		notFound(t, "GetActivity", err)
		_, err = s.GetPipeline(ctx, b.scope, a.pipelineID)
		notFound(t, "GetPipeline", err)
		_, err = s.GetTask(ctx, b.scope, a.taskID)
		notFound(t, "GetTask", err)
		_, err = s.GetNote(ctx, b.scope, a.noteID)
		notFound(t, "GetNote", err)
	})

	t.Run("writes", func(t *testing.T) {
		_, err := s.UpdateAccount(ctx, b.scope, &Account{ID: a.accountID, Name: "Seized", OwnerID: "attacker"})
		notFound(t, "UpdateAccount", err)
		_, err = s.UpdateContact(ctx, b.scope, &Contact{ID: a.contactID, Email: "seized@example.com"})
		notFound(t, "UpdateContact", err)
		_, err = s.UpdateLead(ctx, b.scope, &Lead{ID: a.leadID, Company: "Seized"})
		notFound(t, "UpdateLead", err)
		_, err = s.UpdateOpportunity(ctx, b.scope, &Opportunity{ID: a.opportunityID, Name: "Seized"})
		notFound(t, "UpdateOpportunity", err)

		notFound(t, "DeleteAccount", s.DeleteAccount(ctx, b.scope, a.accountID))
		notFound(t, "DeleteContact", s.DeleteContact(ctx, b.scope, a.contactID))
		notFound(t, "DeleteLead", s.DeleteLead(ctx, b.scope, a.leadID))
		notFound(t, "DeleteOpportunity", s.DeleteOpportunity(ctx, b.scope, a.opportunityID))
		notFound(t, "DeleteActivity", s.DeleteActivity(ctx, b.scope, a.activityID))

		_, err = s.UpdateTask(ctx, b.scope, &Task{ID: a.taskID, Title: "Seized"})
		notFound(t, "UpdateTask", err)
		_, err = s.CompleteTask(ctx, b.scope, a.taskID, "attacker")
		notFound(t, "CompleteTask", err)
		_, err = s.ReopenTask(ctx, b.scope, a.taskID)
		notFound(t, "ReopenTask", err)
		notFound(t, "DeleteTask", s.DeleteTask(ctx, b.scope, a.taskID))

		_, err = s.UpdateNote(ctx, b.scope, &Note{ID: a.noteID, Body: "Seized"})
		notFound(t, "UpdateNote", err)
		notFound(t, "DeleteNote", s.DeleteNote(ctx, b.scope, a.noteID))
	})

	// The alpha records must still be intact: a denied write must not have
	// partially applied before the tenant check ran.
	t.Run("victim records survive", func(t *testing.T) {
		account, err := s.GetAccount(ctx, a.scope, a.accountID)
		if err != nil {
			t.Fatalf("victim can no longer read its own account: %v", err)
		}
		if account.Name == "Seized" {
			t.Error("a cross-tenant UpdateAccount was applied despite returning an error")
		}
		if _, err := s.GetActivity(ctx, a.scope, a.activityID); err != nil {
			t.Errorf("victim can no longer read its own activity: %v", err)
		}
	})

	t.Run("lists and searches", func(t *testing.T) {
		accounts, _, err := s.ListAccounts(ctx, b.scope, Page{Size: 500}, AccountFilter{}, SortAccountCreatedDesc)
		if err != nil {
			t.Fatalf("ListAccounts: %v", err)
		}
		for _, row := range accounts {
			if row.ID == a.accountID {
				t.Error("ListAccounts returned another tenant's account")
			}
		}
		// A name filter is the search surface accounts expose today: filtering
		// for the victim's name must not turn into a cross-tenant lookup.
		hits, _, err := s.ListAccounts(ctx, b.scope, Page{Size: 500},
			AccountFilter{NameContains: "victim Holdings"}, SortAccountCreatedDesc)
		if err != nil {
			t.Fatalf("ListAccounts(filtered): %v", err)
		}
		if len(hits) != 0 {
			t.Errorf("a name filter matched %d of another tenant's accounts", len(hits))
		}

		contacts, _, err := s.ListContacts(ctx, b.scope, Page{Size: 500}, ContactFilter{}, SortContactCreatedDesc)
		if err != nil {
			t.Fatalf("ListContacts: %v", err)
		}
		for _, row := range contacts {
			if row.ID == a.contactID {
				t.Error("ListContacts returned another tenant's contact")
			}
		}

		leads, _, err := s.ListLeads(ctx, b.scope, Page{Size: 500})
		if err != nil {
			t.Fatalf("ListLeads: %v", err)
		}
		for _, row := range leads {
			if row.ID == a.leadID {
				t.Error("ListLeads returned another tenant's lead")
			}
		}

		opportunities, _, err := s.ListOpportunities(ctx, b.scope, Page{Size: 500})
		if err != nil {
			t.Fatalf("ListOpportunities: %v", err)
		}
		for _, row := range opportunities {
			if row.ID == a.opportunityID {
				t.Error("ListOpportunities returned another tenant's opportunity")
			}
		}

		activities, _, err := s.ListActivities(ctx, b.scope, Page{Size: 500})
		if err != nil {
			t.Fatalf("ListActivities: %v", err)
		}
		for _, row := range activities {
			if row.ID == a.activityID {
				t.Error("ListActivities returned another tenant's activity")
			}
		}

		tasks, _, err := s.ListTasks(ctx, b.scope, Page{Size: 500}, TaskFilter{}, SortTaskDue)
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		for _, row := range tasks {
			if row.ID == a.taskID {
				t.Error("ListTasks returned another tenant's task")
			}
		}

		notes, _, err := s.ListNotes(ctx, b.scope, Page{Size: 500}, NoteFilter{})
		if err != nil {
			t.Fatalf("ListNotes: %v", err)
		}
		for _, row := range notes {
			if row.ID == a.noteID {
				t.Error("ListNotes returned another tenant's note")
			}
		}

		// Naming the victim's account as a subject filter must not turn into
		// a lookup into it.
		scoped, _, err := s.ListTasks(ctx, b.scope, Page{Size: 500},
			TaskFilter{SubjectType: SubjectAccount, SubjectID: a.accountID}, SortTaskDue)
		if err != nil {
			t.Fatalf("ListTasks(subject): %v", err)
		}
		if len(scoped) != 0 {
			t.Errorf("a subject filter naming another tenant's account matched %d tasks", len(scoped))
		}

		pipelines, err := s.ListPipelines(ctx, b.scope)
		if err != nil {
			t.Fatalf("ListPipelines: %v", err)
		}
		for _, row := range pipelines {
			if row.ID == a.pipelineID {
				t.Error("ListPipelines returned another tenant's pipeline")
			}
		}
	})

	// Every by-parent-id read is its own leak surface: these take an id
	// belonging to alpha and must return nothing rather than alpha's history.
	t.Run("child collections named by a foreign parent id", func(t *testing.T) {
		history, err := s.ListOwnershipHistory(ctx, b.scope, a.accountID)
		if err == nil && len(history) != 0 {
			t.Errorf("ListOwnershipHistory leaked %d rows of another tenant's account", len(history))
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			t.Errorf("ListOwnershipHistory: error = %v, want ErrNotFound or an empty result", err)
		}

		changes, err := s.ListStageHistory(ctx, b.scope, a.opportunityID)
		if err == nil && len(changes) != 0 {
			t.Errorf("ListStageHistory leaked %d rows of another tenant's opportunity", len(changes))
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			t.Errorf("ListStageHistory: error = %v, want ErrNotFound or an empty result", err)
		}

		timeline, err := s.ListActivitiesForSubject(ctx, b.scope, ActivitySubjectAccount, a.accountID)
		if err == nil && len(timeline) != 0 {
			t.Errorf("ListActivitiesForSubject leaked %d of another tenant's activities", len(timeline))
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			t.Errorf("ListActivitiesForSubject: error = %v, want ErrNotFound or an empty result", err)
		}

		stages, err := s.ListPipelineStages(ctx, b.scope, a.pipelineID)
		if err == nil && len(stages) != 0 {
			t.Errorf("ListPipelineStages leaked %d stages of another tenant's pipeline", len(stages))
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			t.Errorf("ListPipelineStages: error = %v, want ErrNotFound or an empty result", err)
		}

		events, err := s.OutboxEventsFor(ctx, b.scope, a.opportunityID)
		if err != nil {
			t.Fatalf("OutboxEventsFor: %v", err)
		}
		if len(events) != 0 {
			t.Errorf("OutboxEventsFor leaked %d of another tenant's events", len(events))
		}
	})

	// Lookup-by-value paths do not take an id, so an id check cannot save
	// them — they have to be tenant-scoped in the query itself.
	t.Run("lookups by value", func(t *testing.T) {
		victim, err := s.GetContact(ctx, a.scope, a.contactID)
		if err != nil {
			t.Fatalf("read victim contact: %v", err)
		}
		if _, err := s.FindContactByEmail(ctx, b.scope, victim.Email); !errors.Is(err, ErrNotFound) {
			t.Errorf("FindContactByEmail found another tenant's contact by address: err = %v", err)
		}

		account, err := s.GetAccount(ctx, a.scope, a.accountID)
		if err != nil {
			t.Fatalf("read victim account: %v", err)
		}
		if _, err := s.FindAccountByDomain(ctx, b.scope, websiteDomain(account.Website)); !errors.Is(err, ErrNotFound) {
			t.Errorf("FindAccountByDomain found another tenant's account by domain: err = %v", err)
		}
	})

	t.Run("summary counts only the caller's tenant", func(t *testing.T) {
		summary, err := s.Summary(ctx, b.scope)
		if err != nil {
			t.Fatalf("Summary: %v", err)
		}
		// beta seeded exactly one of each entity, so any other number means
		// the count either spans tenants or misses the caller's own rows.
		for _, entity := range []string{"account", "contact", "lead", "opportunity", "activity"} {
			if got := summary.Counts[entity]; got != 1 {
				t.Errorf("Summary counts %d %s rows for tenant beta; want exactly 1", got, entity)
			}
		}
		for _, entity := range []string{"task", "note"} {
			if got := summary.Counts[entity]; got != 1 {
				t.Errorf("Summary counts %d %s rows for tenant beta; want exactly 1", got, entity)
			}
		}
		if summary.Total != 7 {
			t.Errorf("Summary.Total = %d for tenant beta; want 7", summary.Total)
		}
	})

	t.Run("no scope at all is refused", func(t *testing.T) {
		if _, err := s.GetAccount(ctx, Scope{}, a.accountID); !errors.Is(err, ErrNoTenant) {
			t.Errorf("GetAccount with an empty scope: err = %v, want ErrNoTenant", err)
		}
		if _, _, err := s.ListAccounts(ctx, Scope{}, Page{}, AccountFilter{}, SortAccountCreatedDesc); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListAccounts with an empty scope: err = %v, want ErrNoTenant", err)
		}
	})
}

// TestCrossTenantReferenceIsRejected covers the subtler leak: not reading a
// foreign record, but pointing one of your own at it. A dangling reference
// that Postgres accepts would let tenant beta attach its contacts and
// activities to tenant alpha's account, and any later join would carry the
// data across.
func TestCrossTenantReferenceIsRejected(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	a := seedTenant(ctx, t, s, "refvictim")
	b := seedTenant(ctx, t, s, "refattacker")

	invalid := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: accepted a reference to another tenant's record", what)
			return
		}
		if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: error = %v, want ErrInvalid or ErrNotFound", what, err)
		}
	}

	_, err := s.CreateContact(ctx, b.scope, &Contact{
		Email: "cross@example.com", AccountID: a.accountID, FirstName: "Cross",
	})
	invalid(t, "CreateContact naming a foreign account", err)

	_, err = s.CreateOpportunity(ctx, b.scope, &Opportunity{
		Name: "Cross deal", AccountID: a.accountID,
	})
	invalid(t, "CreateOpportunity naming a foreign account", err)

	_, err = s.CreateAccount(ctx, b.scope, &Account{
		Name: "Cross child", OwnerID: "attacker", ParentID: a.accountID,
	})
	invalid(t, "CreateAccount parented to a foreign account", err)

	for _, subject := range []struct {
		kind string
		id   string
	}{
		{ActivitySubjectAccount, a.accountID},
		{ActivitySubjectContact, a.contactID},
		{ActivitySubjectLead, a.leadID},
		{ActivitySubjectOpportunity, a.opportunityID},
	} {
		_, err := s.CreateActivity(ctx, b.scope, &Activity{
			Kind: "note", SubjectType: subject.kind, SubjectID: subject.id,
			Notes: "cross-tenant probe", OccurredAt: time.Now().UTC(),
		})
		invalid(t, "CreateActivity on a foreign "+subject.kind, err)
	}

	_, err = s.CreateOpportunity(ctx, b.scope, &Opportunity{
		Name: "Cross pipeline deal", AccountID: b.accountID,
		PipelineID: a.pipelineID, StageID: a.stageID,
	})
	invalid(t, "CreateOpportunity bound to a foreign pipeline", err)

	_, err = s.CreateTask(ctx, b.scope, &Task{
		Title: "Cross task", SubjectType: SubjectAccount, SubjectID: a.accountID,
	})
	invalid(t, "CreateTask attached to a foreign account", err)

	_, err = s.CreateNote(ctx, b.scope, &Note{
		Body: "Cross note", SubjectType: SubjectAccount, SubjectID: a.accountID,
	})
	invalid(t, "CreateNote attached to a foreign account", err)

	_, err = s.CreatePipelineStage(ctx, b.scope, &PipelineStage{
		PipelineID: a.pipelineID, Name: "Injected", Position: 9, TerminalType: "open",
	})
	invalid(t, "CreatePipelineStage on a foreign pipeline", err)
}

// drainDSN rewrites the test DSN to connect as crm_drain — the BYPASSRLS
// role migration 0003 reserves for the outbox dispatcher's one legitimate
// cross-tenant query.
func drainDSN(t *testing.T) string {
	t.Helper()
	restricted := restrictedDSN(t)
	return strings.Replace(restricted, "crm_app:crm_app", "crm_drain:crm_drain", 1)
}

// TestOutboxDrainRoleIsTheOnlyCrossTenantReader pins down the asynchronous
// worker's privilege boundary at the database (task.md §1.3).
//
// Two things have to be true at once, and they pull in opposite directions:
// the dispatcher must be able to ask "which tenants have work?", which no
// single tenant's scope can answer — and it must not be able to read any
// tenant's rows without naming that tenant. The design splits those across
// two roles. This test proves the split is real rather than asserted, by
// running the same call on both pools and showing the answers differ.
func TestOutboxDrainRoleIsTheOnlyCrossTenantReader(t *testing.T) {
	owner := livePgStore(t)
	ctx := context.Background()

	a := seedTenant(ctx, t, owner, "drainalpha")
	b := seedTenant(ctx, t, owner, "drainbeta")

	// Misconfigured: no drain DSN, so DueOutboxTenants runs on the
	// RLS-scoped application pool. config.Validate rejects this in
	// production; cmd/crm warns at startup. The failure is silent — an
	// outbox that drains nothing looks exactly like one with no work.
	appOnly, err := NewPgStore(ctx, restrictedDSN(t))
	if err != nil {
		t.Skipf("cannot open a crm_app store: %v", err)
	}
	defer func() { _ = appOnly.Close() }()

	blind, err := appOnly.DueOutboxTenants(ctx, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxTenants on the application pool: %v", err)
	}
	for _, tenant := range blind {
		if tenant == a.scope.TenantID || tenant == b.scope.TenantID {
			t.Errorf("the RLS-scoped application pool discovered tenant %q — "+
				"either RLS is not applying to crm_app, or this test's premise is wrong", tenant)
		}
	}

	// Correctly configured: application pool as crm_app, discovery as crm_drain.
	drained, err := NewPgStoreWithDrain(ctx, restrictedDSN(t), drainDSN(t))
	if err != nil {
		t.Skipf("cannot open a crm_app + crm_drain store (migration 0003 creates crm_drain "+
			"only under a superuser): %v", err)
	}
	defer func() { _ = drained.Close() }()

	tenants, err := drained.DueOutboxTenants(ctx, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxTenants on the drain pool: %v", err)
	}
	found := map[string]bool{}
	for _, tenant := range tenants {
		found[tenant] = true
	}
	if !found[a.scope.TenantID] || !found[b.scope.TenantID] {
		t.Fatalf("the drain pool discovered %d tenants but not both seeded ones — "+
			"the dispatcher would never drain them", len(tenants))
	}

	// Discovery is where the cross-tenant privilege stops. Reading the events
	// themselves goes back through the application pool under a real scope.
	events, err := drained.DueOutboxEvents(ctx, a.scope, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxEvents(alpha): %v", err)
	}
	if len(events) == 0 {
		t.Fatal("alpha's own drain claimed no events; the rest of this test would pass vacuously")
	}
	for _, ev := range events {
		if ev.Envelope.TenantID != a.scope.TenantID {
			t.Errorf("draining alpha claimed an event owned by %q", ev.Envelope.TenantID)
		}
		if ev.ID == b.eventID {
			t.Error("draining alpha claimed beta's event")
		}
	}

	// Beta's event is invisible and unwritable from alpha's scope.
	foreign, err := drained.OutboxEventsFor(ctx, a.scope, b.opportunityID)
	if err != nil {
		t.Fatalf("OutboxEventsFor: %v", err)
	}
	if len(foreign) != 0 {
		t.Errorf("alpha read %d of beta's outbox events by naming beta's entity id", len(foreign))
	}

	err = drained.UpdateOutboxEvent(ctx, a.scope, &OutboxEvent{
		ID:       b.eventID,
		Status:   EventPublished,
		Envelope: Envelope{ID: b.eventID, Type: "opportunity.created", TenantID: b.scope.TenantID, SourceApp: SourceAppCRM, EntityType: "opportunity", EntityID: b.opportunityID, SourceVersion: 1, OccurredAt: time.Now().UTC()},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("alpha marking beta's event published: err = %v, want ErrNotFound", err)
	}

	// The drain role is read-only: it must not be able to write at all.
	drainPool, err := pgxpool.New(ctx, drainDSN(t))
	if err == nil {
		defer drainPool.Close()
		if err := drainPool.Ping(ctx); err == nil {
			_, err = drainPool.Exec(ctx,
				`UPDATE event_outbox SET status = 'failed' WHERE id = $1`, a.eventID)
			if err == nil {
				t.Error("the crm_drain role modified an outbox row; it is granted SELECT only " +
					"precisely so a cross-tenant reader can never become a cross-tenant writer")
			}
		}
	}
}
