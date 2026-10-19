CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE TABLE clients (
 id bigserial PRIMARY KEY, name text NOT NULL, phone text NOT NULL DEFAULT '',
 email text NOT NULL UNIQUE, note text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE accounts (
 id bigserial PRIMARY KEY, client_id bigint NOT NULL UNIQUE REFERENCES clients(id),
 password_hash text NOT NULL, role text NOT NULL CHECK (role IN ('admin','client')) DEFAULT 'client'
);
CREATE TABLE sessions (
 token_hash text PRIMARY KEY, account_id bigint NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 expires_at timestamptz NOT NULL
);
CREATE INDEX ON sessions(expires_at);
CREATE TABLE invitations (
 token_hash text PRIMARY KEY, client_id bigint NOT NULL REFERENCES clients(id),
 expires_at timestamptz NOT NULL, used_at timestamptz
);
CREATE TABLE password_resets (
 token_hash text PRIMARY KEY, account_id bigint NOT NULL REFERENCES accounts(id),
 expires_at timestamptz NOT NULL, used_at timestamptz
);
CREATE TABLE workplaces (
 id bigserial PRIMARY KEY, name text NOT NULL, description text NOT NULL DEFAULT '',
 hourly_price integer NOT NULL CHECK (hourly_price > 0), active boolean NOT NULL DEFAULT true
);
CREATE TABLE bookings (
 id bigserial PRIMARY KEY, workplace_id bigint NOT NULL REFERENCES workplaces(id),
 client_id bigint NOT NULL REFERENCES clients(id), starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL,
 hourly_price integer NOT NULL CHECK (hourly_price > 0),
 created_by bigint NOT NULL REFERENCES accounts(id), created_at timestamptz NOT NULL DEFAULT now(),
 cancelled_at timestamptz, cancelled_by bigint REFERENCES accounts(id),
 CHECK (ends_at >= starts_at + interval '30 minutes'),
 CHECK (EXTRACT(SECOND FROM starts_at) = 0 AND EXTRACT(SECOND FROM ends_at) = 0),
 CHECK (EXTRACT(MINUTE FROM starts_at)::int % 15 = 0 AND EXTRACT(MINUTE FROM ends_at)::int % 15 = 0),
 CHECK ((starts_at AT TIME ZONE 'Europe/Moscow')::date = (ends_at AT TIME ZONE 'Europe/Moscow')::date),
 CHECK ((starts_at AT TIME ZONE 'Europe/Moscow')::time >= time '09:00' AND (ends_at AT TIME ZONE 'Europe/Moscow')::time <= time '21:00'),
 EXCLUDE USING gist (workplace_id WITH =, tstzrange(starts_at, ends_at, '[)') WITH &&) WHERE (cancelled_at IS NULL)
);
CREATE INDEX ON bookings(client_id, starts_at);
