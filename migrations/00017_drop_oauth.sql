-- +goose Up
-- +goose StatementBegin

-- The public connector now authenticates through Squawk
-- (https://squawk.nathanblatter.com), which is the authorization server;
-- flightdeck is a plain RFC 9728 protected resource verifying its JWTs.
-- The in-binary OAuth 2.1 server and its tables are gone. Nothing reads them
-- any more, so they are dropped outright (dependents first). api_keys stays.
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS oauth_codes;
DROP TABLE IF EXISTS oauth_clients;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Not reversible: the old authorization server is deleted from the code, so
-- recreating empty tables would serve nothing.
SELECT 1;
-- +goose StatementEnd
