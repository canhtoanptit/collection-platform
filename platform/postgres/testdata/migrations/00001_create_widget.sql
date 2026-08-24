-- Fixture migration set for platform/postgres integration tests. It is not a
-- service schema and nothing depends on its shape: it exists so Migrate can be
-- exercised against a real Postgres — up, version, down, up again.

-- +goose Up
CREATE TABLE widget (
    id           TEXT PRIMARY KEY,
    name         TEXT        NOT NULL,
    -- Money is int64 minor units plus an ISO-4217 code, everywhere, including
    -- fixtures (CLAUDE.md §3). A fixture that used NUMERIC would be a bad
    -- example for anyone who copied it.
    amount_minor BIGINT      NOT NULL,
    currency     CHAR(3)     NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE widget;
