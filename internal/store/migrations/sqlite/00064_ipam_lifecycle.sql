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

-- +goose Down
DROP INDEX idx_ip_address_lifecycle;
ALTER TABLE ip_address DROP COLUMN lifecycle;
DROP INDEX idx_prefix_lifecycle;
ALTER TABLE prefix DROP COLUMN lifecycle;
