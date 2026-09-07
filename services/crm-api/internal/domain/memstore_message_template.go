package domain

import (
	"context"
	"fmt"
	"sort"
)

// The in-memory half of message templates (message_template.go,
// migrations/0030_message_templates.sql).

// CreateMessageTemplate stores new wording. Names are unique per channel per
// tenant, case-insensitively — the same property the migration's index
// enforces, checked here so a collision reads the same way under either
// store rather than one racing to a database error the other cannot produce.
func (s *MemStore) CreateMessageTemplate(_ context.Context, scope Scope, v *MessageTemplate) (*MessageTemplate, error) {
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMessageTemplate(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if err := s.templateNameFree(scope, record.ChannelKind, record.Name, ""); err != nil {
		return nil, err
	}
	return s.messageTemplate.create(scope, record)
}

// GetMessageTemplate reads one template.
func (s *MemStore) GetMessageTemplate(_ context.Context, scope Scope, id string) (*MessageTemplate, error) {
	return s.messageTemplate.get(scope, id)
}

// ListMessageTemplates returns the tenant's templates, optionally narrowed to
// one channel, ordered by name.
func (s *MemStore) ListMessageTemplates(_ context.Context, scope Scope, channelKind string) ([]*MessageTemplate, error) {
	all, err := s.messageTemplate.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*MessageTemplate, 0, len(all))
	for _, template := range all {
		if channelKind != "" && template.ChannelKind != channelKind {
			continue
		}
		out = append(out, cloneMessageTemplate(template))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChannelKind != out[j].ChannelKind {
			return out[i].ChannelKind < out[j].ChannelKind
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UpdateMessageTemplate rewrites one template's wording.
func (s *MemStore) UpdateMessageTemplate(_ context.Context, scope Scope, v *MessageTemplate) (*MessageTemplate, error) {
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMessageTemplate(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.messageTemplate.get(scope, record.ID)
	if err != nil {
		return nil, err
	}
	if err := s.templateNameFree(scope, record.ChannelKind, record.Name, record.ID); err != nil {
		return nil, err
	}
	record.CreatedBy = existing.CreatedBy
	return s.messageTemplate.update(scope, record)
}

// DeleteMessageTemplate removes one. There is no archive state: a template
// carries no history anything depends on — a message that was sent is an
// Activity, not a reference to the wording it came from.
func (s *MemStore) DeleteMessageTemplate(_ context.Context, scope Scope, id string) error {
	return s.messageTemplate.delete(scope, id)
}

func (s *MemStore) templateNameFree(scope Scope, channelKind, name, exceptID string) error {
	all, err := s.messageTemplate.all(scope)
	if err != nil {
		return err
	}
	for _, existing := range all {
		if existing.ID == exceptID || existing.ChannelKind != channelKind {
			continue
		}
		if templateNameKey(existing.Name) == templateNameKey(name) {
			return fmt.Errorf("%w: a %s template named %q already exists",
				ErrConflict, channelKind, name)
		}
	}
	return nil
}
