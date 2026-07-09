CREATE TABLE IF NOT EXISTS users (
    id         BIGSERIAL PRIMARY KEY,
    email      TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tunnels (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subdomain    TEXT NOT NULL UNIQUE,
    buffer_rules JSONB NOT NULL DEFAULT '[]',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- state: queued | replaying | delivered | dead
CREATE TABLE IF NOT EXISTS webhook_events (
    id           BIGSERIAL PRIMARY KEY,
    tunnel_id    BIGINT NOT NULL REFERENCES tunnels(id) ON DELETE CASCADE,
    method       TEXT NOT NULL,
    path         TEXT NOT NULL,
    query        TEXT NOT NULL DEFAULT '',
    headers      JSONB NOT NULL DEFAULT '{}',
    body         BYTEA NOT NULL DEFAULT '',
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    state        TEXT NOT NULL DEFAULT 'queued',
    attempts     INT NOT NULL DEFAULT 0,
    last_status  INT NOT NULL DEFAULT 0,
    delivered_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS webhook_events_queue_idx
    ON webhook_events (tunnel_id, state, received_at, id);

CREATE TABLE IF NOT EXISTS snapshots (
    tunnel_id    BIGINT NOT NULL REFERENCES tunnels(id) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    query_hash   TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    headers      JSONB NOT NULL DEFAULT '{}',
    body         BYTEA NOT NULL,
    captured_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tunnel_id, path, query_hash)
);
