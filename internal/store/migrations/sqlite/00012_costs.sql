-- What things cost.
--
-- COST ATTACHES TO WHAT IS BILLED, AND DOES NOT INHERIT. A VM's cost is not
-- inherited from its hypervisor -- it is a SHARE of it, and if three guests each
-- inherit the host's monthly figure the estate reports three times one box.
-- Silent double-counting destroys trust in a cost report permanently, and it is
-- the same failure the footprint conflict panel exists to catch one layer up. So
-- a cost line goes on the thing that appears on an invoice, and the footprint
-- derivation that already exists does the rest. Splitting a shared thing is
-- ALLOCATION, needs a driver and a policy, and when it arrives it will be a
-- declared share on a `uses` link -- never a number this schema computes.
--
-- KIND x PERIOD, not three fixed columns. "One-time acquisition / yearly support
-- / monthly operating" is two dimensions wearing one coat; encoding the period
-- into the kind doubles the vocabulary the first time monthly support appears.
--
-- `kind` is a LOOKUP because it is descriptive: a new kind of spend is data, and
-- /vocabularies can add one. `period` is a CHECK with a matching Go constant set
-- because it is BEHAVIOURAL: Go divides a yearly figure by twelve, and a period
-- somebody adds by INSERT would be storable and silently uncomputable. That is
-- the same line migration 00004 drew between service_kind and availability.
--
-- AMOUNTS ARE MINOR UNITS IN INTEGER. Never a float, never NUMERIC or MONEY:
-- their arithmetic differs across the two engines and the whole portability
-- constraint rests on it not doing that. One currency for the estate, held in
-- configuration -- summing mixed currencies needs FX rates with valuation dates,
-- which is a subsystem rather than a column, and is a stated non-goal.
--
-- THE VALIDITY WINDOW IS HERE FROM DAY ONE even though the first release only
-- ever queries "current". Without it a renewal at a new price overwrites its
-- predecessor and the only surviving record is change_log, which is an audit
-- trail rather than something to query -- and "what did we spend in 2025" would
-- then need the log replayed. This repository calls that shape miserable to
-- retrofit, and it is the cheapest thing in this migration.
--
-- THREE TABLES, NOT ONE POLYMORPHIC ONE, mirroring project_asset/project_service.
-- Real foreign keys, so a typo in an import cannot invent a row that inflates a
-- total and belongs to nothing. The rollup merges them in Go, which is what every
-- other multi-entity read in this codebase already does.

-- +goose Up
CREATE TABLE cost_kind (
  code        TEXT PRIMARY KEY,
  label       TEXT NOT NULL,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  description TEXT
);
INSERT INTO cost_kind (code, label, sort_order, description) VALUES
  ('acquisition',  'Acquisition',  10, 'What it cost to buy, once. Hardware, a perpetual licence, an installation fee. Capital rather than a run rate, and reported separately from both -- folding a switch bought once into a monthly figure is how a cost report loses its reader.'),
  ('support',      'Support',      20, 'A vendor maintenance or support contract: the right to a replacement, a patch, or somebody to call. Usually yearly, and usually the thing whose lapse the expiry report is about.'),
  ('licence',      'Licence',      30, 'The right to run the software at all, as opposed to support for it. Separate from support because they are separately cancellable and often separately priced.'),
  ('operating',    'Operating',    40, 'What it costs to keep running, per period, that is not any of the above -- hosting, rack space, a managed service fee. The general bucket, and the one to reach for when nothing more specific fits.'),
  ('power',        'Power',        50, 'Electricity and cooling, where it is billed separately rather than folded into rack space. Belongs on the rack or the site, not on each box in it.'),
  ('connectivity', 'Connectivity', 60, 'Transit, peering, circuits, bandwidth commits. Attaches to whatever the invoice names, which is usually a site or an edge device rather than a service.'),
  ('subscription', 'Subscription', 70, 'A recurring third-party service the estate depends on but does not host: SaaS, a hosted monitor, a domain, a certificate. Typically a project cost, because it attaches to no box and no service anybody here runs.');

-- Assets.
CREATE TABLE asset_cost (
  id           TEXT PRIMARY KEY,
  asset_id     TEXT NOT NULL REFERENCES asset(id),
  kind         TEXT NOT NULL REFERENCES cost_kind(code),
  period       TEXT NOT NULL,
  amount_minor INTEGER NOT NULL,
  note         TEXT,
  valid_from   TEXT NOT NULL,
  valid_until  TEXT,
  lifecycle    TEXT NOT NULL DEFAULT 'active',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  CONSTRAINT asset_cost_period_check      CHECK (period IN ('once','monthly','yearly')),
  CONSTRAINT asset_cost_amount_check      CHECK (amount_minor >= 0),
  CONSTRAINT asset_cost_lifecycle_check   CHECK (lifecycle IN ('active','retired')),
  CONSTRAINT asset_cost_valid_from_check  CHECK (length(valid_from) = 10
                 AND substr(valid_from, 5, 1) = '-' AND substr(valid_from, 8, 1) = '-'),
  CONSTRAINT asset_cost_valid_until_check CHECK (valid_until IS NULL OR (length(valid_until) = 10
                 AND substr(valid_until, 5, 1) = '-' AND substr(valid_until, 8, 1) = '-')),
  -- A window that closes before it opens is not a typo the reader can see: it
  -- simply never matches, and the cost silently vanishes from every total.
  CONSTRAINT asset_cost_window_check      CHECK (valid_until IS NULL OR valid_until >= valid_from)
);
CREATE INDEX idx_asset_cost_asset ON asset_cost(asset_id);

-- Services.
CREATE TABLE service_cost (
  id           TEXT PRIMARY KEY,
  service_id   TEXT NOT NULL REFERENCES service(id),
  kind         TEXT NOT NULL REFERENCES cost_kind(code),
  period       TEXT NOT NULL,
  amount_minor INTEGER NOT NULL,
  note         TEXT,
  valid_from   TEXT NOT NULL,
  valid_until  TEXT,
  lifecycle    TEXT NOT NULL DEFAULT 'active',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  CONSTRAINT service_cost_period_check      CHECK (period IN ('once','monthly','yearly')),
  CONSTRAINT service_cost_amount_check      CHECK (amount_minor >= 0),
  CONSTRAINT service_cost_lifecycle_check   CHECK (lifecycle IN ('active','retired')),
  CONSTRAINT service_cost_valid_from_check  CHECK (length(valid_from) = 10
                 AND substr(valid_from, 5, 1) = '-' AND substr(valid_from, 8, 1) = '-'),
  CONSTRAINT service_cost_valid_until_check CHECK (valid_until IS NULL OR (length(valid_until) = 10
                 AND substr(valid_until, 5, 1) = '-' AND substr(valid_until, 8, 1) = '-')),
  CONSTRAINT service_cost_window_check      CHECK (valid_until IS NULL OR valid_until >= valid_from)
);
CREATE INDEX idx_service_cost_service ON service_cost(service_id);

-- Projects. The money that attaches to no box and no service anybody here runs:
-- a SaaS subscription, a contractor, a support retainer, a domain. Without it a
-- project total is systematically low in a way nobody notices until somebody
-- reconciles it against an invoice and stops believing the page.
CREATE TABLE project_cost (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL REFERENCES project(id),
  kind         TEXT NOT NULL REFERENCES cost_kind(code),
  period       TEXT NOT NULL,
  amount_minor INTEGER NOT NULL,
  note         TEXT,
  valid_from   TEXT NOT NULL,
  valid_until  TEXT,
  lifecycle    TEXT NOT NULL DEFAULT 'active',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  CONSTRAINT project_cost_period_check      CHECK (period IN ('once','monthly','yearly')),
  CONSTRAINT project_cost_amount_check      CHECK (amount_minor >= 0),
  CONSTRAINT project_cost_lifecycle_check   CHECK (lifecycle IN ('active','retired')),
  CONSTRAINT project_cost_valid_from_check  CHECK (length(valid_from) = 10
                 AND substr(valid_from, 5, 1) = '-' AND substr(valid_from, 8, 1) = '-'),
  CONSTRAINT project_cost_valid_until_check CHECK (valid_until IS NULL OR (length(valid_until) = 10
                 AND substr(valid_until, 5, 1) = '-' AND substr(valid_until, 8, 1) = '-')),
  CONSTRAINT project_cost_window_check      CHECK (valid_until IS NULL OR valid_until >= valid_from)
);
CREATE INDEX idx_project_cost_project ON project_cost(project_id);

-- +goose Down
DROP TABLE project_cost;
DROP TABLE service_cost;
DROP TABLE asset_cost;
DROP TABLE cost_kind;
