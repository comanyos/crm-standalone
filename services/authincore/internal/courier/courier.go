// Package courier handles async email/SMS delivery for recovery, verification, and magic-links.
package courier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"text/template"
	"time"

	"github.com/google/uuid"
)

type MessageType string

const (
	TypeRecoveryCode     MessageType = "recovery_code"
	TypeVerificationCode MessageType = "verification_code"
	TypeLoginCode        MessageType = "login_code"
	TypeWelcome          MessageType = "welcome"
	TypeInvitation       MessageType = "invitation"
)

// Message is an outbound notification to deliver.
type Message struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Type      MessageType
	Recipient string // email or phone
	Subject   string
	Body      string
	Data      json.RawMessage
	Status    int // 1=queued 2=sent 3=failed
	SendCount int
	CreatedAt time.Time
	SentAt    *time.Time
}

// Sender delivers a message (email or SMS backend).
type Sender interface {
	SendEmail(ctx context.Context, to, subject, body string) error
	SendSMS(ctx context.Context, to, body string) error
}

// Queue persists and dequeues messages.
type Queue interface {
	Enqueue(ctx context.Context, msg *Message) error
	Dequeue(ctx context.Context, limit int) ([]*Message, error)
	MarkSent(ctx context.Context, id uuid.UUID) error
	MarkFailed(ctx context.Context, id uuid.UUID, reason string) error
}

// TemplateLoader loads message templates for an org.
type TemplateLoader interface {
	Load(ctx context.Context, orgID uuid.UUID, msgType MessageType) (subject, body string, err error)
}

// Courier orchestrates message dispatch.
type Courier struct {
	queue  Queue
	sender Sender
	tpl    TemplateLoader
	log    *slog.Logger
}

func New(queue Queue, sender Sender, tpl TemplateLoader, log *slog.Logger) *Courier {
	return &Courier{queue: queue, sender: sender, tpl: tpl, log: log}
}

// Send enqueues a message for async delivery.
func (c *Courier) Send(ctx context.Context, orgID uuid.UUID, msgType MessageType, recipient string, data any) error {
	dataJSON, _ := json.Marshal(data)
	msg := &Message{
		ID:        uuid.New(),
		OrgID:     orgID,
		Type:      msgType,
		Recipient: recipient,
		Data:      dataJSON,
		Status:    1,
		CreatedAt: time.Now().UTC(),
	}
	return c.queue.Enqueue(ctx, msg)
}

// Dispatch processes the queue batch. Call on a ticker or NATS consumer.
func (c *Courier) Dispatch(ctx context.Context) {
	messages, err := c.queue.Dequeue(ctx, 50)
	if err != nil {
		c.log.Error("courier dequeue", "err", err)
		return
	}
	for _, msg := range messages {
		if err := c.deliver(ctx, msg); err != nil {
			c.log.Warn("courier deliver failed", "msg_id", msg.ID, "err", err)
			_ = c.queue.MarkFailed(ctx, msg.ID, err.Error())
		} else {
			_ = c.queue.MarkSent(ctx, msg.ID)
		}
	}
}

func (c *Courier) deliver(ctx context.Context, msg *Message) error {
	subject, bodyTpl, err := c.tpl.Load(ctx, msg.OrgID, msg.Type)
	if err != nil {
		subject, bodyTpl = defaultTemplates(msg.Type)
	}

	var data map[string]any
	_ = json.Unmarshal(msg.Data, &data)

	body, err := renderTemplate(bodyTpl, data)
	if err != nil {
		return fmt.Errorf("courier render template: %w", err)
	}

	return c.sender.SendEmail(ctx, msg.Recipient, subject, body)
}

func renderTemplate(tplStr string, data map[string]any) (string, error) {
	t, err := template.New("msg").Parse(tplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func defaultTemplates(msgType MessageType) (subject, body string) {
	switch msgType {
	case TypeRecoveryCode:
		return "Reset your password", "Your recovery code is: {{.code}}\n\nExpires in 30 minutes."
	case TypeVerificationCode:
		return "Verify your email", "Your verification code is: {{.code}}"
	case TypeLoginCode:
		return "Your login code", "Your one-time code is: {{.code}}\n\nExpires in 5 minutes."
	default:
		return "Notification", "{{.message}}"
	}
}
