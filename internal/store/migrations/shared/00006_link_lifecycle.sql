-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Cables get unpatched, and soft-delete-only is a hard rule everywhere else in
-- this schema (docs/DECISIONS.md, 2026-07-28 decisions). A retired link keeps
-- its row and audit history; it is simply excluded from every far-end lookup.
ALTER TABLE link ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
  CHECK (lifecycle IN ('active','retired'));
CREATE INDEX idx_link_lifecycle ON link(lifecycle);

-- +goose Down
DROP INDEX idx_link_lifecycle;
ALTER TABLE link DROP COLUMN lifecycle;
