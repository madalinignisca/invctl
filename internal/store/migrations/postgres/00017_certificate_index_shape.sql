-- The certificate link indexes carry the lifecycle they are always filtered by,
-- and the three that serve no query go.
--
-- Same argument as 00016, and measured the same way by a database review on a
-- synthetic estate of 60,000 certificates, 195,000 names and 180,000
-- deployments.
--
-- WHAT IS ADDED. Every read of the two link tables pairs `certificate_id = ?`
-- with `lifecycle = 'active'`, and lifecycle was not in the primary key, so both
-- engines found the row by index and then visited the heap for one column:
--
--   expiryCertificateReach   PostgreSQL 590 ms -> 136 ms   SQLite 87 ms -> 68 ms
--   certificateSelect        453,527 buffers -> 284,117, Heap Fetches: 0
--
-- The reach loop was costing about seven times the rest of the expiry report put
-- together. No code changes; both engines get an index-only scan.
--
-- WHAT IS REMOVED, and why it was wrong to add. idx_certificate_san_name,
-- idx_certificate_asset_asset and idx_certificate_service_service index the
-- NON-leading column of each table, and nothing queries in that direction: the
-- host filter runs in Go because a wildcard match is not a LIKE, search resolves
-- through search_index, and no page looks up certificates from an asset or a
-- service. They were built for reverse lookups that were never written.
--
-- 7.9 MB of index on a 12 MB table, and 3% on the SAN replacement path. Not
-- dramatic -- but it is the idx_identity_team argument from 00016 verbatim, and
-- an index nothing uses is a claim about a query that does not exist. If a
-- reverse lookup is ever added, the index comes back shaped for it rather than
-- guessed at now.
--
-- WHAT IS DELIBERATELY LEFT ALONE. idx_certificate_team stays `(team_id)` and is
-- NOT reshaped the way 00016 reshaped its three. Measured: identical plan and
-- identical buffer count either way. The teams case gained because teamSelect's
-- subqueries are COUNTs answerable from the index alone; certificateSelect is
-- `SELECT c.*` and must visit the heap regardless, so adding lifecycle buys
-- nothing. Applying a previous migration's conclusion mechanically is how a
-- schema fills up with indexes nobody measured.

-- +goose Up
CREATE INDEX idx_certificate_asset_current   ON certificate_asset(certificate_id, lifecycle);
CREATE INDEX idx_certificate_service_current ON certificate_service(certificate_id, lifecycle);

DROP INDEX idx_certificate_san_name;
DROP INDEX idx_certificate_asset_asset;
DROP INDEX idx_certificate_service_service;

-- +goose Down
CREATE INDEX idx_certificate_san_name ON certificate_san(name);
CREATE INDEX idx_certificate_asset_asset ON certificate_asset(asset_id);
CREATE INDEX idx_certificate_service_service ON certificate_service(service_id);

DROP INDEX idx_certificate_service_current;
DROP INDEX idx_certificate_asset_current;
