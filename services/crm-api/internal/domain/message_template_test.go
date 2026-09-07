package domain

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func templateScope() Scope { return Scope{TenantID: "tenant-tpl", OrgID: "org-tpl"} }

// TestTemplateRefusesUnknownMergeFields is the test the allowlist exists for.
// A placeholder outside it would render as literal braces in a message to a
// customer — a failure the author hears about from the recipient, if at all.
func TestTemplateRefusesUnknownMergeFields(t *testing.T) {
	ok := &MessageTemplate{
		Name: "Intro", ChannelKind: ChannelWhatsApp,
		Body: "Hi {{contact.first_name}}, this is about {{account.name}}.",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a body using only allowlisted fields was refused: %v", err)
	}

	// Spacing inside the braces is an author's habit, not a different field.
	spaced := &MessageTemplate{
		Name: "Intro", ChannelKind: ChannelWhatsApp, Body: "Hi {{ contact.first_name }},",
	}
	if err := spaced.Validate(); err != nil {
		t.Errorf("a spaced placeholder was refused: %v", err)
	}

	bad := []struct {
		name string
		body string
	}{
		{"unknown field", "Hi {{contact.phone}},"},
		{"invented namespace", "Hi {{user.secret}},"},
		{"unclosed placeholder", "Hi {{contact.first_name,"},
		{"stray opener", "Hi {{ there"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			err := (&MessageTemplate{Name: "T", ChannelKind: ChannelSMS, Body: c.body}).Validate()
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted %q: %v", c.body, err)
			}
			// The message has to name what is allowed, or the author is left
			// guessing at a vocabulary they cannot see.
			if !strings.Contains(err.Error(), MergeContactFirstName) {
				t.Errorf("the refusal does not list the allowed fields: %v", err)
			}
		})
	}
}

func TestTemplateValidation(t *testing.T) {
	bad := []struct {
		name string
		in   *MessageTemplate
	}{
		{"no name", &MessageTemplate{ChannelKind: ChannelSMS, Body: "hi"}},
		{"no body", &MessageTemplate{Name: "T", ChannelKind: ChannelSMS}},
		{"unknown channel", &MessageTemplate{Name: "T", ChannelKind: "pigeon", Body: "hi"}},
		{
			// A dialer has nowhere to put a message, so a voice template
			// would be text nobody could send.
			"voice channel",
			&MessageTemplate{Name: "T", ChannelKind: ChannelVoice, Body: "hi"},
		},
		{
			// Only email has a subject line; storing one elsewhere would be a
			// field the sender never sees and the recipient never gets.
			"subject on a non-email template",
			&MessageTemplate{Name: "T", ChannelKind: ChannelSMS, Subject: "Hello", Body: "hi"},
		},
		{"body too long", &MessageTemplate{
			Name: "T", ChannelKind: ChannelSMS, Body: strings.Repeat("x", maxTemplateBodyLen+1),
		}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if err := c.in.Validate(); err == nil {
				t.Fatal("accepted a template that should have been refused")
			}
		})
	}

	email := &MessageTemplate{
		Name: "Follow-up", ChannelKind: ChannelEmail,
		Subject: "About {{account.name}}", Body: "Hi {{contact.first_name}},",
	}
	if err := email.Validate(); err != nil {
		t.Errorf("a valid email template was refused: %v", err)
	}
}

func TestTemplateStore(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := templateScope()

	created, err := s.CreateMessageTemplate(ctx, scope, &MessageTemplate{
		Name: "Intro", ChannelKind: ChannelWhatsApp, Body: "Hi {{contact.first_name}},",
		CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// One name per channel per tenant, case-insensitively.
	if _, err := s.CreateMessageTemplate(ctx, scope, &MessageTemplate{
		Name: "intro", ChannelKind: ChannelWhatsApp, Body: "Different wording",
	}); !errors.Is(err, ErrConflict) {
		t.Errorf("a duplicate name was accepted: %v", err)
	}

	// The same name on another channel is a different template: "Intro" is a
	// reasonable name for both a WhatsApp opener and an email.
	if _, err := s.CreateMessageTemplate(ctx, scope, &MessageTemplate{
		Name: "Intro", ChannelKind: ChannelEmail, Subject: "Hello", Body: "Hi,",
	}); err != nil {
		t.Errorf("the same name on another channel was refused: %v", err)
	}

	all, err := s.ListMessageTemplates(ctx, scope, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("listed %d templates, want 2", len(all))
	}
	whatsapp, err := s.ListMessageTemplates(ctx, scope, ChannelWhatsApp)
	if err != nil {
		t.Fatalf("list whatsapp: %v", err)
	}
	if len(whatsapp) != 1 || whatsapp[0].ChannelKind != ChannelWhatsApp {
		t.Errorf("channel filter returned %+v", whatsapp)
	}

	// created_by records who introduced the wording; a later editor does not
	// become its author.
	updated, err := s.UpdateMessageTemplate(ctx, scope, &MessageTemplate{
		ID: created.ID, Name: "Intro", ChannelKind: ChannelWhatsApp,
		Body: "Hi {{contact.full_name}},", CreatedBy: "somebody-else",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.CreatedBy != "user-1" {
		t.Errorf("created_by = %q after an edit, want it unchanged", updated.CreatedBy)
	}

	// An edit is validated too, or the allowlist would only guard creation.
	if _, err := s.UpdateMessageTemplate(ctx, scope, &MessageTemplate{
		ID: created.ID, Name: "Intro", ChannelKind: ChannelWhatsApp, Body: "Hi {{contact.phone}},",
	}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an edit introducing an unknown field was accepted: %v", err)
	}

	if err := s.DeleteMessageTemplate(ctx, scope, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetMessageTemplate(ctx, scope, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted template is still readable: %v", err)
	}

	other := Scope{TenantID: "tenant-other", OrgID: "org-other"}
	if rows, err := s.ListMessageTemplates(ctx, other, ""); err != nil || len(rows) != 0 {
		t.Errorf("another tenant sees %d templates (err %v), want 0", len(rows), err)
	}
}
