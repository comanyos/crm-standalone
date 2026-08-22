-- +goose Up
-- GIN index for fast JSONB trait search (email, name, etc.)
CREATE INDEX IF NOT EXISTS idx_identities_traits_gin ON identities USING gin (traits);

-- Trigram index for free-text substring search on traits text representation.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS idx_identities_traits_trgm ON identities USING gin ((traits::text) gin_trgm_ops);

-- +goose Down
DROP INDEX IF EXISTS idx_identities_traits_trgm;
DROP INDEX IF EXISTS idx_identities_traits_gin;
