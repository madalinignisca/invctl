-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A NETWORK AND AN ADDRESS COULD ONLY EVER BE ADDED. The write-surface census
-- (internal/store/write_surface_test.go) carried both as gaps, and their
-- entries say why this is worse than a missing correction elsewhere: a prefix
-- declared in error "keeps taking part in every containment answer computed
-- over the tree", and a freed address "cannot be released, so the allocator
-- keeps treating it as taken". There is no withdraw-and-redeclare workaround
-- available, because there is nothing to withdraw.
--
-- ip_range and aggregate -- the tables either side of these in the addressing
-- model -- have carried lifecycle since they were created. These two were the
-- exception, not the rule.
--
-- Additive with a default, so every existing row reads active and no
-- constraint can fail on data a visitor to the public demo created.

-- +goose Up
ALTER TABLE prefix ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CONSTRAINT prefix_lifecycle_check CHECK (lifecycle IN ('active','retired'));
CREATE INDEX idx_prefix_lifecycle ON prefix(lifecycle);

ALTER TABLE ip_address ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CONSTRAINT ip_address_lifecycle_check CHECK (lifecycle IN ('active','retired'));
CREATE INDEX idx_ip_address_lifecycle ON ip_address(lifecycle);

-- THE UNIQUE INDEXES MUST ONLY BIND LIVE ROWS, or withdrawal is a trapdoor:
-- `10.0.0.0/8` withdrawn stays taken for ever, and CreatePrefix answers "that
-- network is already declared" -- which is false, and names a row the operator
-- cannot see to do anything about. RetireInterface's comment makes the general
-- point: a withdrawal you cannot undo by redeclaring is worse than not being
-- able to withdraw at all.
--
-- A NEW ROW RATHER THAN REACTIVATION, which is where this parts company with
-- interface. A port re-added is the same physical port, so it comes back as
-- itself, id and history intact. A CIDR is not an object; redeclaring
-- 10.0.0.0/8 two years later is a fresh assertion about a different network
-- that happens to occupy the same numbers, and reactivating would drag the old
-- row's environment, role and VLAN binding back with it. Two rows is also what
-- keeps "when was the old one withdrawn" answerable -- the reasoning
-- user_project's partial index already carries.
DROP INDEX prefix_vrf_cidr_key;
DROP INDEX prefix_global_cidr_key;
CREATE UNIQUE INDEX prefix_vrf_cidr_key    ON prefix(vrf_id, cidr_text)
  WHERE vrf_id IS NOT NULL AND lifecycle = 'active';
CREATE UNIQUE INDEX prefix_global_cidr_key ON prefix(cidr_text)
  WHERE vrf_id IS NULL     AND lifecycle = 'active';

-- +goose Down
DROP INDEX prefix_global_cidr_key;
DROP INDEX prefix_vrf_cidr_key;
CREATE UNIQUE INDEX prefix_vrf_cidr_key    ON prefix(vrf_id, cidr_text) WHERE vrf_id IS NOT NULL;
CREATE UNIQUE INDEX prefix_global_cidr_key ON prefix(cidr_text)         WHERE vrf_id IS NULL;
DROP INDEX idx_ip_address_lifecycle;
ALTER TABLE ip_address DROP COLUMN lifecycle;
DROP INDEX idx_prefix_lifecycle;
ALTER TABLE prefix DROP COLUMN lifecycle;
