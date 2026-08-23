package gmail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// The sync engine, against a stub Gmail.
//
// The first test in this file is the one that matters most. Everything else
// here is correctness; that one is the difference between a CRM feature and a
// privacy incident.

// stubAPI is a Gmail that answers from fields the test sets, and records what
// it was asked — including, crucially, whether anything ever requested a
// message body.
type stubAPI struct {
	profile  Profile
	messages map[string]Message
	// pages maps a page token to the ids on that page and the next token.
	pages       []stubPage
	history     []HistoryPage
	historyErr  error
	listCalls   int
	getCalls    []string
	profileCall int
}

type stubPage struct {
	ids  []string
	next string
}

func (s *stubAPI) Profile(context.Context, string) (Profile, error) {
	s.profileCall++
	return s.profile, nil
}

func (s *stubAPI) ListMessageIDs(_ context.Context, _, _, pageToken string, _ int) ([]string, string, error) {
	s.listCalls++
	index := 0
	if pageToken != "" {
		for i, page := range s.pages {
			if page.next == pageToken {
				index = i + 1
				break
			}
		}
	}
	if index >= len(s.pages) {
		return nil, "", nil
	}
	return s.pages[index].ids, s.pages[index].next, nil
}

func (s *stubAPI) GetMessage(_ context.Context, _, id string) (Message, error) {
	s.getCalls = append(s.getCalls, id)
	message, ok := s.messages[id]
	if !ok {
		return Message{}, errors.New("stub: no such message")
	}
	return message, nil
}

func (s *stubAPI) History(_ context.Context, _, _, pageToken string) (HistoryPage, error) {
	if s.historyErr != nil {
		return HistoryPage{}, s.historyErr
	}
	index := 0
	if pageToken != "" {
		for i, page := range s.history {
			if page.NextPageToken == pageToken {
				index = i + 1
				break
			}
		}
	}
	if index >= len(s.history) {
		return HistoryPage{}, nil
	}
	return s.history[index], nil
}

// staticToken is a TokenSource that always works, or always fails.
type staticToken struct{ err error }

func (t staticToken) AccessToken(context.Context, domain.Scope, string) (string, error) {
	if t.err != nil {
		return "", t.err
	}
	return "access-token", nil
}

const mailboxOwner = "user-1"
const mailboxAddress = "rep@ourcompany.example"

func testScope() domain.Scope { return domain.Scope{TenantID: "tenant-1", OrgID: "org-1"} }

// syncFixture builds a store holding one connected mailbox and one Contact.
func syncFixture(t *testing.T) (domain.Store, *domain.Contact) {
	t.Helper()
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := testScope()

	if _, err := store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
		OwnerID:            mailboxOwner,
		GoogleAccountEmail: mailboxAddress,
		Status:             domain.GoogleStatusConnected,
		GrantedScopes:      []string{"https://www.googleapis.com/auth/gmail.readonly"},
		TokenCiphertext:    []byte("sealed"),
	}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	account, err := store.CreateAccount(ctx, scope, &domain.Account{Name: "Acme", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := store.CreateContact(ctx, scope, &domain.Contact{
		Email: "bea@acme.example", AccountID: account.ID, FirstName: "Bea", LastName: "Buyer",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return store, contact
}

func message(id, from, to, subject string, at time.Time, labels ...string) Message {
	return Message{
		ID: id, ThreadID: "thread-" + id, Subject: subject,
		From: from, To: []string{to}, SentAt: at, LabelIDs: labels,
	}
}

// TestOnlyMessagesInvolvingAContactAreRecorded is the load-bearing test in
// this package.
//
// A connected mailbox contains the user's whole correspondence: their
// doctor, their bank, their family. Every CRM Activity is readable by
// colleagues holding crm:activity:read. If sync writes anything at all about
// a message with no CRM counterparty, this feature publishes a person's
// private mail to their employer.
func TestOnlyMessagesInvolvingAContactAreRecorded(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	api := &stubAPI{
		profile: Profile{EmailAddress: mailboxAddress, HistoryID: "1000"},
		pages:   []stubPage{{ids: []string{"m-crm", "m-doctor", "m-bank"}}},
		messages: map[string]Message{
			"m-crm":    message("m-crm", "bea@acme.example", mailboxAddress, "Renewal terms", at),
			"m-doctor": message("m-doctor", "surgery@clinic.example", mailboxAddress, "Your test results", at),
			"m-bank":   message("m-bank", "alerts@bank.example", mailboxAddress, "Overdraft notice", at),
		},
	}
	syncer := NewSyncer(store, api, staticToken{}, nil)

	result, err := syncer.SyncMailbox(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.MessagesExamined != 3 {
		t.Errorf("examined %d messages, want 3", result.MessagesExamined)
	}
	if result.MessagesMatched != 1 || result.EntriesCreated != 1 {
		t.Errorf("matched %d / created %d, want 1 / 1", result.MessagesMatched, result.EntriesCreated)
	}

	// Only the CRM message left a trace. The other two must be absent
	// everywhere — not stored under a different subject, not counted, not
	// recorded by subject line.
	entries, err := store.ListGmailMessagesForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stored %d entries, want 1", len(entries))
	}
	if entries[0].MessageSubject != "Renewal terms" {
		t.Errorf("stored subject = %q", entries[0].MessageSubject)
	}

	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.MessagesIndexed != 1 {
		t.Errorf("indexed counter = %d, want 1 — unmatched mail must leave no trace at all",
			state.MessagesIndexed)
	}

	// And nothing anywhere in the store mentions the private correspondence.
	activities, _, err := store.ListActivities(ctx, scope, domain.Page{Size: 500})
	if err != nil {
		t.Fatalf("list activities: %v", err)
	}
	for _, activity := range activities {
		if strings.Contains(activity.Notes, "test results") || strings.Contains(activity.Notes, "Overdraft") {
			t.Fatalf("a message with no CRM counterparty reached the timeline: %q", activity.Notes)
		}
	}
}

// TestTimelineEntryDescribesTheMessage checks what a person actually sees.
func TestTimelineEntryDescribesTheMessage(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	at := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)

	api := &stubAPI{
		profile: Profile{HistoryID: "1000"},
		pages:   []stubPage{{ids: []string{"m-in", "m-out"}}},
		messages: map[string]Message{
			"m-in":  message("m-in", "Bea Buyer <bea@acme.example>", mailboxAddress, "Renewal terms", at),
			"m-out": message("m-out", mailboxAddress, "bea@acme.example", "Re: Renewal terms", at.Add(time.Hour), "SENT"),
		},
	}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("sync: %v", err)
	}

	entries, err := store.ListGmailMessagesForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("stored %d entries, want 2", len(entries))
	}
	// Newest first.
	if entries[0].Direction != domain.GmailOutbound {
		t.Errorf("the message this mailbox sent is recorded as %q", entries[0].Direction)
	}
	if entries[1].Direction != domain.GmailInbound {
		t.Errorf("the received message is recorded as %q", entries[1].Direction)
	}
	if entries[1].FromAddress != "bea@acme.example" {
		t.Errorf("from = %q; the display name should be stripped", entries[1].FromAddress)
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 2 {
		t.Fatalf("timeline holds %d entries, want 2", len(activities))
	}
	for _, activity := range activities {
		if activity.Kind != domain.GmailActivityKind {
			t.Errorf("activity kind = %q, want %q — a synced email must be distinguishable "+
				"from one a person logged by hand", activity.Kind, domain.GmailActivityKind)
		}
		if !strings.Contains(activity.Notes, "Renewal terms") {
			t.Errorf("timeline entry does not say what the email was about: %q", activity.Notes)
		}
	}
}

// TestReSyncingCreatesNoDuplicates: overlapping history windows and resumed
// backfills mean sync re-reads messages routinely.
func TestReSyncingCreatesNoDuplicates(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	at := time.Now().UTC().Add(-time.Hour)

	api := &stubAPI{
		profile:  Profile{HistoryID: "1000"},
		pages:    []stubPage{{ids: []string{"m-1"}}},
		messages: map[string]Message{"m-1": message("m-1", "bea@acme.example", mailboxAddress, "Hello", at)},
	}
	syncer := NewSyncer(store, api, staticToken{}, nil)

	if _, err := syncer.SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	// Force a second backfill over the same messages, the way an expired
	// history cursor would.
	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	state.BackfillComplete, state.BackfillPageToken = false, ""
	if _, err := store.SaveGmailSyncState(ctx, scope, state); err != nil {
		t.Fatalf("reset state: %v", err)
	}

	second, err := syncer.SyncMailbox(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.EntriesCreated != 0 {
		t.Errorf("a re-sync created %d entries, want 0", second.EntriesCreated)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Errorf("timeline holds %d entries after two syncs of one message, want 1", len(activities))
	}
}

// TestBackfillTakesItsCursorBeforeWalking. Reading the history id after the
// walk would lose every message that arrived during it.
func TestBackfillTakesItsCursorBeforeWalking(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	api := &stubAPI{
		profile:  Profile{HistoryID: "5000"},
		pages:    []stubPage{{ids: []string{"m-1"}}},
		messages: map[string]Message{"m-1": message("m-1", "bea@acme.example", mailboxAddress, "Hi", time.Now())},
	}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if api.profileCall == 0 {
		t.Fatal("the backfill never read a history cursor, so incremental sync has no starting point")
	}
	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.LastHistoryID != "5000" {
		t.Errorf("stored cursor = %q, want the value read before the walk", state.LastHistoryID)
	}
	if !state.BackfillComplete {
		t.Error("a backfill that walked every page did not mark itself complete")
	}
}

// TestIncrementalSyncPicksUpWhereItLeftOff.
func TestIncrementalSyncPicksUpWhereItLeftOff(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	at := time.Now().UTC()

	api := &stubAPI{
		profile:  Profile{HistoryID: "1000"},
		pages:    []stubPage{{ids: []string{}}},
		messages: map[string]Message{},
	}
	syncer := NewSyncer(store, api, staticToken{}, nil)
	if _, err := syncer.SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// A message arrives, reported through history rather than a list walk.
	api.messages["m-new"] = message("m-new", "bea@acme.example", mailboxAddress, "New thread", at)
	api.history = []HistoryPage{{MessageIDs: []string{"m-new"}, HistoryID: "1100"}}

	result, err := syncer.SyncMailbox(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("incremental sync: %v", err)
	}
	if result.EntriesCreated != 1 {
		t.Errorf("incremental sync created %d entries, want 1", result.EntriesCreated)
	}
	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.LastHistoryID != "1100" {
		t.Errorf("cursor = %q, want it advanced to 1100", state.LastHistoryID)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Errorf("timeline holds %d entries, want 1", len(activities))
	}
}

// TestAnExpiredCursorRestartsABackfill. Resuming from a cursor Gmail has
// forgotten would silently skip everything in the gap.
func TestAnExpiredCursorRestartsABackfill(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	if _, err := store.SaveGmailSyncState(ctx, scope, &domain.GmailSyncState{
		OwnerID: mailboxOwner, LastHistoryID: "1", BackfillComplete: true,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	api := &stubAPI{historyErr: ErrHistoryExpired, profile: Profile{HistoryID: "9000"}}
	_, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, mailboxOwner)
	if !errors.Is(err, ErrHistoryExpired) {
		t.Fatalf("sync: %v, want ErrHistoryExpired", err)
	}

	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.BackfillComplete || state.LastHistoryID != "" {
		t.Error("an expired cursor left the mailbox believing it was up to date")
	}
	if state.LastError != ReasonHistoryExpired {
		t.Errorf("recorded reason = %q, want %q", state.LastError, ReasonHistoryExpired)
	}
	// The lease is released even though the pass failed, or the mailbox
	// would be skipped until it expired.
	if !state.SyncingUntil.IsZero() {
		t.Error("a failed pass kept its lease")
	}
}

// TestPartialConsentIsReportedNotRetriedForever: a user may grant Calendar
// and decline Gmail. The connection is healthy; this sync simply cannot run.
func TestPartialConsentIsReportedNotRetriedForever(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	connection, err := store.GetGoogleConnection(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	connection.GrantedScopes = []string{"https://www.googleapis.com/auth/calendar.events"}
	if _, err := store.SaveGoogleConnection(ctx, scope, connection); err != nil {
		t.Fatalf("narrow consent: %v", err)
	}

	api := &stubAPI{}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, mailboxOwner); err == nil {
		t.Fatal("sync ran without mailbox read consent")
	}
	if len(api.getCalls) != 0 || api.listCalls != 0 {
		t.Error("sync called Gmail despite not holding mailbox read consent")
	}
	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.LastError != ReasonNotConnected {
		t.Errorf("recorded reason = %q, want %q", state.LastError, ReasonNotConnected)
	}
}

// TestADisconnectedMailboxIsNotSynced.
func TestADisconnectedMailboxIsNotSynced(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	api := &stubAPI{}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, "nobody"); err == nil {
		t.Fatal("sync ran for a mailbox with no connection")
	}
	if api.listCalls != 0 {
		t.Error("sync called Gmail for a mailbox with no connection")
	}
}

// TestFailuresRecordABoundedReasonNotAProviderMessage. The stored reason is
// shown in the UI and used as a metric label.
func TestFailuresRecordABoundedReasonNotAProviderMessage(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	const leak = "quota exceeded for project 552301975147 user rep@ourcompany.example"
	tokens := staticToken{err: errors.New(leak)}
	if _, err := NewSyncer(store, &stubAPI{}, tokens, nil).SyncMailbox(ctx, scope, mailboxOwner); err == nil {
		t.Fatal("sync succeeded with no usable token")
	}

	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.LastError != ReasonTokenUnavailable {
		t.Errorf("recorded reason = %q, want %q", state.LastError, ReasonTokenUnavailable)
	}
	if strings.Contains(state.LastError, "quota") || strings.Contains(state.LastError, "@") {
		t.Errorf("the stored reason carries provider detail: %q", state.LastError)
	}
}

// TestALeasedMailboxIsNotSyncedTwice.
func TestALeasedMailboxIsNotSyncedTwice(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	if _, err := store.ClaimGmailSync(ctx, scope, mailboxOwner, time.Now().UTC(), time.Hour); err != nil {
		t.Fatalf("pre-claim: %v", err)
	}
	api := &stubAPI{}
	_, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, mailboxOwner)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("sync of a leased mailbox: %v, want ErrConflict", err)
	}
	if api.listCalls != 0 {
		t.Error("a second replica walked a mailbox another was already syncing")
	}
}

// TestOnePassIsBounded so one large mailbox cannot starve the others. The
// cursor makes stopping early free.
func TestOnePassIsBounded(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	// Three pages of ids, more than one pass may examine.
	api := &stubAPI{profile: Profile{HistoryID: "1"}, messages: map[string]Message{}}
	at := time.Now().UTC()
	for page := 0; page < 3; page++ {
		var ids []string
		for i := 0; i < listPageSize; i++ {
			id := string(rune('a'+page)) + "-" + time.Duration(i).String()
			ids = append(ids, id)
			api.messages[id] = message(id, "stranger@nowhere.example", mailboxAddress, "Noise", at)
		}
		next := ""
		if page < 2 {
			next = "page-" + string(rune('a'+page))
		}
		api.pages = append(api.pages, stubPage{ids: ids, next: next})
	}

	result, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.MessagesExamined > messagesPerPass {
		t.Errorf("one pass examined %d messages, want at most %d", result.MessagesExamined, messagesPerPass)
	}
	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.BackfillComplete {
		t.Error("a bounded pass claimed the backfill was finished")
	}
	if state.BackfillPageToken == "" {
		t.Error("a bounded pass saved no resume point, so the next one restarts from the beginning")
	}
}

// TestParticipantsExcludeTheMailboxOwner. People are often Contacts in their
// own organization's CRM; without this every message would match them.
func TestParticipantsExcludeTheMailboxOwner(t *testing.T) {
	m := Message{
		From: "Rep <" + mailboxAddress + ">",
		To:   []string{"Bea Buyer <bea@acme.example>, carol@acme.example"},
		Cc:   []string{mailboxAddress},
	}
	got := m.Participants(mailboxAddress)
	want := []string{"bea@acme.example", "carol@acme.example"}
	if len(got) != len(want) {
		t.Fatalf("participants = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("participants[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestMalformedHeadersDoNotFailASync. Mail in the wild contains a great deal
// of unparseable From.
func TestMalformedHeadersDoNotFailASync(t *testing.T) {
	m := Message{From: "not an address at all", To: []string{"bea@acme.example"}}
	got := m.Participants(mailboxAddress)
	if len(got) != 1 || got[0] != "bea@acme.example" {
		t.Errorf("participants = %v, want just the parseable one", got)
	}
}

// TestSubjectlessMailStillReadsSensibly.
func TestSubjectlessMailStillReadsSensibly(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	api := &stubAPI{
		profile:  Profile{HistoryID: "1"},
		pages:    []stubPage{{ids: []string{"m-1"}}},
		messages: map[string]Message{"m-1": message("m-1", "bea@acme.example", mailboxAddress, "   ", time.Now())},
	}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("sync: %v", err)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 1 || !strings.Contains(activities[0].Notes, "(no subject)") {
		t.Errorf("timeline entry for a subjectless email = %+v", activities)
	}
}

// TestAnEnormousSubjectIsBounded: an unbounded header is an unbounded row.
func TestAnEnormousSubjectIsBounded(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	huge := strings.Repeat("subject ", 5000)
	api := &stubAPI{
		profile:  Profile{HistoryID: "1"},
		pages:    []stubPage{{ids: []string{"m-1"}}},
		messages: map[string]Message{"m-1": message("m-1", "bea@acme.example", mailboxAddress, huge, time.Now())},
	}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("sync: %v", err)
	}
	entries, err := store.ListGmailMessagesForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len([]rune(entries[0].MessageSubject)) > maxSubjectLength {
		t.Errorf("stored subject is %d runes, want at most %d",
			len([]rune(entries[0].MessageSubject)), maxSubjectLength)
	}
}
