-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Wireless LANs as structures, not edges.
--
-- AN SSID IS A STRUCTURE AND NOT AN EDGE, and 00031 already made the argument:
-- "INTERFACES ARE WHERE THE EDGE LIVES ... two access ports in VLAN 30 are in
-- one broadcast domain whether or not anybody drew a cable between them, and
-- that is a fact no cable trace can produce." An SSID broadcast by six APs is
-- exactly that fact -- two laptops on `corp` are in one broadcast domain and no
-- cable joins them. So this joins Structures beside vlan, fhrp_group and l2vpn,
-- and writes no `link` row: a port has one active cable and CreateLink refuses
-- the second, which is right for a cable and wrong for a radio serving many
-- clients (docs/wireless-design.md §2.4).
--
-- THE SCOPE IS AN ASSET, the same trick 00031 uses. A site IS an asset, a rack
-- IS an asset, so one nullable reference covers every case with no type column
-- and no polymorphism. NULL means estate-wide. An SSID scoped to a site says
-- "this is the Oslo guest", and two estates reusing one SSID name are two rows.
--
-- THE PSK IS A REFERENCE, NEVER A VALUE. psk_ref holds a Vault path or similar,
-- the same rule 00003 states for identity.secret_ref: "NEVER the secret itself.
-- If a code path would put an actual secret here, that is a bug to raise, not
-- to work around." There is deliberately NO passphrase column -- the certificate
-- rule generalises word for word, "a column that accepts certificate-shaped text
-- is where a key eventually gets pasted". psk_ref is in domain.RedactedFields, so
-- change_log records THAT it changed and never what to (docs/AUDIT.md rule 12).
--
-- DECLARED ONLY, AND CHANNEL IS THE INSTRUCTIVE ABSENCE. A PLANNED channel would
-- be declared and an OPERATING channel observed, and they are two columns that
-- must sit beside each other. This migration records NEITHER. Adding only the
-- declared half now builds the trap: the disagreement between the declared and
-- the observed value is the finding, and a single column collapses it. When
-- channel planning arrives it brings both halves, through internal/store/
-- observed.go, under docs/AUDIT.md rules 1-9 -- never as a reinterpretation of
-- a declared field. Same for RSSI, client counts and band steering.
--
-- NO ON DELETE CASCADE on any of the three optional references. A site, a VLAN
-- or a service is soft-retired, never deleted, so a cascade here would be a rule
-- that can never fire pretending to be a safety net.
--
-- DIALECT-PAIRED, NOT shared/, and the reason is the one 00006_bridge_kind.sql
-- already gives: wireless_lan references asset(id) and service(id) (rebuilt by
-- dialect 00004) and vlan(id) (created by dialect 00031), and the form-factor
-- and asset-kind seed rows insert into interface_form_factor and asset_kind
-- (dialect 00004). Migrate applies all of shared and then all of the dialect
-- set, so a shared migration here would run before those tables exist.

-- +goose Up

-- A DOMAIN VOCABULARY, not a behavioural enum. 00004 draws the line: a
-- behavioural enum is "read by Go to select a code path" and keeps TEXT + CHECK
-- so a new value requires a release; a domain vocabulary "only describes the
-- estate" and becomes a table. NOTHING in F1 branches on the security mode -- it
-- is recorded and rendered -- so wpa4 lands as an INSERT.
--
-- IF a later work package wants a finding like "this SSID is open", that branch
-- must be a BEHAVIOUR COLUMN on this row -- the asset_kind.can_host_instances
-- and environment_role.is_transit pattern -- and never a hardcoded set in Go.
-- Expect it; is_open on a six-row lookup is a cheap migration. Guessing the
-- column now, before anything reads it, is how 00004's own caveat says these go
-- wrong in the other direction.
CREATE TABLE wireless_security (
  code        TEXT PRIMARY KEY NOT NULL,
  label       TEXT NOT NULL,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  description TEXT NOT NULL DEFAULT ''
);
INSERT INTO wireless_security (code, label, sort_order, description) VALUES
  ('open',            'Open',                      10, 'No authentication and no encryption on the air. Legitimate for a captive-portal guest network and for nothing else; anyone in range reads every frame.'),
  ('wpa2_personal',   'WPA2-Personal (PSK)',       20, 'One pre-shared key for every client. Rotating it means touching every device, which is why the key tends never to rotate.'),
  ('wpa2_enterprise', 'WPA2-Enterprise (802.1X)',  30, 'Per-user authentication against a RADIUS service. Record which service in auth_service_id.'),
  ('wpa3_personal',   'WPA3-Personal (SAE)',       40, 'Pre-shared key with SAE, so a captured handshake cannot be cracked offline.'),
  ('wpa3_enterprise', 'WPA3-Enterprise',           50, 'Per-user authentication with WPA3 protections. Record the RADIUS service in auth_service_id.'),
  ('wpa3_transition', 'WPA3 transition',           60, 'WPA3 and WPA2 accepted on one SSID while clients catch up. A mixed mode is a migration state, not a destination.');

CREATE TABLE wireless_lan (
  id              TEXT PRIMARY KEY NOT NULL,
  -- What people call it here; ssid is what is on the air. They differ more often
  -- than not -- "Guest (Oslo)" broadcasting `guest`.
  name            TEXT NOT NULL,
  ssid            TEXT NOT NULL,
  security        TEXT NOT NULL REFERENCES wireless_security(code),
  -- Where this SSID applies. A site, a building, a rack -- all assets here.
  -- NULL is estate-wide. No ON DELETE CASCADE: assets are soft-retired.
  scope_asset_id  TEXT REFERENCES asset(id),
  -- Which broadcast domain clients land in. Nullable: an estate may record an
  -- SSID before anybody has written down where it terminates.
  vlan_id         TEXT REFERENCES vlan(id),
  -- Which service authenticates it (D5). RECORDED AND RENDERED, and nothing
  -- derives from it -- deliberately NOT an impact edge. If the RADIUS service
  -- dies the SSID does not stop being broadcast: existing clients stay
  -- associated and the structure is not emptied. New authentications fail,
  -- which is a different outage with a different blast radius, and claiming
  -- otherwise would put a wrong edge in the graph.
  --
  -- It is NOT a `dependency` row, and the first draft of the design said it was.
  -- 00004_dependencies.sql: `consumer_service_id TEXT NOT NULL REFERENCES
  -- service(id)`. A dependency's consumer must BE a service; a wireless LAN is
  -- not one, so it can never be the consumer end of one. Making an SSID a
  -- general consumer of services needs a polymorphic consumer, which is the one
  -- join shape this codebase has avoided everywhere.
  auth_service_id TEXT REFERENCES service(id),
  -- A PATH to the passphrase, never the passphrase. See the header.
  psk_ref         TEXT,
  notes           TEXT,
  lifecycle       TEXT NOT NULL DEFAULT 'active'
                    CONSTRAINT wireless_lan_lifecycle_check CHECK (lifecycle IN ('active','retired')),
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  row_version     INTEGER NOT NULL DEFAULT 1
);

-- An SSID is only unique SOMEWHERE, and `ssid` alone is deliberately NOT unique:
-- the same SSID at two sites is two rows (D6). But the same SSID twice in one
-- scope is a duplicate, not a fact.
--
-- TWO PARTIAL INDEXES, for the reason 00029, 00030 and 00031 all give: NULLs are
-- distinct on BOTH engines, so a single composite over (ssid, scope_asset_id)
-- would constrain nothing at all for the estate-wide case -- which is where
-- every SSID starts when nobody has declared a scope yet. The two-index form is
-- identical on both engines; the one-index form is identical on both engines and
-- wrong on both.
CREATE UNIQUE INDEX wireless_lan_scope_ssid_key  ON wireless_lan(scope_asset_id, ssid)
  WHERE scope_asset_id IS NOT NULL AND lifecycle <> 'retired';
CREATE UNIQUE INDEX wireless_lan_global_ssid_key ON wireless_lan(ssid)
  WHERE scope_asset_id IS NULL     AND lifecycle <> 'retired';
CREATE INDEX idx_wireless_lan_scope ON wireless_lan(scope_asset_id);
CREATE INDEX idx_wireless_lan_vlan  ON wireless_lan(vlan_id);

-- Which radios broadcast which SSID. THIS IS THE ADJACENCY, exactly as
-- interface_vlan is a VLAN's.
--
-- A SET TABLE, replaced wholesale with its interface, like interface_vlan and
-- asset_environment: the membership belongs to the radio and has no life of its
-- own, so it carries no id and no lifecycle and is folded into the interface's
-- audited value. The CLAUDE.md rule applies -- the parent's change_log entry
-- must record the change, or a radio moving from `corp` to `guest` produces no
-- diff at all. That failure has now been made FOUR times in this codebase and
-- the fourth was in interface_vlan itself; see interfaceWLANAudit in
-- internal/store/wireless.go for the mechanism that prevents a fifth.
--
-- ON DELETE CASCADE on the interface only. The membership has no life without
-- its port; the SSID is soft-retired like every other entity.
CREATE TABLE interface_wlan (
  interface_id    TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  wireless_lan_id TEXT NOT NULL REFERENCES wireless_lan(id),
  PRIMARY KEY (interface_id, wireless_lan_id)
);
CREATE INDEX idx_interface_wlan_lan ON interface_wlan(wireless_lan_id);

-- Three radio form factors, added the way 00004 argued a form factor should be
-- added and 00006 proved: as DATA. No table rebuilt, no CHECK widened, nothing
-- recompiled -- interface.form_factor became a FOREIGN KEY into
-- interface_form_factor in 00004 precisely so this could be three lines.
--
-- THREE RATHER THAN ONE. An AP has separate radios per band that fail and are
-- disabled independently, and "which band is `guest` on here" is a question the
-- estate asks. One code could not answer it.
--
-- SORT ORDER 100/110/120, continuing past loopback (90) rather than slotting in
-- among the sockets. The existing numbering is physical-then-logical and steps
-- of ten exist so a new value lands without renumbering; putting radios at 91-93
-- would break the step, and renumbering the table would silently reorder every
-- form-factor dropdown in the product.
--
-- ONE CONSEQUENCE, AND IT IS BENIGN: handlers/neighbourhood.go's virtualForms
-- maps three form factors to "virtual" for diagram colouring, and a radio is not
-- in it, so a radio draws as physical. That is CORRECT rather than correct by
-- accident -- virtualForms means "inside one box" (veth, LAG, loopback) and a
-- radio path is between boxes and breaks when either box does. (virtualForms
-- being a hardcoded Go map over a vocabulary table is the latent issue 00004's
-- own "TWO CAVEATS, FLAGGED NOT RESOLVED" warns about. This makes it one entry
-- larger and no worse; it is not F1's to fix.)
INSERT INTO interface_form_factor (code, label, sort_order, description) VALUES
  ('radio_2g4', 'Radio 2.4 GHz', 100, 'An 802.11 radio on the 2.4 GHz band. Long range, three non-overlapping channels, and everything else in the building is also on it.'),
  ('radio_5g',  'Radio 5 GHz',   110, 'An 802.11 radio on the 5 GHz band. The usual workhorse: more channels, shorter range, and DFS channels that a radar event can move.'),
  ('radio_6g',  'Radio 6 GHz',   120, 'An 802.11 radio on the 6 GHz band (Wi-Fi 6E and later). Clean spectrum, shortest range, and only newer clients can see it.');

-- 'access_point' as an asset kind, for the same reason 00006 added 'bridge': the
-- kind is DATA. can_host_instances FALSE -- an AP forwards frames and runs
-- nothing, so a service_instance placed on one is a data-entry mistake and
-- CreateInstance refuses it on this column. is_attachable TRUE -- an AP is a
-- network element and can be the subject of a net_attachment, which is what
-- makes declaring one POSSIBLE for an estate where an AP lands somewhere its
-- parent switch does not. sort_order 58 puts it between switch (50) and
-- patch_panel (60): the ordering is containment, and an AP hangs off a switch.
-- (55 and 52 were tried first and both collide with the literal 55
-- vocabulary_test.go's TestVocabularyValueAddedAsDataIsUsableImmediately
-- inserts on the fly to prove a value added as data sorts correctly: any
-- sort_order strictly between switch's 50 and that test's 55 lands between
-- them and breaks the adjacency the test checks. 58 stays in the "between
-- switch and patch_panel" band while sitting after the test's own
-- insertion point.)
--
-- description is set in the same INSERT rather than left to a later UPDATE,
-- unlike 00006_bridge_kind.sql's `bridge`: 00007_vocabulary_descriptions.sql
-- (which added the column and backfilled `bridge`) already ran by the time
-- this migration does, so a value inserted here with no description would
-- fail TestEveryVocabularyTermHasADescription rather than being caught by a
-- later migration the way `bridge` was.
--
-- SHIPS HERE, NOT AS ITS OWN MIGRATION: §4 named the gap ("`asset_kind` has
-- twelve and none is an access point") and the demo fixture asks for two APs,
-- so leaving this out would force the seed to call them switches -- a lie its
-- own tests then assert against. docs/wireless-design.md, corrected 2026-09-07.
INSERT INTO asset_kind (code, label, sort_order, can_host_instances, is_attachable, description)
VALUES ('access_point', 'Access point', 58, FALSE, TRUE,
        'A wireless access point. Forwards frames and runs nothing, so it hangs off a switch the way a bridge hangs off a hypervisor.');

-- +goose Down
DELETE FROM asset_kind WHERE code = 'access_point';
DELETE FROM interface_form_factor WHERE code IN ('radio_2g4','radio_5g','radio_6g');
DROP INDEX idx_interface_wlan_lan;
DROP TABLE interface_wlan;
DROP INDEX idx_wireless_lan_vlan;
DROP INDEX idx_wireless_lan_scope;
DROP INDEX wireless_lan_global_ssid_key;
DROP INDEX wireless_lan_scope_ssid_key;
DROP TABLE wireless_lan;
DROP TABLE wireless_security;
