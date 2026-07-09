package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxSnapshotBytes is the total stored-snapshot size cap per tunnel. When a
// new snapshot would exceed it, the oldest snapshots are evicted first.
const MaxSnapshotBytes = 100 << 20

type Snapshot struct {
	Path        string
	ContentType string
	Headers     map[string]string
	Body        []byte
	CapturedAt  time.Time
}

func QueryHash(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawQuery))
	return hex.EncodeToString(sum[:8])
}

// PutSnapshot upserts a cached response and then evicts the oldest entries
// for that tunnel until the total stored size is back under the cap.
func (s *Store) PutSnapshot(ctx context.Context, tunnelID int64, path, queryHash, contentType string, headers map[string]string, body []byte) error {
	h, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`INSERT INTO snapshots(tunnel_id, path, query_hash, content_type, headers, body, captured_at)
		 VALUES($1, $2, $3, $4, $5, $6, now())
		 ON CONFLICT(tunnel_id, path, query_hash) DO UPDATE
		   SET content_type=EXCLUDED.content_type, headers=EXCLUDED.headers,
		       body=EXCLUDED.body, captured_at=now()`,
		tunnelID, path, queryHash, contentType, h, body); err != nil {
		return err
	}

	var total int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(octet_length(body)),0) FROM snapshots WHERE tunnel_id=$1`, tunnelID).Scan(&total); err != nil {
		return err
	}
	for total > MaxSnapshotBytes {
		var evicted int64
		err := tx.QueryRow(ctx,
			`DELETE FROM snapshots WHERE ctid IN (
				SELECT ctid FROM snapshots WHERE tunnel_id=$1 ORDER BY captured_at LIMIT 1
			 ) RETURNING octet_length(body)`, tunnelID).Scan(&evicted)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		total -= evicted
	}
	return tx.Commit(ctx)
}

func (s *Store) GetSnapshot(ctx context.Context, tunnelID int64, path, queryHash string) (Snapshot, error) {
	var snap Snapshot
	var headersRaw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT path, content_type, headers, body, captured_at
		 FROM snapshots WHERE tunnel_id=$1 AND path=$2 AND query_hash=$3`,
		tunnelID, path, queryHash).Scan(&snap.Path, &snap.ContentType, &headersRaw, &snap.Body, &snap.CapturedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	if len(headersRaw) > 0 {
		if err := json.Unmarshal(headersRaw, &snap.Headers); err != nil {
			return Snapshot{}, err
		}
	}
	return snap, nil
}

type SnapshotStats struct {
	Pages int64
	Bytes int64
}

func (s *Store) SnapshotStats(ctx context.Context, tunnelID int64) (SnapshotStats, error) {
	var st SnapshotStats
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(octet_length(body)),0) FROM snapshots WHERE tunnel_id=$1`,
		tunnelID).Scan(&st.Pages, &st.Bytes)
	return st, err
}
