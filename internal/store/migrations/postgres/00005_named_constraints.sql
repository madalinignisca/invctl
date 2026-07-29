-- Every remaining behavioural constraint gets a name. PostgreSQL half; see
-- migrations/sqlite/00005 for which sixteen tables, why they stay CHECKs rather
-- than becoming lookup tables, and why SQLite needs a full rebuild to do this.
--
-- ALMOST NOTHING TO DO, AND THAT IS THE POINT. PostgreSQL names every
-- constraint whether you do or not, so all 58 remaining CHECKs and all 6
-- UNIQUEs already have exactly the names the SQLite rebuild is adopting:
-- `<table>_<column>_check` and `<table>_<column>_key`. Fifty-eight of the
-- sixty-four constraints therefore need no statement here at all -- the SQLite
-- half is converging on this engine's naming rather than inventing one, so
-- there is nothing to rename. Each of those names was read out of
-- `pg_constraint` and asserted against the generated SQLite DDL before that
-- file was written, not guessed.
--
-- THE SEVEN THAT DO. A table-level, multi-column CHECK is auto-named
-- `<table>_check`, and `<table>_check1`, `_check2` for any later ones on the
-- same table. That name is positional, not descriptive: it says nothing about
-- what the constraint means, and it shifts if another table-level CHECK is ever
-- added to the table -- at which point the two engines would silently stop
-- agreeing. So these seven are renamed on both engines to something that says
-- what they enforce. Renaming a CHECK in PostgreSQL is a drop and an add; there
-- is no ALTER CONSTRAINT RENAME for a CHECK.
--
-- The expressions below were extracted from the freshly named SQLite DDL by the
-- same generator that produced it, so the two engines cannot drift on what the
-- constraint actually says -- only on the name it is filed under, which is the
-- thing this migration exists to make identical. ADD CONSTRAINT ... CHECK
-- validates every existing row immediately, so a weakened or mistyped
-- expression that still parses would be caught by the store suite's shared
-- rejection tests rather than passing quietly here.
--
-- Order is irrelevant -- every statement is independent and the whole file is
-- one transaction -- so the pairs are listed in the order the SQLite half
-- rebuilds its tables, purely so the two files read side by side.

-- +goose Up

ALTER TABLE asset_health DROP CONSTRAINT asset_health_check;
ALTER TABLE asset_health
  ADD CONSTRAINT asset_health_flap_episode_check
  CHECK ((flap_since IS NULL AND flap_count IS NULL AND flap_first_state IS NULL
          AND flap_seen IS NULL)
      OR (flap_since IS NOT NULL AND flap_count IS NOT NULL AND flap_first_state IS NOT NULL
          AND flap_seen IS NOT NULL));

ALTER TABLE health_override DROP CONSTRAINT health_override_check;
ALTER TABLE health_override
  ADD CONSTRAINT health_override_expiry_check
  CHECK (expires_at > created_at);

ALTER TABLE observed_transition DROP CONSTRAINT observed_transition_check;
ALTER TABLE observed_transition
  ADD CONSTRAINT observed_transition_flap_close_detail_check
  CHECK (kind <> 'flap_close' OR (flap_count IS NOT NULL AND window_start IS NOT NULL
         AND window_end IS NOT NULL AND first_state IS NOT NULL
         AND last_state IS NOT NULL AND settled_state IS NOT NULL));

ALTER TABLE link DROP CONSTRAINT link_check;
ALTER TABLE link
  ADD CONSTRAINT link_distinct_interfaces_check
  CHECK (a_interface_id <> b_interface_id);

ALTER TABLE endpoint DROP CONSTRAINT endpoint_check;
ALTER TABLE endpoint
  ADD CONSTRAINT endpoint_port_or_unix_path_check
  CHECK ((l4_proto = 'unix' AND unix_path IS NOT NULL AND port IS NULL)
      OR (l4_proto <> 'unix' AND port IS NOT NULL AND unix_path IS NULL));

ALTER TABLE dependency DROP CONSTRAINT dependency_check;
ALTER TABLE dependency
  ADD CONSTRAINT dependency_provider_exclusive_check
  CHECK ((provider_endpoint_id IS NOT NULL AND provider_route_id IS NULL)
      OR (provider_endpoint_id IS NULL AND provider_route_id IS NOT NULL));

ALTER TABLE net_uplink DROP CONSTRAINT net_uplink_check;
ALTER TABLE net_uplink
  ADD CONSTRAINT net_uplink_distinct_groups_check
  CHECK (group_id <> upstream_group_id);


-- Two names that disagreed between engines, and therefore could not be widened
-- by one ALTER pair -- named but still frozen for a dual-engine migration.
--
-- service_instance_source_checked_check carries the scar of migration 00008's
-- add/copy/drop/rename dance: PostgreSQL named the constraint after the
-- temporary column (source_checked) and the name survived the rename. SQLite,
-- rebuilt by hand, called it source. Renaming here rather than in 00008 keeps
-- 00008 an accurate record of what it did.
ALTER TABLE service_instance
  RENAME CONSTRAINT service_instance_source_checked_check TO service_instance_source_check;

-- +goose Down

-- The autonames restored, so the down migration leaves the schema PostgreSQL
-- would have generated for itself rather than something that merely behaves the
-- same. `<table>_check` is free again by the time each ADD runs, because the
-- descriptive name is dropped first.

ALTER TABLE net_uplink DROP CONSTRAINT net_uplink_distinct_groups_check;
ALTER TABLE net_uplink
  ADD CONSTRAINT net_uplink_check
  CHECK (group_id <> upstream_group_id);

ALTER TABLE dependency DROP CONSTRAINT dependency_provider_exclusive_check;
ALTER TABLE dependency
  ADD CONSTRAINT dependency_check
  CHECK ((provider_endpoint_id IS NOT NULL AND provider_route_id IS NULL)
      OR (provider_endpoint_id IS NULL AND provider_route_id IS NOT NULL));

ALTER TABLE endpoint DROP CONSTRAINT endpoint_port_or_unix_path_check;
ALTER TABLE endpoint
  ADD CONSTRAINT endpoint_check
  CHECK ((l4_proto = 'unix' AND unix_path IS NOT NULL AND port IS NULL)
      OR (l4_proto <> 'unix' AND port IS NOT NULL AND unix_path IS NULL));

ALTER TABLE link DROP CONSTRAINT link_distinct_interfaces_check;
ALTER TABLE link
  ADD CONSTRAINT link_check
  CHECK (a_interface_id <> b_interface_id);

ALTER TABLE observed_transition DROP CONSTRAINT observed_transition_flap_close_detail_check;
ALTER TABLE observed_transition
  ADD CONSTRAINT observed_transition_check
  CHECK (kind <> 'flap_close' OR (flap_count IS NOT NULL AND window_start IS NOT NULL
         AND window_end IS NOT NULL AND first_state IS NOT NULL
         AND last_state IS NOT NULL AND settled_state IS NOT NULL));

ALTER TABLE health_override DROP CONSTRAINT health_override_expiry_check;
ALTER TABLE health_override
  ADD CONSTRAINT health_override_check
  CHECK (expires_at > created_at);

ALTER TABLE asset_health DROP CONSTRAINT asset_health_flap_episode_check;
ALTER TABLE asset_health
  ADD CONSTRAINT asset_health_check
  CHECK ((flap_since IS NULL AND flap_count IS NULL AND flap_first_state IS NULL
          AND flap_seen IS NULL)
      OR (flap_since IS NOT NULL AND flap_count IS NOT NULL AND flap_first_state IS NOT NULL
          AND flap_seen IS NOT NULL));

ALTER TABLE service_instance
  RENAME CONSTRAINT service_instance_source_check TO service_instance_source_checked_check;
