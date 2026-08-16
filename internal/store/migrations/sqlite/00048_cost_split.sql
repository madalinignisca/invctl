-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- How much of a cluster's cost is CPU and how much is memory (WP-J4).
--
-- ONE INVOICE BUYS TWO DIMENSIONS, AND THE SPECIFICATION DOES NOT SAY HOW TO
-- SEPARATE THEM. COST-ATTRIBUTION.md §5.1 asserts that a price per core and a
-- price per GB both fall out of "cluster cost ÷ usable capacity", which is true
-- of storage -- a pool is its own asset with its own invoice -- and silently
-- under-specified for compute. A hypervisor costs one number and provides both,
-- while a project routinely holds 12.5% of the CPU and 15.63% of the memory.
--
-- Three answers were considered and this is the one taken:
--
--   * a declared split, here;
--   * weighting each project by its BINDING dimension and normalising, which
--     needs no declaration but is a rule no finance reader has seen before;
--   * a dimension per cost LINE, which is the most honest and asks somebody to
--     decompose every server invoice by hand -- so most never would, and the
--     gap report would fill with lines nobody intends to fix.
--
-- The declared split reconciles by construction, is one figure a person can
-- state and defend in a sentence, and is tunable per cluster: a memory-dense
-- box and a CPU-dense one differ honestly rather than by accident.
--
-- WHAT IT COSTS, SAID PLAINLY: the number is a judgement. Nothing on the
-- invoice says 60/40. Somebody decides it and owns it, and because it is
-- declared it is audited, versioned and attributable like every other decision
-- in this system -- which is the difference between a judgement and a guess.
--
-- NULL IS NOT 50/50. Unlike cluster.cpu_overcommit, which defaults to a
-- pessimistic 1:1 because there IS a safe direction to be wrong in, an
-- undeclared split has no conservative reading -- half and half is not cautious,
-- it is arbitrary. So an undeclared split divides no money at all and is
-- reported as a gap. The shares still divide: "who holds 12% of this cluster"
-- needs no money and is answered regardless.
--
-- PERCENT OF THE POOL ATTRIBUTABLE TO CPU; memory takes the remainder. One
-- column rather than two, because two would permit a pair that does not sum to
-- a hundred and there is no honest thing to do with that.
--
-- Storage is deliberately absent from this split. A pool is a separate asset
-- carrying its own invoice, so it divides by its own dimension already and has
-- nothing to be blended with.

-- +goose Up
ALTER TABLE cluster ADD COLUMN cost_split_cpu INTEGER;

ALTER TABLE cluster ADD CONSTRAINT cluster_cost_split_check
  CHECK (cost_split_cpu IS NULL OR (cost_split_cpu >= 0 AND cost_split_cpu <= 100));

-- +goose Down
ALTER TABLE cluster DROP CONSTRAINT cluster_cost_split_check;
ALTER TABLE cluster DROP COLUMN cost_split_cpu;
