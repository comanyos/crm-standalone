-- Migration 003: Add updated_at to sessions table.

-- +goose Up
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- +goose Down
ALTER TABLE sessions DROP COLUMN IF EXISTS updated_at;
