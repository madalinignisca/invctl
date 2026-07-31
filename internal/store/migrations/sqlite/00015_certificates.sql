-- TLS certificates, and where they are deployed.
--
-- THIS IS THE MISSING HALF OF THE EXPIRY REPORT. Hardware end-of-support is an
-- annual event; a certificate expires every ninety days and its expiry is an
-- immediate outage rather than a procurement conversation. `not_after` is
-- exactly an EOL date, so certificates join the report that already answers
-- "what runs out, who owns it, and what rides on it" -- and a report that
-- omitted them would be missing the thing that actually pages people.
--
-- AN ENTITY, NOT A FIELD. A wildcard is genuinely deployed in several places at
-- once, so a certificate has its own identity -- serial, fingerprint, validity --
-- and links to each place it is used. Two parallel link tables with real foreign
-- keys, mirroring project_asset/project_service, so a typo in an import cannot
-- invent a deployment that belongs to nothing.
--
-- NEVER THE PRIVATE KEY. `key_ref` holds a PATH or a vault reference and nothing
-- else, exactly as `identity.secret_ref` does. The public certificate body is
-- not secret -- it is served to every client that connects -- but it is not
-- stored either: it is bulk with no query value, and a column that accepts
-- certificate-shaped text is an invitation to paste a key into the wrong one.
-- If a code path would put key material in this database, stop.
--
-- DECLARED, NOT OBSERVED. Somebody asserts that this certificate, with this
-- expiry, is deployed here. What a scanner finds actually being served on :443
-- is a different fact and belongs on the observed side -- where a mismatch
-- between the two is itself the finding. Nothing here is written by a machine.
--
-- SANS ARE A CHILD TABLE, not a delimited string. "Which certificate covers
-- orders.example.com" is a question people arrive with, and the house rule is
-- that anything you filter on is a column rather than an attribute. It also lets
-- the global search resolve a hostname straight to the certificate, the same
-- treatment an IP address or a MAC already gets.

-- +goose Up
CREATE TABLE certificate (
  id           TEXT NOT NULL PRIMARY KEY,
  -- The common name, kept separate from the SAN list because it is what a
  -- person calls the certificate even when it covers a dozen names.
  subject_cn   TEXT NOT NULL,
  issuer       TEXT,
  -- Lowercase hex, no colons, normalised in Go. Unique because it IS the
  -- certificate's identity: two rows with one fingerprint are one certificate
  -- entered twice, and the totals and expiry counts would double.
  fingerprint  TEXT UNIQUE,
  serial       TEXT,
  not_before   TEXT,
  not_after    TEXT,
  -- A path or a vault reference. NEVER key material; see the header.
  key_ref      TEXT,
  -- Who renews it. Often a different team from whoever runs the box it sits on,
  -- which is why it is recorded here rather than inferred from the deployments.
  team_id      TEXT REFERENCES team(id),
  manager_role TEXT REFERENCES responsibility_role(code),
  lifecycle    TEXT NOT NULL DEFAULT 'active',
  attrs        TEXT NOT NULL DEFAULT '{}',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  CONSTRAINT certificate_subject_check   CHECK (subject_cn <> ''),
  CONSTRAINT certificate_lifecycle_check CHECK (lifecycle IN ('planned','active','deprecated','retired')),
  -- Date shape only, as with eol_date: length and separators are the most both
  -- engines agree on, and Go rejects 2027-02-31 where this cannot.
  CONSTRAINT certificate_not_before_check CHECK (not_before IS NULL OR (length(not_before) = 10
                 AND substr(not_before, 5, 1) = '-' AND substr(not_before, 8, 1) = '-')),
  CONSTRAINT certificate_not_after_check  CHECK (not_after IS NULL OR (length(not_after) = 10
                 AND substr(not_after, 5, 1) = '-' AND substr(not_after, 8, 1) = '-')),
  -- A validity window that closes before it opens never matches anything, so
  -- the certificate would silently never appear as current.
  CONSTRAINT certificate_window_check     CHECK (not_before IS NULL OR not_after IS NULL
                 OR not_after >= not_before)
);
CREATE INDEX idx_certificate_expiry ON certificate(not_after) WHERE not_after IS NOT NULL;
CREATE INDEX idx_certificate_team   ON certificate(team_id)   WHERE team_id IS NOT NULL;

-- The names it covers. One row per name so a hostname is searchable, and the
-- common name is repeated here at insert time: a reader asking "what covers
-- this" should not have to know that one of the names lives in another column.
CREATE TABLE certificate_san (
  certificate_id TEXT NOT NULL REFERENCES certificate(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  PRIMARY KEY (certificate_id, name),
  CONSTRAINT certificate_san_name_check CHECK (name <> '')
);
CREATE INDEX idx_certificate_san_name ON certificate_san(name);

-- Where it is deployed. Two tables rather than one polymorphic pair, for the
-- referential integrity -- the same argument as project_asset/project_service.
CREATE TABLE certificate_asset (
  certificate_id TEXT NOT NULL REFERENCES certificate(id),
  asset_id       TEXT NOT NULL REFERENCES asset(id),
  note           TEXT,
  lifecycle      TEXT NOT NULL DEFAULT 'active',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  PRIMARY KEY (certificate_id, asset_id),
  CONSTRAINT certificate_asset_lifecycle_check CHECK (lifecycle IN ('active','retired'))
);
CREATE INDEX idx_certificate_asset_asset ON certificate_asset(asset_id);

CREATE TABLE certificate_service (
  certificate_id TEXT NOT NULL REFERENCES certificate(id),
  service_id     TEXT NOT NULL REFERENCES service(id),
  note           TEXT,
  lifecycle      TEXT NOT NULL DEFAULT 'active',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  PRIMARY KEY (certificate_id, service_id),
  CONSTRAINT certificate_service_lifecycle_check CHECK (lifecycle IN ('active','retired'))
);
CREATE INDEX idx_certificate_service_service ON certificate_service(service_id);

-- +goose Down
DROP TABLE certificate_service;
DROP TABLE certificate_asset;
DROP TABLE certificate_san;
DROP TABLE certificate;
