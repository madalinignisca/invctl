-- Dialect-split despite being byte-identical in both directories, and that is
-- not an oversight. The seven lookup tables are created by the DIALECT
-- migration 00004, and Migrate applies every shared migration before any
-- dialect one -- so a shared migration touching these tables runs before they
-- exist. Measured, not assumed: as shared/00010 this failed with "no such
-- table: asset_kind". Placement follows the dependency, not the SQL.
--
-- +goose Up
-- Observed state: what the estate reports about itself (docs/AUDIT.md rules 1-14).
--
-- Why a separate set of tables rather than more columns on the rows they
-- describe: "every mutation writes a change_log row, no exceptions" defeats
-- itself once telemetry arrives. A 30-second health poll is ~2,880 rows per
-- asset per day -- ~60k/day for the demo estate -- and the configuration change
-- that caused an incident becomes unfindable underneath it. Naming a column
-- `observed_*` is not separation either: a mixed row produces a mixed audit
-- entry that no portable query can classify, because the only distinguishing
-- information would sit inside change_log.diff and querying inside JSON is
-- banned here. So the split is physical, by table (rule 1), and the retention
-- rule can then be "observed_transition is the only prunable table" rather than
-- a predicate somebody has to get right (rule 10).
--
-- docs/AUDIT.md calls this migration `00006` throughout. 00006 (link.lifecycle)
-- and 00007 (reachability) were taken before M6 started, so it is 00008; every
-- "00006" in that document means this file.
--
-- Portability, verified on both engines before this file was written
-- (modernc.org/sqlite v1.54.0 = SQLite 3.53.3, and PostgreSQL via pgx):
--   * ALTER TABLE ... DROP COLUMN -- SQLite has had it since 3.35, and neither
--     dropped column is indexed or named in a CHECK, which is what would force
--     a table rebuild. Confirmed by running the sequence on both engines.
--   * ALTER TABLE ... ADD COLUMN ... CHECK (...) -- already proven by 00006.
--   * ALTER TABLE ... RENAME COLUMN -- SQLite 3.25+; both engines carry the
--     CHECK expression across the rename (confirmed: a bad INSERT afterwards is
--     rejected on both).
--   * Partial UNIQUE indexes -- already proven by 00007.
-- No arrays, no ENUM types, no jsonb, no SERIAL, no NOW() default: every id and
-- every timestamp on these tables is generated in Go, exactly as everywhere else.

-- ---------------------------------------------------------------------------
-- Current observed state
-- ---------------------------------------------------------------------------

-- One row per (entity, reporter). `reporter` is IN THE PRIMARY KEY, not an
-- attribute: two monitors watching one asset and disagreeing must show as two
-- readings that disagree. Keyed on the entity alone they would overwrite each
-- other on every poll and the entity would appear to flap forever, which is the
-- one failure mode that makes an operator stop believing the panel.
--
-- Three timestamps, and they may not be collapsed (rule 3):
--   state_since    -- server clock, written ONLY when `state` changes. "Down
--                     since when" is the first question anyone asks at 03:00,
--                     and a repeat report must never move it.
--   last_report_at -- server clock, every report. Drives staleness, ordering,
--                     monotonicity and retention.
--   reported_at    -- the reporter's OWN clock, display only. A caller must
--                     never write the field that decides how fresh its own data
--                     looks; Go rejects one more than 300s ahead of the server.
--
-- entity_id carries no foreign key on purpose, and that is not laxity:
-- entity_type is polymorphic so no single FK could express it, and rule 6
-- requires an observation for an unknown entity to be 404 -- never created.
-- An `INSERT ... ON CONFLICT` keyed on this table is the natural verb and it is
-- precisely what turns a deliberately narrow monitoring token into an
-- inventory-write vector, so existence is checked in Go before the write and an
-- unresolvable report lands in unmatched_observation below.
--
-- interval_seconds is the reporter's declared cadence (rule 8). It lives here
-- rather than in config because staleness is computed at read time -- a value
-- older than 3x the interval renders as `unknown (stale since T)`, never as its
-- last value. Silence is not health: an intruder's first act is killing the
-- collector, and under transition-only logging a dead collector and a healthy
-- estate are otherwise the same picture, forever.
--
-- observation_id is the reporter's idempotency key (rule 4). A repeat of the
-- stored value is a no-op rather than a second write; it is nullable because a
-- reporter that does not supply one still gets ordering from reported_at.
--
-- The four flap_* columns carry rule 9's OPEN EPISODE, and they live here
-- rather than in a table of their own for two reasons. The first is that a flap
-- episode is a property of the current reading -- "flapping" is displayed
-- ALONGSIDE the state, never instead of it, or a box that died twenty minutes
-- ago still shows as flapping -- so putting it anywhere else would make every
-- health read a join. The second is that the writer already upserts this row on
-- every transition, so the episode is maintained in the write that caused it.
--
-- flap_since is NULL exactly when no episode is open, and the trailing CHECK
-- makes the four move together: a half-open episode would suppress transitions
-- with nothing able to close it or say how many it hid.
--
-- flap_seen is the set of states seen since the episode opened, comma-joined.
-- It is opaque to SQL -- never filtered on, never joined through, exactly like
-- `attrs` -- and is parsed in Go. It decides rule 9's escape hatch: a transition
-- to a value NOT in this set is always emitted as its own row, because a novel
-- value is by definition not part of the oscillation being compressed. That is
-- a security property, not a display nicety: it is what stops a stolen token
-- deliberately tripping the floor so the real `down` five minutes later reads
-- as a monitoring artefact.
--
-- The thresholds themselves are NOT here and never will be. domain.FlapThreshold
-- and domain.FlapWindow are Go constants: never env, never a per-row column,
-- never a payload field, because a writer that can raise its own suppression
-- threshold has a mute button.
CREATE TABLE asset_health (
  entity_type      TEXT NOT NULL CHECK (entity_type IN ('asset','service_instance')),
  entity_id        TEXT NOT NULL,
  reporter         TEXT NOT NULL,
  state            TEXT NOT NULL CHECK (state IN ('up','degraded','down','unknown')),
  state_since      TEXT NOT NULL,
  reported_at      TEXT NOT NULL,
  last_report_at   TEXT NOT NULL,
  interval_seconds INTEGER CHECK (interval_seconds IS NULL OR interval_seconds > 0),
  observation_id   TEXT,
  -- Rule 9's open episode. All four are NULL together or set together.
  flap_since       TEXT,
  flap_count       INTEGER CHECK (flap_count IS NULL OR flap_count > 0),
  flap_first_state TEXT CHECK (flap_first_state IS NULL OR
                     flap_first_state IN ('up','degraded','down','unknown')),
  flap_seen        TEXT,
  PRIMARY KEY (entity_type, entity_id, reporter),
  CHECK ((flap_since IS NULL AND flap_count IS NULL AND flap_first_state IS NULL
          AND flap_seen IS NULL)
      OR (flap_since IS NOT NULL AND flap_count IS NOT NULL AND flap_first_state IS NOT NULL
          AND flap_seen IS NOT NULL))
);
-- Every reading for one entity, which is what a detail page renders.
CREATE INDEX idx_asset_health_entity ON asset_health(entity_type, entity_id);
-- "Has this reporter gone quiet?" -- rule 8's one alertable event, rather than a
-- thousand entities quietly going green.
CREATE INDEX idx_asset_health_reporter ON asset_health(reporter, last_report_at);

-- ---------------------------------------------------------------------------
-- The transition ledger -- the only prunable table in this schema
-- ---------------------------------------------------------------------------

-- A transition is a change in `state` alone, never a struct diff: diffJSON
-- compares every db-tagged field except updated_at, so routing an observation
-- through logUpdate would log every heartbeat via the moving timestamp -- the
-- exact unbounded growth this table exists to prevent (rule 3).
--
-- from_state is nullable because the FIRST observation has no prior and IS a
-- transition. It is the row an incident reviewer looks for, so it is logged.
--
-- kind carries rule 9's flap compression. Above domain.FlapThreshold (5)
-- transitions in domain.FlapWindow (5 min) per (entity, reporter), the writer
-- stops emitting one row per oscillation and opens an episode. flap_open and
-- flap_close are UNCONDITIONAL: a reader must always be able to see that
-- suppression happened and how much it hid, or a stolen token can trip the
-- floor deliberately so the real `down` five minutes later reads as a
-- monitoring artefact. Those constants live in internal/domain -- never env,
-- never a column here, and never writable by a reporter, because a writer that
-- can raise its own suppression threshold has a mute button.
--
-- to_state is NOT NULL for every kind and always means "the state after this
-- row", so the incident timeline (rule 15's UNION ALL of this table with
-- change_log, both RFC3339 UTC TEXT, both sorting correctly on both engines)
-- reads one column. settled_state repeats it on a flap_close because rule 9
-- names the settled value explicitly alongside the window's first and last.
CREATE TABLE observed_transition (
  id            TEXT PRIMARY KEY,
  entity_type   TEXT NOT NULL CHECK (entity_type IN ('asset','service_instance')),
  entity_id     TEXT NOT NULL,
  reporter      TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'transition'
                  CHECK (kind IN ('transition','flap_open','flap_close')),
  from_state    TEXT CHECK (from_state IS NULL OR
                  from_state IN ('up','degraded','down','unknown')),
  to_state      TEXT NOT NULL CHECK (to_state IN ('up','degraded','down','unknown')),
  at            TEXT NOT NULL,
  -- flap_close detail. Null on every other kind.
  flap_count    INTEGER CHECK (flap_count IS NULL OR flap_count > 0),
  window_start  TEXT,
  window_end    TEXT,
  first_state   TEXT CHECK (first_state IS NULL OR
                  first_state IN ('up','degraded','down','unknown')),
  last_state    TEXT CHECK (last_state IS NULL OR
                  last_state IN ('up','degraded','down','unknown')),
  settled_state TEXT CHECK (settled_state IS NULL OR
                  settled_state IN ('up','degraded','down','unknown')),
  -- A flap_close that cannot say how much it hid is worse than no compression.
  CHECK (kind <> 'flap_close' OR (flap_count IS NOT NULL AND window_start IS NOT NULL
         AND window_end IS NOT NULL AND first_state IS NOT NULL
         AND last_state IS NOT NULL AND settled_state IS NOT NULL))
);
-- The 03:00 query: this entity's timeline, newest first.
CREATE INDEX idx_observed_transition_entity ON observed_transition(entity_type, entity_id, at);
-- Rule 9's window count, which is per (entity, reporter), not per entity.
CREATE INDEX idx_observed_transition_window
  ON observed_transition(entity_type, entity_id, reporter, at);
-- The rule 10 prune, which is a range scan on `at` and the only DELETE FROM
-- this codebase permits. Three indexes is affordable precisely because rule 3
-- makes writes here rare -- a heartbeat that changes nothing writes no row.
CREATE INDEX idx_observed_transition_at ON observed_transition(at);

-- ---------------------------------------------------------------------------
-- Reports about entities the inventory does not have
-- ---------------------------------------------------------------------------

-- Rule 6: an observation for an unknown entity is 404 and NEVER creates the
-- entity. It is not dropped either -- an asset the estate has and the inventory
-- does not is a finding, so it queues here and surfaces as drift.
--
-- The column is `entity_ref`, not `entity_id`, deliberately: it is whatever the
-- reporter claimed and it resolves to nothing in this database. Calling it
-- entity_id invites a LEFT JOIN that always returns NULL and, eventually, a
-- foreign key that cannot exist.
--
-- UNIQUE on (entity_type, entity_ref, reporter) with a counter rather than one
-- row per report: a collector pointed at a host this inventory has never heard
-- of will say so every 30 seconds, and a queue that floods is a queue nobody
-- reads. The count and the window are the finding.
CREATE TABLE unmatched_observation (
  id            TEXT PRIMARY KEY,
  entity_type   TEXT NOT NULL CHECK (entity_type IN ('asset','service_instance')),
  entity_ref    TEXT NOT NULL,
  reporter      TEXT NOT NULL,
  state         TEXT NOT NULL CHECK (state IN ('up','degraded','down','unknown')),
  reported_at   TEXT NOT NULL,
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  report_count  INTEGER NOT NULL DEFAULT 1 CHECK (report_count > 0),
  UNIQUE (entity_type, entity_ref, reporter)
);
CREATE INDEX idx_unmatched_observation_last ON unmatched_observation(last_seen_at);

-- ---------------------------------------------------------------------------
-- An operator's override of an observation -- declared, not observed
-- ---------------------------------------------------------------------------

-- Rule 14. A person who knows a reading is wrong writes a row here rather than
-- editing an observed column, which would be clobbered by the next poll thirty
-- seconds later and unattributed besides. The override SHADOWS the observation
-- at read time and never mutates it: the reporter keeps recording the truth
-- underneath, because when the override lapses you need to know what actually
-- happened while it was in force.
--
-- This table is DECLARED state. Create, amend and clear each write a change_log
-- row and sit behind CSRF and RequireAdmin like any other declared mutation.
--
-- expires_at is NOT NULL and Go caps it at 24h from creation, because a
-- permanent override is how a real outage stays invisible for six weeks. The
-- cap is arithmetic on timestamps and cannot be expressed portably in a CHECK,
-- so the CHECK here only enforces the direction; domain.NewHealthOverride
-- enforces the ceiling. `reason` is mandatory for the same reason failure_mode
-- is mandatory on a dependency: writing down why is most of the value.
--
-- No lifecycle 'retired': clearing an override is not retiring an entity, and
-- the vocabulary should not pretend otherwise. Soft-delete still applies -- a
-- cleared override keeps its row and its audit history, because "who silenced
-- this, and for how long" outlives the silence.
--
-- actor holds an opaque app_user.id, never a username (docs/DECISIONS.md,
-- 2026-07-28): any column recording who did something stores an id. No foreign
-- key, matching change_log.actor, so scrubbing an account to satisfy an erasure
-- request cannot be blocked by this table.
CREATE TABLE health_override (
  id             TEXT PRIMARY KEY,
  entity_type    TEXT NOT NULL CHECK (entity_type IN ('asset','service_instance')),
  entity_id      TEXT NOT NULL,
  asserted_state TEXT NOT NULL CHECK (asserted_state IN ('up','degraded','down','unknown')),
  reason         TEXT NOT NULL CHECK (reason <> ''),
  actor          TEXT NOT NULL,
  lifecycle      TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','cleared')),
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  expires_at     TEXT NOT NULL,
  CHECK (expires_at > created_at)
);
-- One active override per entity. Partial, so clearing one frees the entity for
-- a new override rather than blocking it -- the same technique 00007 uses for
-- net_attachment and net_group_member.
CREATE UNIQUE INDEX idx_health_override_one
  ON health_override(entity_type, entity_id) WHERE lifecycle = 'active';
-- "What is silenced right now, and until when."
CREATE INDEX idx_health_override_expiry ON health_override(expires_at);

-- ---------------------------------------------------------------------------
-- service_instance: observed state moves out, provenance gets its CHECK
-- ---------------------------------------------------------------------------

-- Rule 1. UpdateInstance wrote desired_state and observed_state in one
-- statement from a round-tripped struct, so a stale read silently reverted a
-- concurrent operator edit and logUpdate attributed the revert to the human --
-- and CreateInstance's logCreate snapshot put observed values into change_log
-- for every seeded placement. Both stop being possible here rather than being
-- fixed by convention. Nothing has ever read these columns, so no behaviour is
-- lost; the panels that rendered them are removed until GetObservedState and
-- its staleness arithmetic exist, because a health cell with no reporter and no
-- age reads as configuration (rule 2).
ALTER TABLE service_instance DROP COLUMN observed_state;
ALTER TABLE service_instance DROP COLUMN observed_at;

-- Rule 7. service_instance.source was the only unconstrained provenance column
-- in the schema -- dependency.source, app_user.source and all four net_* source
-- columns already have one. No machine may assert that a fact was hand-declared:
-- that laundering is how a fabricated workload inside an in_scope environment
-- renders to an operator as hand-asserted fact and never reaches the conflict
-- queue.
--
-- Adding a CHECK to an existing column is not portable: PostgreSQL has ALTER
-- TABLE ... ADD CONSTRAINT and SQLite has nothing at all. So the column is
-- replaced in four portable steps. The alternative was rebuilding the table,
-- which is worse here: rt_systemd, rt_windows, rt_container and rt_k8s all
-- carry REFERENCES service_instance(id) ON DELETE CASCADE, so DROP TABLE
-- cascades away every runtime-detail row on SQLite and refuses outright on
-- PostgreSQL.
--
-- The UPDATE is deliberately unguarded: a row holding a value outside the new
-- set fails the migration loudly rather than being coerced into 'declared'.
-- Silently rewriting provenance is the exact thing rule 7 exists to stop.
--
-- 'discovered_netstat' is excluded on purpose. netstat discovers connections --
-- dependency edges -- not placements; nothing could ever legitimately set it
-- here, and a vocabulary that admits values the writer cannot produce is not a
-- constraint. See domain.ServiceInstanceSources.
ALTER TABLE service_instance ADD COLUMN source_checked TEXT NOT NULL DEFAULT 'declared'
  CHECK (source_checked IN
    ('declared','discovered_systemd','discovered_k8s','discovered_config'));
UPDATE service_instance SET source_checked = source;
ALTER TABLE service_instance DROP COLUMN source;
ALTER TABLE service_instance RENAME COLUMN source_checked TO source;

-- +goose Down
ALTER TABLE service_instance ADD COLUMN source_unchecked TEXT NOT NULL DEFAULT 'declared';
UPDATE service_instance SET source_unchecked = source;
ALTER TABLE service_instance DROP COLUMN source;
ALTER TABLE service_instance RENAME COLUMN source_unchecked TO source;
ALTER TABLE service_instance ADD COLUMN observed_at TEXT;
ALTER TABLE service_instance ADD COLUMN observed_state TEXT;
DROP TABLE health_override;
DROP TABLE unmatched_observation;
DROP TABLE observed_transition;
DROP TABLE asset_health;
