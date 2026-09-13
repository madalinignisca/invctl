-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- A cable can now be corrected (WP-Link, closing the writeSurfaceGaps entry
-- for Link), so it needs the token -- the same reasoning 00063 gave net_group
-- and net_anchor when their first correction path arrived.
--
-- WITHOUT IT: two operators correcting the same cable's medium and length at
-- the same time silently overwrite each other, and TestEveryEditFormCarriesItsVersion
-- (internal/web) derives its population from the handlers that reach
-- submittedVersion -- a correction path built without a token is not flagged
-- by it, it is simply ABSENT from the population, which is the silent-
-- exclusion shape that census exists to prevent one layer down.
--
-- ONLY medium AND length_m ARE REACHABLE THROUGH UpdateLink. a_interface_id
-- and b_interface_id stay off the UPDATE statement entirely -- the endpoints
-- are the cable's identity, and pointing a cable at a different port is a
-- different cable (withdraw-and-re-patch, matching what physically happened),
-- exactly the reasoning writeSurfaceByDesign already records for NetUplink,
-- CircuitTermination and NetAttachment. Link earns its own correction path
-- only because, unlike those three, it carries attributes beyond its two
-- endpoints.

-- +goose Up
ALTER TABLE link ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE link DROP COLUMN row_version;
