-- Second fixture migration: proves ordering, the recorded version and that a
-- rollback unwinds in reverse.

-- +goose Up
ALTER TABLE widget ADD COLUMN note TEXT;

-- +goose Down
ALTER TABLE widget DROP COLUMN note;
