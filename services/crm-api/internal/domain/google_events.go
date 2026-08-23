package domain

import (
	"context"
	"time"
)

// The Google connection's audit trail.
//
// These are durable outbox events rather than log lines because the questions
// they answer are the ones an incident asks months later: who linked a
// mailbox to this organization, when did access end, and did anyone purge the
// record of it. A log that has rotated cannot answer any of them.
//
// The payloads deliberately carry no credential material and no message
// content — only the account address the user themselves consented with,
// which they already see in the UI.

// EntityGoogleConnection identifies Google connection envelopes on this
// module's bus subject: crm.google_connection.<tenant> (Envelope.Subject).
const EntityGoogleConnection = "google_connection"

// The Google connection lifecycle events.
const (
	// EventGoogleConnectionAuthorized fires when a token exchange succeeds,
	// on a first connection and on every reconnection.
	EventGoogleConnectionAuthorized = EntityGoogleConnection + ".authorized"
	// EventGoogleConnectionReauthorizationRequired fires when a stored
	// credential stops working — consent withdrawn at Google, or a refresh
	// token that no longer refreshes.
	EventGoogleConnectionReauthorizationRequired = EntityGoogleConnection + ".reauthorization_required"
	// EventGoogleConnectionDisconnected fires when a user withdraws the
	// connection and the grant is revoked at Google.
	EventGoogleConnectionDisconnected = EntityGoogleConnection + ".disconnected"
	// EventGoogleConnectionPurged fires when the connection row itself is
	// deleted. The event outlives the row on purpose: that a purge happened
	// is exactly the fact a purge must not be able to erase.
	EventGoogleConnectionPurged = EntityGoogleConnection + ".purged"
)

// GoogleConnectionEventPayload is the event body.
type GoogleConnectionEventPayload struct {
	ConnectionID string   `json:"connection_id"`
	OwnerID      string   `json:"owner_id"`
	AccountEmail string   `json:"account_email,omitempty"`
	Status       string   `json:"status"`
	Scopes       []string `json:"scopes,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

// EnqueueGoogleConnectionEvent writes one durable audit event about a Google
// connection.
//
// source_version is the event's own time in microseconds. The outbox has a
// unique index on (tenant, entity_type, entity_id, source_version), so the
// version has to increase across the several events one connection
// accumulates over its life; a fixed 1 — which is right for a transition that
// can only happen once, like opportunity.closed_won — would make the second
// event about a connection collide with the first. Each of these events
// follows its own durable write, so two cannot share a microsecond.
func EnqueueGoogleConnectionEvent(
	ctx context.Context,
	store Store,
	scope Scope,
	connection *GoogleConnection,
	eventType, actor, reason string,
	occurredAt time.Time,
) error {
	if connection == nil {
		return ErrInvalid
	}
	if actor == "" {
		actor = "system"
	}
	env := Envelope{
		ID:            newID(),
		Type:          eventType,
		TenantID:      scope.TenantID,
		CorrelationID: newID(),
		SourceApp:     SourceAppCRM,
		EntityType:    EntityGoogleConnection,
		EntityID:      connection.ID,
		SourceVersion: occurredAt.UTC().UnixMicro(),
		Actor:         actor,
		OccurredAt:    occurredAt.UTC(),
	}
	env, err := env.WithPayload(GoogleConnectionEventPayload{
		ConnectionID: connection.ID,
		OwnerID:      connection.OwnerID,
		AccountEmail: connection.GoogleAccountEmail,
		Status:       connection.Status,
		Scopes:       connection.GrantedScopes,
		Reason:       reason,
	})
	if err != nil {
		return err
	}
	_, err = EnqueueEvent(ctx, store, scope, env)
	return err
}
