-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- What the deal was priced on (WP-J7).
--
-- NOT CALLED `contracted`, AND THE NAME IS THE POINT. These contracts specify
-- no hardware or software resources at all -- nobody was promised a number of
-- cores. What exists is the assumption the price was built on, and a column
-- called `contracted` in a system with a permanent audit trail will eventually
-- be exported, screenshotted, and quoted at a client as though somebody had
-- promised it. That is a liability created by a column name, and it costs
-- nothing to avoid today. See docs/COST-ATTRIBUTION.md §5.5.
--
-- ON THE PROJECT, BECAUSE THAT IS WHERE THE QUOTE WAS MADE. A deal is priced
-- once for an engagement, not per virtual machine, and putting it here means it
-- survives the workloads underneath being rearranged -- which they will be, and
-- which must not silently change what the engagement was sold on.
--
-- THE ALERT IT ENABLES IS ABOUT MARGIN, NOT BREACH. Exceeding a contract is a
-- liability question. Exceeding a pricing assumption is a profitability one:
-- nothing is breached, nobody is owed anything, and the engagement is quietly
-- becoming worth less than the quote assumed. It fires precisely when the
-- infrastructure team responds to demand, which is the moment nobody thinks to
-- tell whoever owns the commercial relationship.
--
-- NULL IS "NOT PRICED ON A RESOURCE ASSUMPTION", which is the ordinary state
-- for internal projects and for anything sold on a flat fee. It is reported as
-- a gap where it matters and never as a fault.

-- +goose Up
ALTER TABLE project ADD COLUMN priced_for_vcpu INTEGER;
ALTER TABLE project ADD COLUMN priced_for_memory_mb INTEGER;

ALTER TABLE project ADD CONSTRAINT project_priced_for_vcpu_check
  CHECK (priced_for_vcpu IS NULL OR priced_for_vcpu > 0);
ALTER TABLE project ADD CONSTRAINT project_priced_for_memory_check
  CHECK (priced_for_memory_mb IS NULL OR priced_for_memory_mb > 0);

-- +goose Down
ALTER TABLE project DROP CONSTRAINT project_priced_for_memory_check;
ALTER TABLE project DROP CONSTRAINT project_priced_for_vcpu_check;
ALTER TABLE project DROP COLUMN priced_for_memory_mb;
ALTER TABLE project DROP COLUMN priced_for_vcpu;
