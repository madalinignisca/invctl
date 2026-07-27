-- +goose Up
-- PostgreSQL-specific objects: the session store and the trigram search index.

-- scs postgresstore expects exactly this shape.
CREATE TABLE sessions (
  token  TEXT PRIMARY KEY,
  data   BYTEA NOT NULL,
  expiry TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions(expiry);

-- Mirror of the SQLite FTS5 table, maintained by the same store code. Trigram
-- rather than tsvector because the queries that matter here are identifier
-- fragments -- "10.20.30", "vault-", "aa:bb:cc" -- which a word-stemming
-- full-text index handles badly and a trigram index handles well.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE search_index (
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  subtitle    TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (entity_type, entity_id)
);

CREATE INDEX idx_search_title_trgm ON search_index USING gin (title gin_trgm_ops);
CREATE INDEX idx_search_body_trgm  ON search_index USING gin (body gin_trgm_ops);

-- +goose Down
DROP TABLE search_index;
DROP TABLE sessions;
