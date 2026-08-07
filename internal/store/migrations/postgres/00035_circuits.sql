-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Circuits: the connectivity somebody signs a contract for.
--
-- The estate already records the PORT a handoff lands on -- fw-edge-1 has three
-- WAN interfaces called after their providers. That is the physical fact and it
-- says nothing anybody asks about at renewal time: who sells it, what it costs
-- a month, what was committed, and when the contract ends. A circuit is that
-- second half, and it is the half with a date on it.
--
-- ONLY THE COST AND EXPIRY HALF OF WP-E1, deliberately. The roadmap's appendix
-- is right that the work package bundles two sizes: this is a table with a
-- monthly figure and a contract date, and the reachability half needs a
-- site-to-site edge that internal/impact/reach.go has no concept of -- it works
-- over network groups, members, uplinks and anchors, and there is no site in
-- any of them. Shipping the small half first is not a shortcut; the two share
-- nothing but a name.
--
-- A TERMINATION NAMES A SITE OR A PORT, never both, for the reason a VLAN
-- termination does: a row naming two ends says the circuit lands twice, and one
-- naming neither lands nowhere while looking like a connection. Both ends of a
-- circuit are the same table -- the A side and the Z side -- because "which end
-- is ours" is a question about the terminations and not about the circuit.

-- +goose Up
CREATE TABLE provider (
  id           TEXT PRIMARY KEY NOT NULL,
  name         TEXT NOT NULL,
  -- The account or customer reference, for the person who has to ring them.
  -- A reference, never a person: the team.contact_ref rule applies.
  account_ref  TEXT,
  portal_url   TEXT,
  description  TEXT,
  lifecycle    TEXT NOT NULL DEFAULT 'active'
                 CONSTRAINT provider_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  row_version  INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX provider_name_key ON provider(name) WHERE lifecycle <> 'retired';

CREATE TABLE circuit (
  id           TEXT PRIMARY KEY NOT NULL,
  -- The provider's own identifier for it. This is what you quote when you ring
  -- them at 03:00, so it is the natural key rather than a name we invent.
  cid          TEXT NOT NULL,
  provider_id  TEXT NOT NULL REFERENCES provider(id),
  -- Free text like prefix.role: "DIA", "wavelength", "broadband", "MPLS". The
  -- set varies by what an estate actually buys, and a CHECK here would refuse
  -- the next thing somebody orders.
  service_type TEXT,
  -- Committed rate in megabits. Nullable: plenty of circuits are sold as
  -- "up to" and recording a commit for those would be inventing one.
  commit_mbps  INTEGER
                 CONSTRAINT circuit_commit_check CHECK (commit_mbps IS NULL OR commit_mbps > 0),
  install_date TEXT
                 CONSTRAINT circuit_install_check
                 CHECK (install_date IS NULL OR (length(install_date) = 10
                   AND substr(install_date, 5, 1) = '-' AND substr(install_date, 8, 1) = '-')),
  -- WHEN THE CONTRACT ENDS, and the reason this table joins the expiry report.
  -- It is not an end of support: nothing stops working, somebody either
  -- renegotiates or is auto-renewed at a rate nobody checked.
  contract_end TEXT
                 CONSTRAINT circuit_contract_end_check
                 CHECK (contract_end IS NULL OR (length(contract_end) = 10
                   AND substr(contract_end, 5, 1) = '-' AND substr(contract_end, 8, 1) = '-')),
  description  TEXT,
  lifecycle    TEXT NOT NULL DEFAULT 'active'
                 CONSTRAINT circuit_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  row_version  INTEGER NOT NULL DEFAULT 1
);
-- A provider's circuit ID is unique to that provider, never globally: two
-- carriers reusing a number is ordinary and neither knows about the other.
CREATE UNIQUE INDEX circuit_provider_cid_key ON circuit(provider_id, cid) WHERE lifecycle <> 'retired';
CREATE INDEX idx_circuit_contract_end ON circuit(contract_end) WHERE contract_end IS NOT NULL;

CREATE TABLE circuit_termination (
  id           TEXT PRIMARY KEY NOT NULL,
  circuit_id   TEXT NOT NULL REFERENCES circuit(id),
  -- Which end. Two sides, and the schema does not care which is "ours".
  side         TEXT NOT NULL
                 CONSTRAINT circuit_termination_side_check CHECK (side IN ('a','z')),
  asset_id     TEXT REFERENCES asset(id),
  interface_id TEXT REFERENCES interface(id) ON DELETE CASCADE,
  lifecycle    TEXT NOT NULL DEFAULT 'active'
                 CONSTRAINT circuit_termination_lifecycle_check
                 CHECK (lifecycle IN ('active','retired')),
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  row_version  INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT circuit_termination_one_end_check
    CHECK ((asset_id IS NOT NULL AND interface_id IS NULL)
        OR (asset_id IS NULL AND interface_id IS NOT NULL))
);
CREATE INDEX idx_circuit_termination_circuit ON circuit_termination(circuit_id);
CREATE INDEX idx_circuit_termination_interface ON circuit_termination(interface_id);
-- One row per side per circuit: a circuit has an A end and a Z end, and a
-- second A end is a contradiction rather than extra information.
CREATE UNIQUE INDEX circuit_termination_side_key ON circuit_termination(circuit_id, side)
  WHERE lifecycle <> 'retired';

-- The fourth cost surface, identical in shape to the other three so the
-- existing rollup, validity windows and amendment behaviour apply unchanged.
CREATE TABLE circuit_cost (
  id           TEXT NOT NULL PRIMARY KEY,
  circuit_id   TEXT NOT NULL REFERENCES circuit(id),
  kind         TEXT NOT NULL REFERENCES cost_kind(code),
  period       TEXT NOT NULL,
  amount_minor BIGINT NOT NULL,
  note         TEXT,
  valid_from   TEXT NOT NULL,
  valid_until  TEXT,
  lifecycle    TEXT NOT NULL DEFAULT 'active',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  row_version  INTEGER NOT NULL DEFAULT 1,
  CONSTRAINT circuit_cost_period_check      CHECK (period IN ('once','monthly','yearly')),
  CONSTRAINT circuit_cost_amount_check      CHECK (amount_minor >= 0),
  CONSTRAINT circuit_cost_lifecycle_check   CHECK (lifecycle IN ('active','retired')),
  CONSTRAINT circuit_cost_valid_from_check  CHECK (length(valid_from) = 10
                 AND substr(valid_from, 5, 1) = '-' AND substr(valid_from, 8, 1) = '-'),
  CONSTRAINT circuit_cost_valid_until_check CHECK (valid_until IS NULL OR (length(valid_until) = 10
                 AND substr(valid_until, 5, 1) = '-' AND substr(valid_until, 8, 1) = '-'))
);
CREATE INDEX idx_circuit_cost_circuit ON circuit_cost(circuit_id);

-- +goose Down
DROP INDEX idx_circuit_cost_circuit;
DROP TABLE circuit_cost;
DROP INDEX circuit_termination_side_key;
DROP INDEX idx_circuit_termination_interface;
DROP INDEX idx_circuit_termination_circuit;
DROP TABLE circuit_termination;
DROP INDEX idx_circuit_contract_end;
DROP INDEX circuit_provider_cid_key;
DROP TABLE circuit;
DROP INDEX provider_name_key;
DROP TABLE provider;
