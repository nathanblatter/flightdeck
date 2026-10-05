-- +goose Up
-- +goose StatementBegin

-- OAuth 2.1 authorization server for the public MCP connector surface
-- (claude.ai / Claude mobile "Connectors"). Those clients cannot send an
-- X-API-Key header and only speak OAuth, so the instance issues opaque bearer
-- tokens that resolve to an existing api_keys row. Identity, scopes, and
-- revocation therefore stay exactly where they already live: revoke the key
-- and every token minted from it dies with it.

-- Clients self-register (RFC 7591 dynamic client registration). secret_hash is
-- NULL for public clients (token_endpoint_auth_method=none).
CREATE TABLE oauth_clients (
    id            text PRIMARY KEY,
    secret_hash   text,
    name          text NOT NULL DEFAULT '',
    redirect_uris text[] NOT NULL,
    auth_method   text NOT NULL DEFAULT 'none',
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Authorization codes: single use, short lived, bound to the PKCE challenge
-- and redirect URI they were issued for.
CREATE TABLE oauth_codes (
    code_hash      text PRIMARY KEY,
    client_id      text NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    api_key_id     uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    redirect_uri   text NOT NULL,
    code_challenge text NOT NULL,
    scope          text NOT NULL DEFAULT '',
    expires_at     timestamptz NOT NULL,
    used           boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- One row per access/refresh pair. Refresh rotates: the old row is revoked and
-- a new one inserted, so a replayed refresh token is detectable.
CREATE TABLE oauth_tokens (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id          text NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    api_key_id         uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    access_hash        text NOT NULL UNIQUE,
    refresh_hash       text NOT NULL UNIQUE,
    scope              text NOT NULL DEFAULT '',
    access_expires_at  timestamptz NOT NULL,
    refresh_expires_at timestamptz NOT NULL,
    revoked            boolean NOT NULL DEFAULT false,
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_used_at       timestamptz
);

CREATE INDEX oauth_tokens_api_key_idx ON oauth_tokens (api_key_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS oauth_codes;
DROP TABLE IF EXISTS oauth_clients;
-- +goose StatementEnd
