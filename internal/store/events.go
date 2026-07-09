package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type EventState string

const (
	EventQueued    EventState = "queued"
	EventReplaying EventState = "replaying"
	EventDelivered EventState = "delivered"
	EventDead      EventState = "dead"
)

type WebhookEvent struct {
	ID          int64
	TunnelID    int64
	Method      string
	Path        string
	Query       string
	Headers     map[string][]string
	Body        []byte
	ReceivedAt  time.Time
	State       EventState
	Attempts    int
	LastStatus  int
	DeliveredAt *time.Time
}

const maxReplayAttempts = 5

func (s *Store) EnqueueEvent(ctx context.Context, tunnelID int64, method, path, query string, headers map[string][]string, body []byte) (int64, error) {
	h, err := json.Marshal(headers)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO webhook_events(tunnel_id, method, path, query, headers, body, state)
		 VALUES($1, $2, $3, $4, $5, $6, 'queued') RETURNING id`,
		tunnelID, method, path, query, h, body).Scan(&id)
	return id, err
}

// QueuedEvents returns queued events for a tunnel, oldest first, so replay
// preserves original receipt order.
func (s *Store) QueuedEvents(ctx context.Context, tunnelID int64) ([]WebhookEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tunnel_id, method, path, query, headers, body, received_at, state, attempts, last_status, delivered_at
		 FROM webhook_events WHERE tunnel_id=$1 AND state='queued' ORDER BY received_at, id`, tunnelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WebhookEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListEvents(ctx context.Context, tunnelID int64, state string) ([]WebhookEvent, error) {
	var rows pgx.Rows
	var err error
	if state == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT id, tunnel_id, method, path, query, headers, body, received_at, state, attempts, last_status, delivered_at
			 FROM webhook_events WHERE tunnel_id=$1 ORDER BY received_at, id`, tunnelID)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id, tunnel_id, method, path, query, headers, body, received_at, state, attempts, last_status, delivered_at
			 FROM webhook_events WHERE tunnel_id=$1 AND state=$2 ORDER BY received_at, id`, tunnelID, state)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WebhookEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EventByID(ctx context.Context, id int64) (WebhookEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tunnel_id, method, path, query, headers, body, received_at, state, attempts, last_status, delivered_at
		 FROM webhook_events WHERE id=$1`, id)
	if err != nil {
		return WebhookEvent{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return WebhookEvent{}, ErrNotFound
	}
	return scanEvent(rows)
}

func (s *Store) MarkReplaying(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE webhook_events SET state='replaying' WHERE id=$1`, id)
	return err
}

func (s *Store) MarkDelivered(ctx context.Context, id int64, status int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE webhook_events SET state='delivered', attempts=attempts+1, last_status=$2, delivered_at=now() WHERE id=$1`,
		id, status)
	return err
}

// MarkFailed records a failed replay attempt. Once maxReplayAttempts is
// reached the event moves to 'dead'; otherwise it goes back to 'queued' so
// it is retried on the next reconnect drain.
func (s *Store) MarkFailed(ctx context.Context, id int64, status int) error {
	var attempts int
	err := s.pool.QueryRow(ctx,
		`UPDATE webhook_events SET attempts=attempts+1, last_status=$2 WHERE id=$1 RETURNING attempts`,
		id, status).Scan(&attempts)
	if err != nil {
		return err
	}
	next := "queued"
	if attempts >= maxReplayAttempts {
		next = "dead"
	}
	_, err = s.pool.Exec(ctx, `UPDATE webhook_events SET state=$2 WHERE id=$1`, id, next)
	return err
}

func (s *Store) RequeueEvent(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE webhook_events SET state='queued', attempts=0, last_status=0, delivered_at=NULL WHERE id=$1`, id)
	return err
}

func (s *Store) ClearEvents(ctx context.Context, tunnelID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM webhook_events WHERE tunnel_id=$1`, tunnelID)
	return err
}

func scanEvent(rows pgx.Rows) (WebhookEvent, error) {
	var e WebhookEvent
	var headersRaw []byte
	var state string
	var deliveredAt *time.Time
	if err := rows.Scan(&e.ID, &e.TunnelID, &e.Method, &e.Path, &e.Query, &headersRaw, &e.Body,
		&e.ReceivedAt, &state, &e.Attempts, &e.LastStatus, &deliveredAt); err != nil {
		return WebhookEvent{}, err
	}
	e.State = EventState(state)
	e.DeliveredAt = deliveredAt
	if len(headersRaw) > 0 {
		if err := json.Unmarshal(headersRaw, &e.Headers); err != nil {
			return WebhookEvent{}, err
		}
	}
	return e, nil
}

var ErrEventNotFound = errors.New("event not found")
