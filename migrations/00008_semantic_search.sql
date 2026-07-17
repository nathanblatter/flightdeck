-- +goose Up
-- +goose StatementBegin

-- (28) Semantic search: pgvector enables conceptual recall — finding items by
-- meaning, not just lexical overlap — complementing FTS (exact-ish) and trigram
-- (typos/fragments). Embeddings are produced out-of-band by the background
-- embedder (OpenAI text-embedding-3-small, 1536 dims) and stored here; the
-- column is NULL until embedded and is reset to NULL when an item's title/body
-- changes, so the embedder re-embeds it.
CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE items ADD COLUMN embedding       vector(1536);
ALTER TABLE items ADD COLUMN embedding_model text NOT NULL DEFAULT '';

-- HNSW cosine index for approximate nearest-neighbour search. HNSW needs no
-- training (unlike IVFFlat) and gives strong recall at flightdeck's scale.
CREATE INDEX items_embedding_hnsw_idx ON items
    USING hnsw (embedding vector_cosine_ops);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS items_embedding_hnsw_idx;
ALTER TABLE items DROP COLUMN IF EXISTS embedding_model;
ALTER TABLE items DROP COLUMN IF EXISTS embedding;
-- +goose StatementEnd
