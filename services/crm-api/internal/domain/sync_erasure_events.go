package domain

import (
	"context"
	"time"
)

// EnqueueSyncErasureEvent records that synced content was erased.
//
// An erasure that leaves no trace cannot be shown to have happened, which is
// the first thing an auditor asks about one. The event carries counts and the
// scope and no content: an audit record that quoted the subject lines it had
// just erased would defeat the erasure it was recording.
//
// source_version is the event's own time in microseconds, for the same reason
// the Google connection events use it: several erasures can touch one scope
// over its life, so a fixed 1 would collide on the outbox's uniqueness key.
func EnqueueSyncErasureEvent(
	ctx context.Context,
	store Store,
	scope Scope,
	in EraseSubject,
	result ErasureResult,
	automatic bool,
	occurredAt time.Time,
) error {
	actor := in.Actor
	if actor == "" {
		actor = "system"
	}
	// The entity is the scope the erasure named, so every erasure touching
	// one contact is findable through OutboxEventsFor with that contact's id.
	entityID := in.SubjectID
	if entityID == "" {
		entityID = in.OwnerID
	}
	if entityID == "" {
		entityID = "retention"
	}

	env := Envelope{
		ID:            newID(),
		Type:          EventSyncContentErased,
		TenantID:      scope.TenantID,
		CorrelationID: newID(),
		SourceApp:     SourceAppCRM,
		EntityType:    EntitySyncErasure,
		EntityID:      entityID,
		SourceVersion: occurredAt.UTC().UnixMicro(),
		Actor:         actor,
		OccurredAt:    occurredAt.UTC(),
	}
	payload := SyncErasurePayload{
		SubjectType:         in.SubjectType,
		SubjectID:           in.SubjectID,
		OwnerID:             in.OwnerID,
		ActivitiesDeleted:   result.ActivitiesDeleted,
		MessagesRedacted:    result.MessagesRedacted,
		EventsRedacted:      result.EventsRedacted,
		AddressesSuppressed: result.AddressesSuppressed,
		Reason:              in.Reason,
		Automatic:           automatic,
	}
	if !in.Before.IsZero() {
		payload.Before = in.Before.UTC().Format(time.RFC3339)
	}
	env, err := env.WithPayload(payload)
	if err != nil {
		return err
	}
	_, err = EnqueueEvent(ctx, store, scope, env)
	return err
}
