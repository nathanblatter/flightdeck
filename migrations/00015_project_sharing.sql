-- +goose Up

-- Cross-instance project sharing.
--
-- A share is a bidirectional link between one local project and one peer,
-- carried by an encrypted mailbox neither instance can reach directly. Both
-- sides may write: this is a shared project, not a read-only mirror.
--
-- Sync identity is items.id / activity.id. Those are already UUIDs, so the same
-- row carries the same identity on every instance and matching needs no new
-- columns. Crucially it must NOT be seq/ref: both sides create items, and their
-- per-project sequences would collide immediately. Each instance therefore
-- assigns its own local seq and ref, and the same item may be flightdeck-12
-- here and flightdeck-31 there.
CREATE TABLE project_shares (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- Display name for the instance on the other end, from the invite.
    peer_name    text NOT NULL DEFAULT '',

    -- Where the mailbox lives and how to open it. send_token writes to the
    -- peer's mailbox; recv_token drains our own.
    mailbox_url  text NOT NULL,
    send_mailbox text NOT NULL,
    send_token   text NOT NULL,
    recv_mailbox text NOT NULL,
    recv_token   text NOT NULL,

    -- Symmetric key for the payloads. Never leaves this instance and is never
    -- sent to the mailbox host, which is why that host can carry project data
    -- it cannot read.
    secret       bytea NOT NULL,

    -- Client certificate proving this instance may talk to the mailbox host at
    -- all. Without it the TLS handshake fails before any request is made.
    client_cert  text NOT NULL,
    client_key   text NOT NULL,
    ca_pem       text NOT NULL,

    enabled      boolean NOT NULL DEFAULT true,
    last_send_at timestamptz,
    last_recv_at timestamptz,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    -- One share per project per mailbox pair.
    UNIQUE (project_id, send_mailbox)
);

CREATE INDEX project_shares_project_idx ON project_shares (project_id) WHERE enabled;

-- Per-row sync bookkeeping. Two hashes, because one cannot answer both
-- questions this protocol has to ask.
--
-- sent_hash is the echo guard: the content we last put on the wire. Without it,
-- applying a peer's change would look like a local change on the next outbound
-- scan and bounce straight back, and the instances would volley forever.
--
-- base_hash is the conflict detector: the content we and the peer last provably
-- agreed on, updated only when we APPLY something, never when we send. Sending
-- is optimistic — the peer may have been editing the same row at the same
-- moment — so a hash advanced on send would make a genuine conflict look like
-- agreement and silently overwrite the local edit.
CREATE TABLE project_share_state (
    share_id    uuid NOT NULL REFERENCES project_shares(id) ON DELETE CASCADE,
    entity_kind text NOT NULL CHECK (entity_kind IN ('project', 'item', 'activity')),
    entity_id   uuid NOT NULL,
    sent_hash   text NOT NULL DEFAULT '',
    base_hash   text NOT NULL DEFAULT '',
    synced_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (share_id, entity_kind, entity_id)
);

-- +goose Down
DROP TABLE project_share_state;
DROP TABLE project_shares;
