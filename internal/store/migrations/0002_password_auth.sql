-- Self-serve signup: users created via liveurld seed have no password and
-- can only be reached via a CLI-minted token, so this stays nullable.
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT;
