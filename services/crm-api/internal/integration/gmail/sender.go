package gmail

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/santoshguru/crm/internal/domain"
)

// Sending a message as a connected mailbox (task_crm.md §4, the gmail.send
// rung).
//
// This is deliberately its own type rather than a method on Syncer. Sync
// walks mailboxes on a timer and writes CRM records; this sends one message
// because a person pressed a button. They share a client and a token source
// and nothing else, and giving Syncer a Send method would invite a future
// change to send from inside a sync pass — which is exactly the automatic
// sending that would make quiet hours (task_crm.md D6) mandatory.

// SendAPI is the part of the Gmail client sending uses. Tests substitute it.
type SendAPI interface {
	Send(ctx context.Context, accessToken string, req SendRequest) (string, error)
}

// Sender delivers one message as one connected mailbox.
type Sender struct {
	store  domain.Store
	api    SendAPI
	tokens TokenSource
}

// NewSender wires sending. It is only constructed when Google is configured;
// a nil Sender is how the rest of the service asks "can this deployment send
// at all", the same way a nil Syncer answers "can it read".
func NewSender(store domain.Store, api SendAPI, tokens TokenSource) *Sender {
	return &Sender{store: store, api: api, tokens: tokens}
}

// ErrNoMailbox means the acting user has not connected a Google account, so
// there is nothing to send as.
var ErrNoMailbox = errors.New("gmail: this user has no connected mailbox to send from")

// Send delivers one message as ownerID's own mailbox and returns Gmail's id.
//
// The From address is read from the stored connection rather than taken from
// the caller: Gmail will only send as an address the authenticated account
// owns, so a supplied From would either be ignored or rejected — and
// pretending a message came from somebody else is not a feature this should
// make easy.
func (s *Sender) Send(
	ctx context.Context, scope domain.Scope, ownerID string, req SendRequest,
) (string, error) {
	connection, err := s.store.GetGoogleConnection(ctx, scope, ownerID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", ErrNoMailbox
		}
		return "", err
	}
	if connection.Status != domain.GoogleStatusConnected {
		return "", fmt.Errorf("%w: the connected mailbox needs reauthorization", ErrNoMailbox)
	}
	if strings.TrimSpace(connection.GoogleAccountEmail) == "" {
		return "", fmt.Errorf("%w: the connection records no address", ErrNoMailbox)
	}

	token, err := s.tokens.AccessToken(ctx, scope, ownerID)
	if err != nil {
		return "", err
	}

	req.From = connection.GoogleAccountEmail
	return s.api.Send(ctx, token, req)
}
