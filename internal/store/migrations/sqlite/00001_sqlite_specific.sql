-- +goose Up
-- SQLite-specific objects: the session store and the FTS5 search index.
--
-- This is the one place dialect-specific SQL is expected (HANDOVER §4.4).
-- Everything else lives in migrations/shared and must run unmodified on both
-- engines.

-- scs sqlite3store expects exactly this shape.
CREATE TABLE sessions (
  token  TEXT PRIMARY KEY,
  data   BLOB NOT NULL,
  expiry REAL NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions(expiry);

-- A single denormalised search index across every searchable entity, kept in
-- sync by the store on write. External-content FTS5 tables were considered and
-- rejected: they need triggers per source table, and the source rows here are
-- spread across eight tables with different shapes.
--
-- `unindexed` columns are stored but not tokenised -- they are payload for
-- rendering a result, not something to match on.
--
-- The tokenchars set is deliberate. `.` and `:` are included so that an IPv4
-- address, an IPv6 address and a MAC each stay a single token rather than
-- fragmenting into meaningless numbers. `-` is deliberately NOT included:
-- with it, "hv-01-renamed" would be one token and the prefix query used for
-- type-ahead would never match "renamed". Splitting on the hyphen means
-- "orders-api" is findable as "orders", as "api", and as the whole string.
CREATE VIRTUAL TABLE search_index USING fts5(
  entity_type UNINDEXED,
  entity_id   UNINDEXED,
  title,
  subtitle,
  body,
  tokenize = "unicode61 tokenchars '.:_/'"
);

-- +goose Down
DROP TABLE search_index;
DROP TABLE sessions;
