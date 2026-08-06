-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- The reverse lookup exists now, so the indexes come back shaped for it.
--
-- Migration 00017 dropped idx_certificate_asset_asset and
-- idx_certificate_service_service because nothing queried in that direction, and
-- said so: "if a reverse lookup is ever added, the index comes back shaped for
-- it rather than guessed at now." This is that.
--
-- The query the asset and service pages now run is
--
--   WHERE asset_id = ? AND lifecycle = 'active'
--
-- so lifecycle is the second column, not absent. The dropped indexes were on the
-- bare column and would have sent both engines to the heap for it -- the same
-- shape 00016 and 00017 both had to correct. Guessing the shape a year early
-- would have produced the wrong index and the appearance of having thought
-- about it.
--
-- WHY THE REVERSE VIEW MATTERS. A certificate page already lists where it is
-- deployed. The question during an incident runs the other way: this box is
-- serving a certificate error, what is on it -- and until now that was
-- unanswerable from the asset page.

-- +goose Up
CREATE INDEX idx_certificate_asset_reverse   ON certificate_asset(asset_id, lifecycle);
CREATE INDEX idx_certificate_service_reverse ON certificate_service(service_id, lifecycle);

-- +goose Down
DROP INDEX idx_certificate_service_reverse;
DROP INDEX idx_certificate_asset_reverse;
