-- Every primary key says NOT NULL.
--
-- `id TEXT PRIMARY KEY` leaves the column NULLABLE on SQLite. Only an INTEGER
-- PRIMARY KEY (the rowid alias) is implicitly NOT NULL; every other type is not,
-- and the documentation calls this a long-standing bug kept for compatibility.
-- PostgreSQL implies NOT NULL for every primary key, so the two engines have
-- disagreed about 30 columns since the first migration.
--
-- WHY IT MATTERS, beyond tidiness. SQLite treats NULLs as DISTINCT in a unique
-- index, so a nullable primary key admits any number of NULL rows. Such a row is
-- invisible to every statement in this codebase keyed on `WHERE id = ?` -- it can
-- never be read back, edited or retired -- while still being counted by every
-- aggregate. A cost line like that inflates a total that nobody can then trace.
--
-- Nothing in the application can produce one today: ids are UUIDv7 generated in
-- Go and never null. This closes the gap anyway, because the cost of doing so is
-- one statement per column now and a table rebuild after sign-off, and because
-- the guarantee is what the test asserts rather than the current behaviour of
-- the callers.
--
-- Found by TestColumnShapesMatchAcrossEngines, which was written after a
-- database review pointed out that TestConstraintNamesMatchAcrossEngines
-- compares a flat set of CHECK names and can therefore see neither nullability
-- nor which table a constraint belongs to.
--
-- Measured on the pinned driver (modernc.org/sqlite v1.54.0, SQLite 3.53.3):
-- ALTER COLUMN ... SET NOT NULL succeeds, VALIDATES existing rows (it refuses
-- when a NULL is already present), needs no table rebuild, and DROP NOT NULL
-- reverses it. The stored DDL text does not change -- the constraint is enforced
-- without appearing in sqlite_master -- which is why the shape test asks
-- pragma_table_info instead of parsing DDL.

-- +goose Up
ALTER TABLE app_user ALTER COLUMN id SET NOT NULL;
ALTER TABLE asset ALTER COLUMN id SET NOT NULL;
ALTER TABLE asset_kind ALTER COLUMN code SET NOT NULL;
ALTER TABLE backend_pool ALTER COLUMN id SET NOT NULL;
ALTER TABLE container_engine ALTER COLUMN code SET NOT NULL;
ALTER TABLE data_class ALTER COLUMN code SET NOT NULL;
ALTER TABLE dependency ALTER COLUMN id SET NOT NULL;
ALTER TABLE endpoint ALTER COLUMN id SET NOT NULL;
ALTER TABLE environment ALTER COLUMN id SET NOT NULL;
ALTER TABLE environment_role ALTER COLUMN code SET NOT NULL;
ALTER TABLE health_override ALTER COLUMN id SET NOT NULL;
ALTER TABLE interface ALTER COLUMN id SET NOT NULL;
ALTER TABLE interface_form_factor ALTER COLUMN code SET NOT NULL;
ALTER TABLE ip_address ALTER COLUMN id SET NOT NULL;
ALTER TABLE ip_address_role ALTER COLUMN code SET NOT NULL;
ALTER TABLE link ALTER COLUMN id SET NOT NULL;
ALTER TABLE net_anchor ALTER COLUMN id SET NOT NULL;
ALTER TABLE net_attachment ALTER COLUMN id SET NOT NULL;
ALTER TABLE net_group ALTER COLUMN id SET NOT NULL;
ALTER TABLE net_uplink ALTER COLUMN id SET NOT NULL;
ALTER TABLE observed_transition ALTER COLUMN id SET NOT NULL;
ALTER TABLE prefix ALTER COLUMN id SET NOT NULL;
ALTER TABLE route ALTER COLUMN id SET NOT NULL;
ALTER TABLE rt_container ALTER COLUMN instance_id SET NOT NULL;
ALTER TABLE rt_k8s ALTER COLUMN instance_id SET NOT NULL;
ALTER TABLE rt_systemd ALTER COLUMN instance_id SET NOT NULL;
ALTER TABLE rt_windows ALTER COLUMN instance_id SET NOT NULL;
ALTER TABLE service ALTER COLUMN id SET NOT NULL;
ALTER TABLE service_kind ALTER COLUMN code SET NOT NULL;
ALTER TABLE unmatched_observation ALTER COLUMN id SET NOT NULL;

-- +goose Down
--
-- NOTHING, and the asymmetry with the SQLite half is the point.
--
-- On PostgreSQL a primary key has ALWAYS implied NOT NULL, so every statement in
-- the Up section above was a no-op here -- it asserted a constraint the engine
-- had already applied. There is nothing to undo, and PostgreSQL will not let
-- anyone try: ALTER COLUMN id DROP NOT NULL returns "column \"id\" is in a
-- primary key" (SQLSTATE 42P16).
--
-- The SQLite half does drop them, because there the previous state genuinely was
-- nullable. A down migration restores what was there before, and what was there
-- before differed by engine -- which is exactly the situation the dialect split
-- exists for.
SELECT 1;
