package domain

import (
	"context"
	"errors"
	"testing"
)

func outboundScope() Scope { return Scope{TenantID: "tenant-out", OrgID: "org-out"} }

// TestRenderMergeFieldsMatchesTheBrowser pins the property that makes the
// preview in the template editor honest: the server renders the same words
// the browser does.
//
// The two implementations are separate on purpose — the browser substitutes
// from the record on screen, the server from the record a rule fired on — but
// a divergence means a rep approves wording in the editor and a customer
// receives something else. The fixtures here are deliberately the same ones
// apps/web/src/lib/messageTemplates.test.ts uses.
func TestRenderMergeFieldsMatchesTheBrowser(t *testing.T) {
	values := map[string]string{
		MergeContactFirstName: "Bob",
		MergeContactLastName:  "Buyer",
		MergeContactFullName:  "Bob Buyer",
		MergeContactEmail:     "bob@example.com",
		MergeContactCompany:   "Acme Holdings",
		MergeSenderEmail:      "rep@example.com",
	}
	cases := []struct{ body, want string }{
		{
			"Hi {{contact.first_name}}, this is {{sender.email}} about {{account.name}}.",
			"Hi Bob, this is rep@example.com about Acme Holdings.",
		},
		{"Hi {{ contact.first_name }},", "Hi Bob,"},
		{"Hi {{contact.first_name}},\n\nAbout the renewal.", "Hi Bob,\n\nAbout the renewal."},
		{"Just checking in.", "Just checking in."},
	}
	for _, c := range cases {
		if got := RenderMergeFields(c.body, values); got != c.want {
			t.Errorf("render(%q) = %q, want %q", c.body, got, c.want)
		}
	}
}

func TestRenderMergeFieldsDoesNotSendHiComma(t *testing.T) {
	// The failure this exists for: a lead has no name at all, so every name
	// placeholder resolves to nothing. "Hi ," reaching a customer is very
	// visible and entirely avoidable — and inventing a name from an email
	// local part would put a guess in a message instead.
	empty := MergeValuesForLead(&Lead{ContactEmail: "alex@example.com", Company: "Example Ltd"}, "")
	if got := RenderMergeFields("Hi {{contact.first_name}},", empty); got != "Hi," {
		t.Errorf("render with no name = %q, want %q", got, "Hi,")
	}
	if got := RenderMergeFields("Hi {{contact.first_name}} {{contact.last_name}}, about {{account.name}}.", empty); got != "Hi, about Example Ltd." {
		t.Errorf("render = %q, want the gap collapsed", got)
	}
	// An unknown placeholder renders empty rather than as literal braces.
	if got := RenderMergeFields("Hi {{contact.nickname}}!", empty); got != "Hi!" {
		t.Errorf("unknown field rendered as %q", got)
	}
}

func TestMergeValuesForContactBuildsAFullName(t *testing.T) {
	v := MergeValuesForContact(&Contact{FirstName: "Bob", LastName: ""}, "", "")
	if v[MergeContactFullName] != "Bob" {
		t.Errorf("full name = %q, want %q with no stray space", v[MergeContactFullName], "Bob")
	}
	if got := MergeValuesForContact(nil, "", ""); len(got) != 0 {
		t.Errorf("values for no contact = %v, want empty", got)
	}
}

func TestDraftOutboundMessageIsIdempotentPerExecution(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := outboundScope()

	draft := &OutboundMessage{
		SubjectType: SubjectLead, SubjectID: "lead-1",
		ChannelKind: ChannelEmail, Address: "alex@example.com",
		Body: "Hi,", AssigneeID: "rep-1", ExecutionID: "exec-1",
	}

	first, created, err := s.DraftOutboundMessage(ctx, scope, draft)
	if err != nil || !created {
		t.Fatalf("first draft: created=%v err=%v", created, err)
	}
	if first.Status != OutboundPending {
		t.Errorf("status = %q, want %q", first.Status, OutboundPending)
	}

	// The engine re-runs an action if it crashes between performing it and
	// persisting actions_completed. A second draft here means the customer
	// gets the same message twice, and a rep sends both.
	again, created, err := s.DraftOutboundMessage(ctx, scope, draft)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if created {
		t.Error("a re-run of the same execution drafted a second message")
	}
	if again.ID != first.ID {
		t.Errorf("re-run returned %q, want the original %q", again.ID, first.ID)
	}

	all, err := s.ListOutboundMessages(ctx, scope, OutboundMessageFilter{}, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("queue holds %d drafts, want 1", len(all))
	}
}

func TestResolveOutboundMessageClaimsOnce(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := outboundScope()

	draft, _, err := s.DraftOutboundMessage(ctx, scope, &OutboundMessage{
		SubjectType: SubjectLead, SubjectID: "lead-1",
		ChannelKind: ChannelEmail, Address: "alex@example.com", Body: "Hi,",
	})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}

	sent, err := s.ResolveOutboundMessage(ctx, scope, draft.ID, OutboundSent, "rep-1", "")
	if err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if sent.SentBy != "rep-1" || sent.SentAt.IsZero() {
		t.Errorf("sent message = %+v, want it attributed — otherwise "+
			"'who contacted this customer' has no answer", sent)
	}

	// Two reps working the same queue must not both claim one draft.
	if _, err := s.ResolveOutboundMessage(ctx, scope, draft.ID, OutboundSent, "rep-2", ""); !errors.Is(err, ErrConflict) {
		t.Errorf("second claim: %v, want ErrConflict", err)
	}

	if _, err := s.ResolveOutboundMessage(ctx, scope, draft.ID, "posted", "rep-1", ""); err == nil {
		t.Error("an unknown status was accepted")
	}
}

func TestOutboundMessageValidation(t *testing.T) {
	bad := []struct {
		name string
		in   *OutboundMessage
	}{
		{"no body", &OutboundMessage{SubjectType: SubjectLead, SubjectID: "l", ChannelKind: ChannelEmail, Address: "a@b.com"}},
		{"no address", &OutboundMessage{SubjectType: SubjectLead, SubjectID: "l", ChannelKind: ChannelEmail, Body: "hi"}},
		{
			// An Opportunity has no address of its own, and choosing one of
			// its account's contacts is a decision nobody has made.
			"unmessageable subject",
			&OutboundMessage{SubjectType: SubjectOpportunity, SubjectID: "o", ChannelKind: ChannelEmail, Address: "a@b.com", Body: "hi"},
		},
		{
			// Voice has nowhere to put a message.
			"voice channel",
			&OutboundMessage{SubjectType: SubjectLead, SubjectID: "l", ChannelKind: ChannelVoice, Address: "+441632960001", Body: "hi"},
		},
		{
			"sent with nobody attached",
			&OutboundMessage{SubjectType: SubjectLead, SubjectID: "l", ChannelKind: ChannelEmail, Address: "a@b.com", Body: "hi", Status: OutboundSent},
		},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if err := c.in.Validate(); err == nil {
				t.Fatal("accepted a draft that should have been refused")
			}
		})
	}
}

func TestDraftMessageActionValidation(t *testing.T) {
	ok := WorkflowAction{Type: ActionDraftMessage, ChannelKind: ChannelEmail, TemplateID: "tpl-1"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid draft_message action was refused: %v", err)
	}

	bad := []struct {
		name   string
		action WorkflowAction
	}{
		{"no template", WorkflowAction{Type: ActionDraftMessage, ChannelKind: ChannelEmail}},
		{"no channel", WorkflowAction{Type: ActionDraftMessage, TemplateID: "tpl-1"}},
		{"voice channel", WorkflowAction{Type: ActionDraftMessage, ChannelKind: ChannelVoice, TemplateID: "tpl-1"}},
		{
			// The draft goes to whoever owns the record when the rule runs;
			// naming an assignee too would give the action two answers.
			"also names an assignee",
			WorkflowAction{Type: ActionDraftMessage, ChannelKind: ChannelEmail, TemplateID: "tpl-1", Assignee: AssigneeRecordOwner},
		},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if err := c.action.Validate(); err == nil {
				t.Fatal("accepted an action that should have been refused")
			}
		})
	}
}

// TestDraftMessageIsInTheAuthoringCatalog stops the action existing in the
// engine while no editor can offer it.
func TestDraftMessageIsInTheAuthoringCatalog(t *testing.T) {
	for _, actionType := range WorkflowActionTypes() {
		if actionType == ActionDraftMessage {
			return
		}
	}
	t.Errorf("draft_message is missing from WorkflowActionTypes() %v", WorkflowActionTypes())
}
