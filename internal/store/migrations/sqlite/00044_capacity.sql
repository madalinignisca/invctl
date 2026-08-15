-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Compute capacity (WP-J3).
--
-- THE ESTATE MODELS RACK UNITS, WEIGHT AND DEPTH AND NOTHING ABOUT COMPUTE.
-- Migration 00038 measured the cabinets; nothing has ever recorded how big the
-- machines in them are. Every figure WP-J4 computes divides a cluster's cost by
-- its capacity, so this is the prerequisite rather than a feature of its own --
-- and it is useful before any money is involved, because it answers "is this
-- cluster oversubscribed" on its own.
--
-- COLUMNS ON asset, FOLLOWING 00038 EXACTLY. The alternative was a side table
-- keyed by asset and dimension, which normalises tidily and then makes every
-- capacity question a join. This estate already carries usable_depth_mm and
-- max_load_grams as nullable columns on the same table for the same reason:
-- most assets have no value for them, and that is the ordinary state rather
-- than an error.
--
-- THREE NUMBERS PER DIMENSION, AND THEY ARE DIFFERENT FACTS (see
-- docs/COST-ATTRIBUTION.md §5.5):
--
--   physical    what the machine HAS. A property of a host.
--   provisioned the hard limit configured on a workload.
--   allocated   the figure money is computed on.
--
-- All three are DECLARED. A person agrees a VM gets eight cores; a hypervisor
-- reporting thirty percent CPU is telemetry and belongs to the observed path
-- with its own audit obligations. That separation is why adding two more
-- numbers adds no new class of fact.
--
-- NULL IS "NOT RECORDED", NEVER ZERO, and every check built on these reports a
-- gap rather than assuming a value -- the same rule airflow and port_face
-- follow. An estate that has recorded nothing must report that it knows
-- nothing, not that everything fits.
--
-- MEGABYTES FOR MEMORY, whole cores for CPU. Integers throughout, for the
-- reason money is minor units: these get summed across a cluster and divided
-- into a cost, and a float that drifts in the last place turns a reconciling
-- total into one that is out by a cent nobody can find.

-- +goose Up

-- What the machine has. A host's own capacity.
ALTER TABLE asset ADD COLUMN cpu_cores INTEGER;
ALTER TABLE asset ADD COLUMN memory_mb INTEGER;

-- What a workload may take, and what it is charged for.
--
-- PROVISIONED AND ALLOCATED ARE BOTH KEPT because they answer different
-- questions and routinely differ: a VM given 16 GB "to be safe" while the deal
-- was priced on 8 is not a mistake, it is a decision somebody made without
-- pricing it, and the gap between these two columns is what makes that visible.
ALTER TABLE asset ADD COLUMN vcpu_provisioned INTEGER;
ALTER TABLE asset ADD COLUMN vcpu_allocated INTEGER;
ALTER TABLE asset ADD COLUMN memory_provisioned_mb INTEGER;
ALTER TABLE asset ADD COLUMN memory_allocated_mb INTEGER;

ALTER TABLE asset ADD CONSTRAINT asset_cpu_cores_check
  CHECK (cpu_cores IS NULL OR cpu_cores > 0);
ALTER TABLE asset ADD CONSTRAINT asset_memory_mb_check
  CHECK (memory_mb IS NULL OR memory_mb > 0);
ALTER TABLE asset ADD CONSTRAINT asset_vcpu_provisioned_check
  CHECK (vcpu_provisioned IS NULL OR vcpu_provisioned > 0);
ALTER TABLE asset ADD CONSTRAINT asset_vcpu_allocated_check
  CHECK (vcpu_allocated IS NULL OR vcpu_allocated > 0);
ALTER TABLE asset ADD CONSTRAINT asset_memory_provisioned_check
  CHECK (memory_provisioned_mb IS NULL OR memory_provisioned_mb > 0);
ALTER TABLE asset ADD CONSTRAINT asset_memory_allocated_check
  CHECK (memory_allocated_mb IS NULL OR memory_allocated_mb > 0);

-- How far the operator is willing to oversubscribe this cluster's CPU.
--
-- DECLARED, PER CLUSTER, AND NEVER INFERRED FROM LOAD. It is a judgement its
-- operator makes, and deriving it from observed utilisation would let a quiet
-- cluster raise its own apparent safe ratio -- licensing exactly the
-- overcommitment the finding exists to catch.
--
-- Hundredths, so 300 is 3.0:1. Memory is deliberately absent: it is rarely
-- overcommitted and doing so has a different failure mode from CPU contention,
-- so a single ratio covering both would be one number pretending to be two.
ALTER TABLE cluster ADD COLUMN cpu_overcommit INTEGER;
ALTER TABLE cluster ADD CONSTRAINT cluster_cpu_overcommit_check
  CHECK (cpu_overcommit IS NULL OR cpu_overcommit BETWEEN 100 AND 6400);

-- +goose Down
ALTER TABLE cluster DROP CONSTRAINT cluster_cpu_overcommit_check;
ALTER TABLE cluster DROP COLUMN cpu_overcommit;

ALTER TABLE asset DROP CONSTRAINT asset_memory_allocated_check;
ALTER TABLE asset DROP CONSTRAINT asset_memory_provisioned_check;
ALTER TABLE asset DROP CONSTRAINT asset_vcpu_allocated_check;
ALTER TABLE asset DROP CONSTRAINT asset_vcpu_provisioned_check;
ALTER TABLE asset DROP CONSTRAINT asset_memory_mb_check;
ALTER TABLE asset DROP CONSTRAINT asset_cpu_cores_check;

ALTER TABLE asset DROP COLUMN memory_allocated_mb;
ALTER TABLE asset DROP COLUMN memory_provisioned_mb;
ALTER TABLE asset DROP COLUMN vcpu_allocated;
ALTER TABLE asset DROP COLUMN vcpu_provisioned;
ALTER TABLE asset DROP COLUMN memory_mb;
ALTER TABLE asset DROP COLUMN cpu_cores;
