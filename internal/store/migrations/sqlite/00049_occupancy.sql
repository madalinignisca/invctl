-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Several tenants in one machine (WP-J5, COST-ATTRIBUTION.md §5.4).
--
-- ESTATES PACK TENANTS INTO ONE VM TO SAVE ON LICENSING, and when they do, the
-- ownership model this system already has answers the wrong question. A project
-- OWNS an asset -- at most one does, enforced by a partial unique index -- and
-- everything J4 divides follows from that: the whole of a machine's capacity
-- lands on its owner. For a box carrying four clients' applications that is not
-- an approximation, it is a wrong answer given confidently.
--
-- DECLARED BY A PERSON, NEVER INFERRED. There is no measurement that separates
-- four tenants inside one operating system -- not CPU time, not memory, not
-- disk. Anything this system computed would be a guess wearing an authoritative
-- number's clothes, and §8's first open question already says so: attribution
-- requires modelling, and where the modelling has not been done the honest
-- output is a gap rather than a figure.
--
-- SO THE PERCENTAGES ARE AN OPINION, AND THEY ARE AUDITED LIKE ONE. Somebody
-- decides that the shared application server is 40% one engagement and 30% each
-- of two others, and the change_log records who and when. That is the same
-- standing as cluster.cost_split_cpu, and for the same reason: a judgement
-- recorded is worth more than a measurement nobody can take.
--
-- WHEN THEY DO NOT TOTAL 100 THAT IS A FINDING, NOT A SILENT ROUNDING -- §5.4's
-- own words. Under-declared leaves the remainder attributed to nobody, which is
-- visible and fixable; normalising it away would quietly inflate every declared
-- share and there would be nothing on any page to notice.
--
-- WHOLE PERCENT, not hundredths. This is a judgement somebody made in a meeting
-- and nobody defends a tenant's share to two decimal places; offering the
-- precision would invite an argument the number cannot support. Contrast
-- cluster.cpu_overcommit, which is hundredths because 2.5:1 is a real
-- configuration somebody sets.
--
-- IT DOES NOT REPLACE OWNERSHIP. An occupied asset still has an owner -- who is
-- answerable for it, who is called when it breaks -- and occupancy only changes
-- how its COST and CAPACITY divide. Conflating the two would mean a machine
-- four projects share belongs to nobody.

-- +goose Up

CREATE TABLE asset_occupant (
  asset_id   TEXT NOT NULL REFERENCES asset(id),
  project_id TEXT NOT NULL REFERENCES project(id),
  -- Whole percent of this asset attributable to this project.
  percent    INTEGER NOT NULL,
  -- Why this number and not another. The field that makes the judgement
  -- defensible in six months, when whoever agreed it has moved on.
  note       TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (asset_id, project_id),
  CONSTRAINT asset_occupant_percent_check CHECK (percent > 0 AND percent <= 100)
);

-- Every occupant of one asset, which is the split query.
CREATE INDEX idx_asset_occupant_asset ON asset_occupant(asset_id);

-- +goose Down
DROP INDEX idx_asset_occupant_asset;
DROP TABLE asset_occupant;
