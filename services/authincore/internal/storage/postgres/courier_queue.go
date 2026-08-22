package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/courier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CourierQueue implements courier.Queue using the courier_messages table.
type CourierQueue struct {
	db *pgxpool.Pool
}

func NewCourierQueue(db *pgxpool.Pool) *CourierQueue {
	return &CourierQueue{db: db}
}

func (q *CourierQueue) Enqueue(ctx context.Context, msg *courier.Message) error {
	data, _ := json.Marshal(msg.Data)
	_, err := q.db.Exec(ctx,
		`INSERT INTO courier_messages
		   (id, org_id, type, recipient, subject, body, data, status, send_count, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 1, 0, $8)`,
		msg.ID, msg.OrgID, string(msg.Type), msg.Recipient,
		msg.Subject, msg.Body, data, msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("courier enqueue: %w", err)
	}
	return nil
}

func (q *CourierQueue) Dequeue(ctx context.Context, limit int) ([]*courier.Message, error) {
	rows, err := q.db.Query(ctx,
		`SELECT id, org_id, type, recipient, subject, body, data, status, send_count, created_at
		 FROM courier_messages
		 WHERE status = 1
		 ORDER BY created_at ASC
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("courier dequeue: %w", err)
	}
	defer rows.Close()

	var msgs []*courier.Message
	for rows.Next() {
		m := &courier.Message{}
		var msgType string
		var dataJSON []byte
		if err := rows.Scan(
			&m.ID, &m.OrgID, &msgType, &m.Recipient,
			&m.Subject, &m.Body, &dataJSON,
			&m.Status, &m.SendCount, &m.CreatedAt,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			return nil, fmt.Errorf("courier dequeue scan: %w", err)
		}
		m.Type = courier.MessageType(msgType)
		m.Data = json.RawMessage(dataJSON)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (q *CourierQueue) MarkSent(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	_, err := q.db.Exec(ctx,
		`UPDATE courier_messages
		 SET status = 2, sent_at = $1, send_count = send_count + 1
		 WHERE id = $2`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("courier mark sent: %w", err)
	}
	return nil
}

func (q *CourierQueue) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := q.db.Exec(ctx,
		`UPDATE courier_messages
		 SET status = 3, send_count = send_count + 1, body = $1
		 WHERE id = $2`,
		reason, id,
	)
	if err != nil {
		return fmt.Errorf("courier mark failed: %w", err)
	}
	return nil
}
