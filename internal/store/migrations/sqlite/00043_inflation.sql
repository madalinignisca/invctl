-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- What money did, so a price rise can be judged against it (WP-J2).
--
-- WITHOUT THIS, "UP 23%" IS A NUMBER AND NOT AN ANSWER. The question the estate
-- owner actually asks is whether a supplier raised prices faster than money
-- fell, and that cannot be answered from the estate's own data at all -- it
-- needs a figure from outside. One row per year, typed by a person from a
-- published statistic, and nothing here fetches it: invariant 7 says no
-- outbound calls, and a rate that arrived on its own would be a fact nobody
-- chose and nobody could date.
--
-- BASIS POINTS, NOT A DECIMAL. 3.2% is 320. The same argument as money in minor
-- units: a rate is compounded and compared, and binary floating point is wrong
-- in ways that only surface once somebody multiplies three of them together.
-- Two decimal places is finer than any published index quotes.
--
-- SIGNED, because deflation is a real year. A negative rate must round-trip
-- rather than being refused as impossible by somebody who has only seen
-- inflation.
--
-- THE YEAR IS THE KEY. One rate per year, and no region column: a deployment
-- serves one company that buys in one currency, which is the same assumption
-- INV_CURRENCY already makes. A second dimension here would need a second one
-- on every cost line to say which series applies, and nobody has asked for
-- that.

-- +goose Up
CREATE TABLE inflation_rate (
  -- TEXT, NOT INTEGER, AND THE GUARD FOUND OUT WHY. `INTEGER PRIMARY KEY` is
  -- SQLite's rowid alias and creates no separate index, while PostgreSQL builds
  -- one for the constraint -- so the two engines disagreed about the shape of
  -- the same table and TestIndexesMatchAcrossEngines said so. Four characters
  -- sort correctly as text, which is the same reason every date here is TEXT.
  year        TEXT PRIMARY KEY NOT NULL
                CONSTRAINT inflation_rate_year_check
                CHECK (length(year) = 4 AND year >= '1900' AND year <= '2200'),
  -- Hundredths of a percent. 320 is 3.2%; -50 is deflation of half a percent.
  -- Bounded because a typo of 3200 for 320 would otherwise silently report
  -- every price in the estate as a bargain.
  basis_points INTEGER NOT NULL
                CONSTRAINT inflation_rate_bp_check CHECK (basis_points BETWEEN -5000 AND 50000),
  -- Where the figure came from. Not decoration: a rate somebody cannot source
  -- is a rate nobody can defend in the conversation this exists to support.
  source      TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  row_version INTEGER NOT NULL DEFAULT 1
);

-- +goose Down
DROP TABLE inflation_rate;
