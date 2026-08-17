-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Who invoiced it (WP-J6, COST-ATTRIBUTION.md §1 question 3).
--
-- THE CEO'S THIRD QUESTION WAS ABOUT A SUPPLIER AND THE ESTATE COULD ONLY
-- ANSWER ABOUT AN ITEM. "Which suppliers raise prices beyond inflation" is the
-- question behind leaving a supplier rather than absorbing an increase, and J2
-- built everything needed to answer it for ONE cost line at a time. What was
-- missing is the dimension: nothing said who a line was owed to.
--
-- ON THE COST LINE, NOT ON THE ASSET, and this is where the roadmap's own
-- wording was wrong. It said to promote `asset.vendor` to a real reference,
-- which attributes a support contract to whoever MADE the box. One server
-- routinely carries hardware bought from a reseller, support from the
-- manufacturer, and a licence from a third party entirely -- three suppliers,
-- three price histories, one asset. A reference on the asset can express one of
-- them, so it would answer the question wrongly and confidently.
--
-- REUSING provider RATHER THAN ADDING A THIRD ORGANISATION TABLE. The estate
-- already holds two, and each was right for its job:
--
--   manufacturer  who MADE it. Referenced by device_type. Unchanged here.
--   provider      an organisation we hold an account with -- it already carries
--                 account_ref and portal_url, which is precisely what you need
--                 to ring a supplier about a price rise.
--
-- `provider` was introduced for circuits and held four telcos, and its meaning
-- is hereby widened to any organisation that invoices us: a telco, a hosting
-- company, a reseller, a licence vendor. Nothing about the table changes -- only
-- what may appear in it -- because a telco IS a supplier and modelling it twice
-- would mean "which supplier" had to union two tables to be answered honestly.
-- A report assembled that way is one nobody trusts.
--
-- NULLABLE, AND IT STAYS NULLABLE. Most estates will fill this in slowly, and a
-- line whose supplier nobody has recorded is the ordinary state rather than an
-- error. The report counts what it could not attribute instead of quietly
-- reporting a total over the half that was labelled.
--
-- WHAT THIS DOES NOT DO: `asset.vendor` is left exactly as it is, free text and
-- drifting. It answers a different question -- who made or sold this box -- and
-- conflating it with who invoices us is the mistake this migration exists to
-- avoid. Tidying it is its own job and is not pretended to here.

-- +goose Up
ALTER TABLE asset_cost ADD COLUMN provider_id TEXT REFERENCES provider(id);
ALTER TABLE service_cost ADD COLUMN provider_id TEXT REFERENCES provider(id);
ALTER TABLE project_cost ADD COLUMN provider_id TEXT REFERENCES provider(id);
ALTER TABLE circuit_cost ADD COLUMN provider_id TEXT REFERENCES provider(id);

-- Every line owed to one supplier, which is the group-by the report runs.
CREATE INDEX idx_asset_cost_provider ON asset_cost(provider_id);
CREATE INDEX idx_service_cost_provider ON service_cost(provider_id);
CREATE INDEX idx_project_cost_provider ON project_cost(provider_id);
CREATE INDEX idx_circuit_cost_provider ON circuit_cost(provider_id);

-- +goose Down
DROP INDEX idx_circuit_cost_provider;
DROP INDEX idx_project_cost_provider;
DROP INDEX idx_service_cost_provider;
DROP INDEX idx_asset_cost_provider;
ALTER TABLE circuit_cost DROP COLUMN provider_id;
ALTER TABLE project_cost DROP COLUMN provider_id;
ALTER TABLE service_cost DROP COLUMN provider_id;
ALTER TABLE asset_cost DROP COLUMN provider_id;
