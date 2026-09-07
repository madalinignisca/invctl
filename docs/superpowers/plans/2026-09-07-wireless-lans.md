# WP-F1 Wireless LANs — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An estate runs SSIDs. Record them, record which radios broadcast them, and make the impact engine answer *this AP is down — what is no longer being broadcast, and where?*

**Spec:** `docs/wireless-design.md` on branch `wp-f1-wireless` (commit `d1f1c69`, DRAFT 2026-09-07, challenged and amended twice). **D5 was CORRECTED and §2.6's channel paragraph rewritten** — the current file is the authority. D1–D7 are decided and nothing here re-opens them. The three places this plan cannot decide are collected under "Decisions this plan cannot take"; one of them blocks a task.

**Architecture:** A wireless LAN is a `Structure`, not an edge (D1). That is not a new mechanism — `internal/store/graph.go`'s `loadStructures` already builds three of them from parent-plus-member joins and `internal/impact/structures.go`'s `analyseStructures` already reports the two states worth reporting. F1 adds a fourth join, a kind constant and two detail sentences; the analysis is free. A radio is an ordinary `interface` row with a new form-factor code (D2), so it inherits MAC, `enabled`, `is_mgmt`, `authorizeInterfaceSubject` and every existing interface test. `interface_wlan` is a set table replaced wholesale and folded into the interface's audited value (D7) — the highest-risk item in the whole work package, and the one this codebase has got wrong four times.

**Tech stack:** Go (`go.mod` toolchain), `jmoiron/sqlx` hand-written SQL, `pressly/goose/v3` plain `.sql`, `html/template` + HTMX. **No new dependency.** One paired migration.

---

## Migration number — checked on both dialects and in `shared/`

| Set | Highest existing | New file |
|---|---|---|
| `internal/store/migrations/shared/` | `00010_change_log_batch_id.sql` | — not used, see below |
| `internal/store/migrations/sqlite/` | `00060_saved_view.sql` | **`00061_wireless.sql`** |
| `internal/store/migrations/postgres/` | `00060_saved_view.sql` | **`00061_wireless.sql`** |

**It must be dialect-paired, not `shared/`, and the reason is recorded rather than a preference.** `internal/store/migrate.go:38` applies *all* of `shared` and *then* all of the dialect set, and `00006_bridge_kind.sql:42-45` states the consequence in its own words: `asset_kind` is `CREATE`d in the dialect sets (00004 in each), "so a shared migration referencing it would run before the table exists". The same applies four times over here — `wireless_lan` references `asset(id)` and `service(id)` (rebuilt by dialect 00004) and `vlan(id)` (created by dialect 00031), and the form-factor seed rows insert into `interface_form_factor` (dialect 00004). The two files are **byte-identical**; that is expected and correct, exactly as `00006_bridge_kind.sql` is.

---

## Global Constraints

These are the ones this feature can get wrong quietly.

- **The audit fold is the highest-risk item in this work package.** `SetInterfaceWLANs` must fold membership into the *interface's* audited value using **`db`-tagged** fields. `internal/store/diff.go:76-86` compares every `db`-tagged field and ignores the rest; writing to `interface_wlan` changes no column of the `interface` row, so an audit that diffs a plain `domain.Interface` compares a row that did not change and records **silence**. `internal/store/vlans.go:349-366` names the trap: *"an audit struct whose membership fields carried only json tags diffed nothing but the interface id — which never changes."* `SetInterfaceVLANs` itself got this wrong first (`vlans.go:265-272`), so "follow the VLAN code" is not sufficient instruction. Task 5's test is the check, and Task 5 step 6 mutates the tag and watches it go red.
- **The audited value is SSID NAMES, sorted and joined — never row ids.** `assetAudit`'s reason, quoted in `vlans.go:359-361`: *"an audit entry is read by people, and 'untagged 30, tagged 40,99' is a sentence where three UUIDs are a lookup exercise."*
- **Embed `domain.Interface` BY VALUE, never by pointer.** `diff.go:56-68` panics on an anonymous pointer embed, and the comment explains why the panic exists: a pointer embed silently matched neither branch and "every column of the embedded struct vanished from change_log while an entry was still written. The bug was invisible for a week."
- **`psk_ref` holds a path, never a secret.** No passphrase column, and there will not be one (§2.5, D4). It joins `domain.RedactedFields` beside `secret_ref`, `key_ref` and `contact_ref`. The audit records **that** it changed and never what to — `docs/AUDIT.md` rule 12. Task 4's test asserts both directions, modelled on `TestSnapshotRedactsSecretRef` (`internal/store/boundary_test.go:728-810`).
- **Every new column is DECLARED.** No observed wireless column, no channel column of either kind, no RSSI, no client count, no band steering (§2.6). Channel is the instructive case and F1 has **neither** half of it: adding only the declared half now builds the exact trap §2.6 exists to describe. Nothing in this work package touches `internal/store/observed.go`.
- **No `link` rows (§2.4), no `net_uplink` row, no change to `impact.Request`, no point-to-point bridge as a cut target (§5).** `auth_service_id` is recorded and rendered and **nothing derives from it** (D5): if the RADIUS service dies the SSID does not stop being broadcast, and claiming otherwise puts a wrong edge in the graph.
- **Every query runs unmodified on SQLite AND PostgreSQL.** `?` placeholders only, through `s.read`/`t.exec`, which rebind. Never `$1`. No `inet`, no native arrays, no `ENUM`, no `SERIAL`, no `NOW()` default, no JSON operator in a `WHERE`. Ids are UUIDv7 `TEXT` from Go; timestamps are RFC3339 UTC `TEXT` from Go.
- **`UNIQUE (ssid, scope_asset_id)` cannot be one index — see the flagged note below.** It is two partial indexes, which *is* identical on both engines. This is not a new decision; `00031_vlans.sql:70-76` made it twice already for the same reason.
- **Soft delete only.** `lifecycle = 'retired'`. The one `DELETE FROM` this work package adds is the `interface_wlan` set replacement inside the interface's transaction, and it must be added to `prune_test.go`'s per-table allowlist (Task 5 step 5) or `TestTheOnlyFactDeletingStatementIsThePrune` fails.
- **No new permit minter.** `SetInterfaceWLANs` reuses `authorizeInterfaceSubject` (`internal/store/network.go:173`), which is already in `storePermitMinters`. `permitMinterBudget` does **not** change, so this work package does not trip the auth-review-and-sign-off requirement `permit_source_test.go:105-115` attaches to a new minter. If an implementation finds itself wanting a `authorizeWirelessSubject`, **stop** — that is a conversation, not a helper.
- **Three edits per new column, not two:** the migration, `docs/AUDIT.md`'s normative table, and `internal/domain/classification.go`'s census. `TestEveryColumnIsClassified` reads the live schema on both engines and fails in both directions.
- **Licence header on every new file** (`.go`, `.sql`, `.html`), AGPL-3.0-**only**. In Go, a **blank line** between the notice and the `package` clause or the licence becomes the package doc; `internal/license` fails otherwise.
- **invctl never acts on the estate.** This records and presents declared wireless configuration. Nothing here polls, pushes, or reconciles.
- **Gates:** `make lint` and `make test`, foreground, one at a time, exit status read directly. Never pipe either through `tail`/`head`/`grep`. `go test ./...` on its own is **not** the gate — with `INV_TEST_POSTGRES_DSN` unset the Postgres half is silently skipped.

---

## Decisions this plan cannot take

### A. `asset_kind = 'access_point'` — **BLOCKS Task 2 and Task 8**

§1 names the gap explicitly: *"`asset_kind` has twelve and none is an access point."* §4's build list then covers only the three **form-factor** rows (item 2) and never mentions the asset kind. Item 8 asks the seed for "two APs carrying `corp` and `guest`" — which, without a kind, has to call them `switch`, putting a lie in the demo fixture the fixture tests then assert against.

The shape is settled and cheap — `00006_bridge_kind.sql` is the template, four lines, `can_host_instances FALSE` (an AP forwards frames; it runs nothing) and `is_attachable TRUE` (an AP is a network element that can be the subject of a `net_attachment`). What this plan cannot do is add a row to a **declared** vocabulary that §4 did not list. **Take a yes/no before Task 2.** The plan below writes it as Task 2 step 3, bracketed, and Task 8 assumes it.

If the answer is no, Task 8 must seed the APs as `switch` and say so in a comment, and `docs/wireless-design.md` §1's paragraph should be struck through rather than left describing a gap nobody chose to leave.

### B. §4 item 2's "data, no migration" is mechanically impossible — non-blocking, but the phrasing must not be implemented literally

D2 says the three radio form factors are "seeded vocabulary rows, not a migration". Every deployment that already exists has run `00004_vocabulary_lookups.sql`; goose will never run it again, so an `INSERT` added to 00004 reaches nobody. `internal/seed` is also not an option, and `00006_bridge_kind.sql:16-24` gives the reason in full: the vocabulary tables are classified **declared**, "so a writer that is not a migration owes it a `change_log` row. Migrations are the one exception … Seeding a vocabulary row from `internal/seed` would either break that rule or silently exempt a declared table, and a demo fixture is not a reason to do either."

So the rows ship as `INSERT`s **inside migration 00061**. That is what "data, not a migration" was reaching for — no table rebuilt, no `CHECK` widened, nothing recompiled — and it is the identical move `00006_bridge_kind` already made. **No architecture decision here; recording it so nobody re-derives it.**

### C. The partial unique index — **FLAGGED AS THE SPEC ASKED, and it resolves**

§4 item 1 asks for `UNIQUE (ssid, scope_asset_id) WHERE lifecycle <> 'retired'` and adds: *"the engines differ on NULL in a unique index; if the partial index cannot express this identically on both, say so rather than shipping two behaviours."*

**Written as one index it cannot express it — but not because the engines differ.** They agree: NULLs are distinct in a unique index on *both* SQLite and PostgreSQL. A single composite over `(ssid, scope_asset_id)` therefore constrains **nothing at all** for the estate-wide case (`scope_asset_id IS NULL`), which is where every SSID starts before anybody declares a scope. `00031_vlans.sql:70-76` hit this exactly and says so verbatim: *"NULLs are distinct on both engines, so a single composite over (group_id, vid) would constrain nothing at all for the estate-wide pool — which is where every VLAN starts when nobody has declared a group yet."*

**The two-partial-index form is identical on both engines and is what this plan writes:**

```sql
CREATE UNIQUE INDEX wireless_lan_scope_ssid_key  ON wireless_lan(scope_asset_id, ssid)
  WHERE scope_asset_id IS NOT NULL AND lifecycle <> 'retired';
CREATE UNIQUE INDEX wireless_lan_global_ssid_key ON wireless_lan(ssid)
  WHERE scope_asset_id IS NULL     AND lifecycle <> 'retired';
```

One behaviour, both engines, and it is the third time this repository has used it (00029, 00030, 00031). Task 5's `TestAnSSIDIsUniqueWithinItsScopeAndTheUnscopedPoolIsOne` proves both halves, and the second half is the one that would silently pass under the single-index form.

---

## Files touched

**New (8):**

| Path | What |
|---|---|
| `internal/store/migrations/sqlite/00061_wireless.sql` | schema + vocabulary + form-factor seeds |
| `internal/store/migrations/postgres/00061_wireless.sql` | byte-identical twin |
| `internal/domain/wireless.go` | `WirelessLAN`, `NewWirelessLAN`, `InterfaceWLAN` |
| `internal/store/wireless.go` | CRUD, retire, `SetInterfaceWLANs`, `interfaceWLANAudit` |
| `internal/store/wireless_test.go` | the six §4 item 9 tests, dual-engine |
| `internal/web/handlers/wireless.go` | list, detail, create, retire, radio add/remove |
| `web/templates/pages/wireless_list.html` | list + create form |
| `web/templates/pages/wireless_detail.html` | one SSID, its radios, the picker |

**Modified (14):**

| Path | Change |
|---|---|
| `internal/domain/network.go:13-35` | three `FFRadio*` constants, appended to `FormFactors` |
| `internal/domain/errors.go:273-298` | `psk_ref` into `RedactedFields` |
| `internal/domain/classification.go:~446` | three tables into `DeclaredColumns` |
| `internal/domain/role.go:~618` | `"wireless_lan": ScopeTopology`, `"wireless_security": ScopeEstateConfig` |
| `internal/store/vocabulary.go:40-97` | `vocabWirelessSecurity` const, query, `WirelessSecurities()` reader |
| `internal/store/graph.go:571-664` | the fourth join in `loadStructures` |
| `internal/impact/structures.go:39-172` | `StructureWLAN`, two detail sentences, one href |
| `internal/impact/structures_test.go:17-32` | a WLAN in `structureFixture` |
| `internal/store/prune_test.go:~680` | `internal/store/wireless.go` → `interface_wlan` in the allowlist |
| `internal/web/routes.go:~206,~513` | two read routes, four write routes |
| `internal/web/handlers/nav.go:~99` | "Wireless" under Network |
| `internal/web/handlers/help.go:59-85` | `wireless_security` in `vocabTopics` |
| `internal/web/detail_pages_render_test.go:59` | `/wireless/` in the census |
| `internal/seed/seed.go:54-72`, `internal/seed/seed_engine.go:43-50` | `Refs.WirelessLANs`, a `wirelessLANs()` phase |
| `docs/AUDIT.md` (two tables) | column classification + `ScopeTopology` entry |
| `web/templates/partials/rows.html:~104` | `wireless_lan` search-result link |
| `internal/web/handlers/assets.go` + `web/templates/pages/asset_detail.html` | radios panel (Task 7b) |
| `internal/seed/seed_engine_test.go` | two fixture-honesty tests (Task 8) |

---

## Task 1 — Migration 00061, both dialects

**Interfaces:** none (SQL only).

- [ ] **Step 1.** Create `internal/store/migrations/sqlite/00061_wireless.sql` with the AGPL header, then this header comment (the reasoning is the file's job, not this plan's — copy it in):

```sql
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
```

- [ ] **Step 2.** `-- +goose Up`, then the vocabulary table. `wireless_security` is a **domain vocabulary, not a behavioural enum** (D3): nothing in F1 branches on it, so `wpa4` arrives as an `INSERT` and no release is needed. Descriptions are mandatory — `TestEveryVocabularyTermHasADescription` (`internal/store/vocabulary_test.go:499`) fails on an empty one.

```sql
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
```

- [ ] **Step 3.** The entity table and its indexes. `id`/`created_at`/`updated_at` come from Go and never the database.

```sql
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
```

- [ ] **Step 4.** The set table. Cascade on the interface **only**, matching `interface_vlan`.

```sql
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
```

- [ ] **Step 5.** The three radio form factors (D2, and see "Decisions this plan cannot take" §B for why they are `INSERT`s here and not in `internal/seed`).

```sql
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
```

- [ ] **Step 6.** The down migration, indexes and children before parents:

```sql
-- +goose Down
DELETE FROM interface_form_factor WHERE code IN ('radio_2g4','radio_5g','radio_6g');
DROP INDEX idx_interface_wlan_lan;
DROP TABLE interface_wlan;
DROP INDEX idx_wireless_lan_vlan;
DROP INDEX idx_wireless_lan_scope;
DROP INDEX wireless_lan_global_ssid_key;
DROP INDEX wireless_lan_scope_ssid_key;
DROP TABLE wireless_lan;
DROP TABLE wireless_security;
```

The `DELETE FROM interface_form_factor` is safe for the same reason `00006_bridge_kind.sql:52-56` gives: `interface.form_factor` references it, so if any radio still exists the foreign key refuses the `DELETE` rather than orphaning the row. **That is the correct failure.** (This `DELETE` is in a migration, not in store code, so `TestTheOnlyFactDeletingStatementIsThePrune` does not see it — it walks `.go` string literals and `.sql` files for `DELETE FROM change_log` only. Confirm on the first `make test`.)

- [ ] **Step 7.** Copy the file byte-for-byte to `internal/store/migrations/postgres/00061_wireless.sql`. Verify with `diff` — they must be identical. Nothing in it is dialect-specific: partial unique indexes with `<>` predicates, `TEXT`, `INTEGER` and named `CHECK` constraints all behave the same on both.
- [ ] **Step 8.** `make test` — the suite migrates both engines on every run, so a broken migration fails immediately and loudly. Expect `TestEveryColumnIsClassified` to go **red** here: three tables exist in the live schema with no classification. That is the next task and the failure is the point.

---

## Task 2 — Classification, redaction and scope (the three-edit obligation)

**Interfaces:** data only, no new functions.

- [ ] **Step 1.** `internal/domain/classification.go` — add to `DeclaredColumns`, next to the `vlan`/`interface_vlan` block (~line 446) so the two models read together:

```go
	// Wireless, migration 00061. Declared throughout: an SSID exists because
	// somebody configured it, a security mode because somebody chose it, and a
	// radio broadcasts an SSID because somebody put it there. An AP can REPORT
	// its operating channel, its transmit power, RSSI and its associated-client
	// count -- all observed state about the radio, all a different fact from
	// anything declared here, and the disagreement between a planned channel and
	// an operating one is the finding. F1 records NEITHER half of channel on
	// purpose (docs/wireless-design.md §2.6): a lone declared column is the trap,
	// because the pair is what makes the disagreement sayable.
	//
	// psk_ref is declared and is also in domain.RedactedFields -- classification
	// decides the AUDIT obligation, redaction decides what the entry may contain.
	// The two are independent and both apply.
	"wireless_lan": {
		"id", "name", "ssid", "security", "scope_asset_id", "vlan_id",
		"auth_service_id", "psk_ref", "notes", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// A vocabulary, like the seven 00004 created and for the same reason: a
	// lookup row is somebody asserting that a kind of thing exists in this
	// estate. Nothing observes it.
	"wireless_security": {"code", "label", "sort_order", "description"},
	// A set table, replaced wholesale with its interface and audited on it.
	"interface_wlan": {"interface_id", "wireless_lan_id"},
```

- [ ] **Step 2.** `docs/AUDIT.md` — one row in the normative column table, beside the `l2vpn` row (~line 59), and one row in the **ScopeTopology** table (~line 190). Use the `fhrp_group`/`l2vpn` rows as the model for length and tone; the row must state the declared/observed pair for channel explicitly, because that is the part a future reader is most likely to get wrong.
- [ ] **Step 3.** *(Blocked on Decision A.)* If `access_point` is approved, add to migration 00061 Step 5:

```sql
-- 'access_point' as an asset kind, for the same reason 00006 added 'bridge': the
-- kind is DATA. can_host_instances FALSE -- an AP forwards frames and runs
-- nothing, so a service_instance placed on one is a data-entry mistake and
-- CreateInstance refuses it on this column. is_attachable TRUE -- an AP is a
-- network element and can be the subject of a net_attachment, which is what
-- makes declaring one POSSIBLE for an estate where an AP lands somewhere its
-- parent switch does not. sort_order 55 puts it between switch (50) and
-- patch_panel (60): the ordering is containment, and an AP hangs off a switch.
INSERT INTO asset_kind (code, label, sort_order, can_host_instances, is_attachable)
VALUES ('access_point', 'Access point', 55, FALSE, TRUE);
```

Add `KindAccessPoint = "access_point"` to `internal/domain/asset.go`'s constant set and to `AssetKinds` if one exists there — `TestGoConstantsExistInEveryVocabulary` (`internal/store/vocabulary_test.go:124`) checks the Go constants against the seeded table, so the two must move together. Corresponding `DELETE` in the down migration.

- [ ] **Step 4.** `internal/domain/errors.go` — `psk_ref` into `RedactedFields` (~line 297), with its reason:

```go
	// A wireless network's pre-shared key REFERENCE. Same rule as secret_ref
	// and key_ref, and the same reason: it is a path to something secret, and a
	// complete, permanent, widely-readable map of where every credential lives
	// is a reconnaissance gift. There is deliberately no passphrase column --
	// docs/wireless-design.md §2.5 -- so this is the only wireless field that
	// could ever carry a lead, and change_log records THAT it changed and never
	// what to.
	"psk_ref": true,
```

- [ ] **Step 5.** `internal/domain/role.go` `entityScope` — two entries:

```go
	"wireless_lan":      ScopeTopology,
	"wireless_security": ScopeEstateConfig,
```

`ScopeTopology` for the SSID, matching `vlan`, `fhrp_group` and `l2vpn`. `ScopeEstateConfig` for the vocabulary, matching the ten already there. **`interface_wlan` gets no entry and must not** — it is never its own `change_log` entity_type; membership audits under `"interface"`, exactly as `interface_vlan` does, and `permit_source_test.go:593-598` states that asymmetry is deliberate and not this test's job.

- [ ] **Step 6.** `internal/domain/network.go` — three constants and the slice:

```go
	FFRadio2G4 = "radio_2g4"
	FFRadio5G  = "radio_5g"
	FFRadio6G  = "radio_6g"
```

appended to `FormFactors`. These are names for the seed and the tests, not a permitted set — the table is the authority (`network.go:25-31`).

- [ ] **Step 7.** `make test`. `TestEveryColumnIsClassified` goes green on both engines. `TestTheScopeClassificationCoversEveryAuditedEntityType` stays green (nothing logs `wireless_lan` yet — it goes red only if the classification and the audited list disagree, which they will not until Task 3 lands).

---

## Task 3 — `domain.WirelessLAN`

**Interfaces:**

```go
// internal/domain/wireless.go
type WirelessLAN struct {
	ID            string  `db:"id"`
	Name          string  `db:"name"`
	SSID          string  `db:"ssid"`
	Security      string  `db:"security"`
	ScopeAssetID  *string `db:"scope_asset_id"`
	VLANID        *string `db:"vlan_id"`
	AuthServiceID *string `db:"auth_service_id"`
	PSKRef        *string `db:"psk_ref"`
	Notes         *string `db:"notes"`
	Lifecycle     string  `db:"lifecycle"`
	CreatedAt     *string `db:"created_at"`
	UpdatedAt     *string `db:"updated_at"`
	RowVersion    int     `db:"row_version"`
}

func NewWirelessLAN(id, name, ssid, security string, scopeAssetID *string) (*WirelessLAN, error)
func (w *WirelessLAN) Validate() error
func (w *WirelessLAN) Retired() bool

// InterfaceWLAN is one radio's membership of one SSID. No mode column, unlike
// InterfaceVLAN: a radio broadcasts an SSID or it does not, and there is no
// tagged/untagged distinction to make.
type InterfaceWLAN struct {
	InterfaceID   string `db:"interface_id"`
	WirelessLANID string `db:"wireless_lan_id"`
}

func ValidateWLANMembership(members []InterfaceWLAN) error
```

- [ ] **Step 1.** Create `internal/domain/wireless.go` with the licence header, a blank line, then `package domain`. **Zero external dependencies** — `strings` and the package's own `ValidationError` only.
- [ ] **Step 2.** `NewWirelessLAN` trims `name` and `ssid`, sets `Lifecycle = LifecycleActive`, and calls `Validate`.
- [ ] **Step 3.** `Validate` checks, and the comment must say what is deliberately *not* checked:

```go
// Validate checks what the domain can check. The DB CHECK and the foreign key
// are the second line of defence, not the first.
//
// SECURITY IS NOT CHECKED HERE, deliberately: it is a vocabulary, not a Go
// constant set, so a value inserted a second ago must be accepted a second
// later. The store checks it with requireVocabulary inside the transaction that
// is about to store it -- which is what turns SQLite's column-less "FOREIGN KEY
// constraint failed" into a field-level 422 that lists the values that exist NOW.
//
// PSK_REF IS NEVER REQUIRED, and no cross-field rule ties it to a security mode.
// An estate may record an SSID without recording where its secret lives, and
// demanding one is exactly how a passphrase ends up pasted into the field
// (docs/wireless-design.md §2.5). A wpa2_personal SSID with no psk_ref is an
// incomplete record; a wpa2_personal SSID with the key in it is an incident.
func (w *WirelessLAN) Validate() error {
	ve := &ValidationError{}
	if strings.TrimSpace(w.Name) == "" {
		ve.Add("name", "a wireless network needs a name; the SSID alone does not say what it is for")
	}
	if strings.TrimSpace(w.SSID) == "" {
		ve.Add("ssid", "an SSID is what is broadcast on the air; it cannot be blank")
	}
	if strings.TrimSpace(w.Security) == "" {
		ve.Add("security", "is required")
	}
	if w.Lifecycle != LifecycleActive && w.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", w.Lifecycle)
	}
	return ve.OrNil()
}
```

- [ ] **Step 4.** `ValidateWLANMembership` refuses a duplicate `wireless_lan_id` in the submitted slice with `ErrInvalid`. The primary key would refuse it anyway; this gives the operator a field-level 422 instead of a bare one. No untagged-equivalent rule exists — one radio broadcasting six SSIDs is normal.
- [ ] **Step 5.** Table-driven unit test in `internal/domain/wireless_test.go`: blank name, blank SSID, blank security, a bad lifecycle, a duplicate member, and — as a positive assertion with a comment naming the reason — **a `wpa2_personal` LAN with a nil `PSKRef` is valid**.
- [ ] **Step 6.** `make lint && make test`.

---

## Task 4 — Store: CRUD, retire, and the vocabulary reader

**Interfaces:**

```go
// internal/store/vocabulary.go
const vocabWirelessSecurity = "wireless_security"
func (s *SQLStore) WirelessSecurities(ctx context.Context) ([]VocabularyTerm, error)

// internal/store/wireless.go
type WirelessLANRow struct {
	domain.WirelessLAN
	SecurityLabel  string `db:"security_label"`
	ScopeAssetName string `db:"scope_asset_name"`
	VLANName       string `db:"vlan_name"`
	VLANVID        *int   `db:"vlan_vid"`
	AuthServiceName string `db:"auth_service_name"`
	// RadioCount is what turns a record into a broadcast SSID on screen. An
	// SSID with none is declared and on the air nowhere, which is a finding
	// rather than a gap in this query.
	RadioCount int `db:"radio_count"`
	// AssetCount is DISTINCT assets, not radios: that is the number the impact
	// engine counts, and showing radios beside a finding that counts boxes
	// would make the page disagree with the simulator.
	AssetCount int `db:"asset_count"`
}

func (s *SQLStore) ListWirelessLANs(ctx context.Context) ([]WirelessLANRow, error)
func (s *SQLStore) GetWirelessLAN(ctx context.Context, id string) (*domain.WirelessLAN, error)
func (s *SQLStore) CreateWirelessLAN(ctx context.Context, p domain.Permit, w *domain.WirelessLAN) error
func (s *SQLStore) UpdateWirelessLAN(ctx context.Context, p domain.Permit, w *domain.WirelessLAN) error
func (s *SQLStore) RetireWirelessLAN(ctx context.Context, p domain.Permit, id string) error
```

- [ ] **Step 1.** `internal/store/vocabulary.go`: add `vocabWirelessSecurity` to the const block, its literal query to `vocabularyQueries` (`SELECT code, label, sort_order, description FROM wireless_security ORDER BY sort_order, code` — a full literal, not a concatenated table name, per the file's own comment at lines 36-39), and the `WirelessSecurities` reader. Add it to `TestEveryVocabularyTermHasADescription`'s list (`vocabulary_test.go:506-514`).
- [ ] **Step 2.** Create `internal/store/wireless.go` with the header and `ListWirelessLANs`:

```go
	err := s.read(ctx, &rows, `
		SELECT w.*,
		       COALESCE(sec.label, '') AS security_label,
		       COALESCE(a.name, '')    AS scope_asset_name,
		       COALESCE(v.name, '')    AS vlan_name,
		       v.vid                   AS vlan_vid,
		       COALESCE(svc.name, '')  AS auth_service_name,
		       (SELECT COUNT(*) FROM interface_wlan iw WHERE iw.wireless_lan_id = w.id) AS radio_count,
		       (SELECT COUNT(DISTINCT i.asset_id) FROM interface_wlan iw
		          JOIN interface i ON i.id = iw.interface_id
		         WHERE iw.wireless_lan_id = w.id) AS asset_count
		FROM wireless_lan w
		LEFT JOIN wireless_security sec ON sec.code = w.security
		LEFT JOIN asset a   ON a.id   = w.scope_asset_id
		LEFT JOIN vlan v    ON v.id   = w.vlan_id
		LEFT JOIN service svc ON svc.id = w.auth_service_id
		WHERE w.lifecycle <> 'retired'
		ORDER BY COALESCE(a.name, ''), w.ssid`)
```

`COUNT(DISTINCT ...)` and scalar subqueries in the select list are ANSI and behave identically on both engines. No `?` here because there is no parameter; `ListVLANs` is the same shape.

- [ ] **Step 3.** `CreateWirelessLAN`, following `CreateVLAN` (`vlans.go:67-94`) exactly: `Validate()`, `RowVersion = 1`, timestamps from `s.now()`, then inside `s.write`:
  - `t.requireVocabulary(ctx, vocabWirelessSecurity, "security", w.Security)` **first**, before the `INSERT`, so an unknown mode is a field-level 422 rather than a bare one.
  - the `INSERT` with `?` placeholders, `translateWriteErr(err, "creating wireless lan")`.
  - `t.logCreate(ctx, "wireless_lan", w.ID, w)` — `psk_ref` is redacted by `snapshotJSON` via `domain.RedactedFields`, which Task 4's test proves rather than assumes.
  - `s.indexEntity` with `EntityType: "wireless_lan"`, `Title: w.SSID + " — " + w.Name`, `Body: w.SSID + " " + w.Name`. **The SSID is exactly what somebody types into search during an incident** ("who broadcasts warehouse-scan"), which is the whole reason to index it.
- [ ] **Step 4.** `UpdateWirelessLAN` following `UpdateVLAN`: read `before` via `GetWirelessLAN`, `requireVocabulary`, `UPDATE ... row_version = row_version + 1 WHERE id = ? AND row_version = ?`, `requireVersion`, `t.logUpdate`, reindex. **`lifecycle` is not in the `SET` list** — retirement is `RetireWirelessLAN`'s job, exactly as `UpdateVLAN` leaves it out.
- [ ] **Step 5.** `RetireWirelessLAN` following `RetireVLAN` (`vlans.go:140-176`), inside `s.writeSerializable`, **refusing while radios still broadcast it**:

```go
// RetireWirelessLAN withdraws an SSID.
//
// It refuses while any radio still broadcasts it, the same rule and the same
// reason RetireVLAN gives: a retired SSID with radios on it is a record saying
// "this does not exist" beside several saying "and here is what carries it" --
// and soft delete means the contradiction is permanent rather than merely wrong.
```

The count is `SELECT COUNT(*) FROM interface_wlan WHERE wireless_lan_id = ?`; `> 0` returns `domain.ErrConflict` with the number, so the handler answers 409.

- [ ] **Step 6.** Write `TestSnapshotRedactsPSKRef` in `internal/store/wireless_test.go`. **This is §4 item 9 bullet 2 and its shape is `TestSnapshotRedactsSecretRef`'s** (`boundary_test.go:728-810`) — both directions, and the whole `change_log` table read raw rather than through `ListRecentChanges`, which caps at a limit and would let a leak hide beyond the last page:

```go
// TestSnapshotRedactsPSKRef: psk_ref is absent from every change_log diff, and
// a rotation is still visible as having happened.
//
// docs/AUDIT.md rule 12, applied to the field D4 introduced: "Record THAT
// secret_ref changed, never what to. A path is not a secret, but a complete map
// of secret paths readable by every account is a reconnaissance gift."
//
// BOTH HALVES, because redacting one and not the other leaks on the first
// UPDATE instead of the first CREATE -- a worse failure, because it looks fixed.
// The whole table is read raw for the reason TestSnapshotRedactsSecretRef gives.
func TestSnapshotRedactsPSKRef(t *testing.T) {
	const pskPath = "kv/prod/wifi/corp/psk"
	const rotated = "kv/prod/wifi/corp/psk-2026-09"

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			w, err := domain.NewWirelessLAN(NewID(), "Corp", "corp", "wpa2_personal", nil)
			if err != nil {
				t.Fatalf("building wireless lan: %v", err)
			}
			w.PSKRef = strPtr(pskPath)
			if err := s.CreateWirelessLAN(ctx, testPermit, w); err != nil {
				t.Fatalf("creating wireless lan: %v", err)
			}

			var diffs []string
			if err := s.read(ctx, &diffs, `SELECT diff FROM change_log`); err != nil {
				t.Fatalf("reading every audit diff: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("the audit trail is empty; this test proved nothing")
			}
			for _, diff := range diffs {
				if strings.Contains(diff, pskPath) {
					t.Errorf("a PSK path reached the audit trail: %s", diff)
				}
			}

			// The rule asks for the change to be RECORDED, not erased: a reader
			// must be able to tell the field exists and moved.
			var entry string
			err = s.readOne(ctx, &entry,
				`SELECT diff FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"wireless_lan", w.ID)
			if err != nil {
				t.Fatalf("reading the wireless lan's audit entry: %v", err)
			}
			if !strings.Contains(entry, "psk_ref") {
				t.Errorf("the diff does not mention psk_ref at all, so a reader cannot tell "+
					"the SSID has one: %s", entry)
			}
			if !strings.Contains(entry, domain.Redacted) {
				t.Errorf("the diff does not mark the field redacted: %s", entry)
			}

			// The UPDATE path, through the real store method rather than
			// diffJSON directly -- unlike identity, there IS an UpdateWirelessLAN,
			// so the rotation an operator would actually perform is what gets
			// driven here.
			t.Run("a rotation is recorded as having happened and not as what to", func(t *testing.T) {
				before, err := s.ListChangesForEntity(ctx, "wireless_lan", w.ID, 50)
				if err != nil {
					t.Fatalf("reading the change log: %v", err)
				}
				w.PSKRef = strPtr(rotated)
				if err := s.UpdateWirelessLAN(ctx, testPermit, w); err != nil {
					t.Fatalf("rotating the psk reference: %v", err)
				}
				after, err := s.ListChangesForEntity(ctx, "wireless_lan", w.ID, 50)
				if err != nil {
					t.Fatalf("reading the change log: %v", err)
				}
				if len(after) <= len(before) {
					t.Fatalf("rotating the PSK reference wrote no change_log entry "+
						"(%d before, %d after). Redaction must hide the VALUE, not the "+
						"fact that it moved -- an invisible rotation is worse than a "+
						"visible one", len(before), len(after))
				}
				newest := after[0]
				if strings.Contains(newest.Diff, rotated) || strings.Contains(newest.Diff, pskPath) {
					t.Errorf("the rotation leaked a PSK path: %s", newest.Diff)
				}
				if !strings.Contains(newest.Diff, "psk_ref") || !strings.Contains(newest.Diff, domain.Redacted) {
					t.Errorf("the rotation entry does not record that psk_ref changed: %s", newest.Diff)
				}
			})
		})
	}
}
```

- [ ] **Step 7.** **Prove the test can fail.** Comment out `"psk_ref": true` in `RedactedFields`, run `make test`, watch the first `t.Errorf` fire on both engines, restore. Then invert it: make `diffJSON` drop redacted fields entirely instead of masking them, watch the "invisible rotation" assertion fire, restore. A test that has never been observed failing is a claim, not a check.
- [ ] **Step 8.** `TestAWirelessLANWithRadiosCannotBeRetired`, modelled on `TestAVLANInUseCannotBeRetired` (`vlans_test.go:~180`): retire with a radio on it → `ErrConflict`; clear the membership; retire again → nil.
- [ ] **Step 9.** `make lint && make test`. Commit via `commit-writer` — the *why* is "an SSID is a record with a lifecycle; the PSK is a path and the audit trail must never hold one".

---

## Task 5 — `SetInterfaceWLANs` and the audit fold (**the highest-risk task**)

**Interfaces:**

```go
// internal/store/wireless.go
func (s *SQLStore) ListInterfaceWLANMembers(ctx context.Context, interfaceID string) ([]domain.InterfaceWLAN, error)
func (s *SQLStore) SetInterfaceWLANs(ctx context.Context, p domain.Permit, interfaceID string, members []domain.InterfaceWLAN) error
func (s *SQLStore) AddRadioToWLAN(ctx context.Context, p domain.Permit, wlanID, interfaceID string) error
func (s *SQLStore) RemoveRadioFromWLAN(ctx context.Context, p domain.Permit, wlanID, interfaceID string) error

type WLANRadio struct {
	InterfaceID   string `db:"interface_id"`
	InterfaceName string `db:"interface_name"`
	FormFactor    string `db:"form_factor"`
	Enabled       bool   `db:"enabled"`
	AssetID       string `db:"asset_id"`
	AssetName     string `db:"asset_name"`
}
func (s *SQLStore) ListWLANRadios(ctx context.Context, wlanID string) ([]WLANRadio, error)
func (s *SQLStore) ListRadioOptions(ctx context.Context) ([]InterfaceOption, error)

// unexported
type interfaceWLANAudit struct {
	domain.Interface
	WirelessLANs string `db:"wireless_lans"`
}
func auditedInterfaceWLANs(i *domain.Interface, ssids []string) *interfaceWLANAudit
func (s *SQLStore) listInterfaceWLANSSIDs(ctx context.Context, interfaceID string) ([]string, error)
```

- [ ] **Step 1.** Write the audit struct **first**, with its comment, before anything that uses it. The comment is the deliverable as much as the code is:

```go
// interfaceWLANAudit is the audited shape of a radio: the port row plus the
// SSIDs it broadcasts.
//
// THE `db` TAGS ARE THE WHOLE POINT. diffJSON compares every db-TAGGED field
// and ignores the rest (internal/store/diff.go), and writing to interface_wlan
// changes no column of the interface row -- so an audit that diffed a plain
// domain.Interface would compare a row that did not change and record SILENCE.
// The radio moved from `corp` to `guest` and change_log says nothing happened.
//
// THAT FAILURE HAS BEEN MADE FOUR TIMES IN THIS CODEBASE, and the fourth was in
// SetInterfaceVLANs -- the function this one copies. interfaceVLANAudit's own
// comment names it: "an audit struct whose membership fields carried only json
// tags diffed nothing but the interface id -- which never changes." A json tag
// here would compile, run, write an entry, and record nothing.
//
// SSID NAMES rather than row ids, and a joined string rather than a slice, both
// for the reason assetAudit gives: an audit entry is read by people, and
// "corp,guest" is a sentence where two UUIDs are a lookup exercise.
//
// EMBEDDED BY VALUE, never by pointer: auditFields panics on an anonymous
// pointer embed, because the shape that made the certificate audit record
// nothing at all matched neither branch and dropped every embedded column while
// still writing an entry.
type interfaceWLANAudit struct {
	domain.Interface
	WirelessLANs string `db:"wireless_lans"` // db, NOT json
}

func auditedInterfaceWLANs(i *domain.Interface, ssids []string) *interfaceWLANAudit {
	names := append([]string(nil), ssids...)
	sort.Strings(names)
	return &interfaceWLANAudit{
		Interface:    *i,
		WirelessLANs: strings.Join(names, ","),
	}
}
```

**Sorted, and the copy matters.** `sort.Strings` on the caller's slice would reorder a slice the caller still holds; and without the sort, two reads returning the same set in a different order produce a spurious diff. The read below already orders by SSID, so the sort is belt-and-braces — keep both, because the fold must not depend on a query's `ORDER BY` staying where it is.

- [ ] **Step 2.** `SetInterfaceWLANs`, following `SetInterfaceVLANs` (`vlans.go:265-347`) structurally:

```go
// SetInterfaceWLANs replaces a radio's whole set of broadcast SSIDs.
//
// REPLACED WHOLESALE, like interface_vlan and asset_environment, and audited on
// the INTERFACE -- the membership belongs to the radio and has no life of its
// own. The parent's change_log entry has to record it, or a radio moving from
// `corp` to `guest` produces no diff at all: the failure CLAUDE.md names three
// times over and this codebase has now made four. See interfaceWLANAudit for
// the mechanism that prevents a fifth.
func (s *SQLStore) SetInterfaceWLANs(ctx context.Context, p domain.Permit,
	interfaceID string, members []domain.InterfaceWLAN) error {

	if err := domain.ValidateWLANMembership(members); err != nil {
		return err
	}
	iface, err := s.GetInterface(ctx, interfaceID)
	if err != nil {
		return err
	}
	beforeSSIDs, err := s.listInterfaceWLANSSIDs(ctx, interfaceID)
	if err != nil {
		return err
	}
	before := auditedInterfaceWLANs(iface, beforeSSIDs)

	// WLAN membership is a write to the INTERFACE -- the audited value folds the
	// SSID list into the interface row and the change_log entry below names
	// entity_type "interface". So it takes the same subject derivation every
	// other interface write takes, or a project owner could edit an interface on
	// their own asset and then be refused when setting its SSIDs, which is the
	// same permission expressed two ways.
	//
	// NO NEW MINTER: authorizeInterfaceSubject is already in
	// storePermitMinters. A wireless-specific one would be a security-relevant
	// change requiring auth review and sign-off (permit_source_test.go), and
	// there is nothing here it could check that this does not.
	//
	// Reached from HTTP through AddRadioToWLAN and RemoveRadioFromWLAN, which
	// both delegate here -- POST /wireless/{id}/radios and its remove sibling.
	ifacePermit, err := authorizeInterfaceSubject(p, iface.AssetID, interfaceID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())

	return s.write(ctx, ifacePermit, func(t *tx) error {
		if _, err := t.exec(ctx,
			`DELETE FROM interface_wlan WHERE interface_id = ?`, interfaceID); err != nil {
			return translateWriteErr(err, "clearing wireless membership")
		}
		for _, m := range members {
			_, err := t.exec(ctx,
				`INSERT INTO interface_wlan (interface_id, wireless_lan_id) VALUES (?, ?)`,
				interfaceID, m.WirelessLANID)
			if err != nil {
				return translateWriteErr(err, "setting wireless membership")
			}
		}
		// The interface's own row carries the audit, so its timestamp moves with
		// the change it is recording.
		if _, err := t.exec(ctx, `
			UPDATE interface SET updated_at = ?, row_version = row_version + 1
			WHERE id = ?`, at, interfaceID); err != nil {
			return translateWriteErr(err, "touching interface")
		}

		// Read the resulting SSIDs back INSIDE the transaction. Composing them
		// from `members` in Go would look equivalent and is not: members carries
		// ids, the audit carries names, and resolving names outside the
		// transaction reads a wireless_lan row that another transaction may be
		// renaming.
		var ssids []string
		err := t.selectAll(ctx, &ssids, `
			SELECT w.ssid
			FROM interface_wlan iw JOIN wireless_lan w ON w.id = iw.wireless_lan_id
			WHERE iw.interface_id = ? ORDER BY w.ssid`, interfaceID)
		if err != nil {
			return fmt.Errorf("reading wireless membership: %w", err)
		}
		updated := *iface
		updated.UpdatedAt = &at
		updated.RowVersion = iface.RowVersion + 1
		after := auditedInterfaceWLANs(&updated, ssids)
		return t.logUpdate(ctx, "interface", interfaceID, before, after)
	})
}
```

- [ ] **Step 3.** `AddRadioToWLAN` and `RemoveRadioFromWLAN` — read the current set, append or filter, delegate to `SetInterfaceWLANs`. Copy `AddPortToVLAN`'s comment (`vlans.go:433-439`) and its reason verbatim in substance: *"Doing the INSERT here would skip the validation AND the audit fold, and the audit fold is the thing this codebase has now got wrong four times."* Adding a radio already in the set is a no-op returning nil; removing one that is not in the set is a no-op returning nil (`RemovePortFromVLAN`'s "not in it; nothing to record").
- [ ] **Step 4.** `ListWLANRadios` — the SSID's radios, with the box each is in. Give it the comment that says what it is:

```go
// ListWLANRadios returns every radio broadcasting an SSID, with the box it is in.
//
// THIS IS THE EDGE. A wireless LAN with a VLAN and no radios is a record; this
// is what makes it a broadcast domain, and it is a fact no cable trace can
// produce -- two laptops on `corp` can reach each other and no cable joins them.
```

`ListRadioOptions` is `ListPortOptions` filtered to radio form factors, and the filter must be **`i.form_factor LIKE 'radio%'`** — not a hardcoded IN-list of the three codes. The form factors are a vocabulary and `radio_60g` will arrive as an `INSERT`; an IN-list would make it storable and unofferable, which is the exact failure `vocabulary.go:30-34` describes. Say so in a comment. (`LIKE` with a literal pattern behaves identically on both engines; no `ESCAPE` clause is needed because the pattern has no metacharacter beyond the trailing `%`.)

- [ ] **Step 5.** Add the allowlist entry to `internal/store/prune_test.go`'s `allowed` map, in the same style as the ten already there:

```go
		// interface_wlan holds the CURRENT set of SSIDs a radio broadcasts,
		// which the radio owns. Replaced wholesale inside the interface's
		// transaction, and auditedInterfaceWLANs puts the SSID NAMES into the
		// audited value so the replacement cannot produce an empty diff -- a
		// radio moving from `corp` to `guest` with no entry in change_log
		// would be the FIFTH time this codebase made that exact mistake.
		//
		// The SSIDs themselves are soft-retired like every other entity, and
		// RetireWirelessLAN refuses while any radio still broadcasts one.
		"internal/store/wireless.go": {
			tables: []string{"interface_wlan"},
			reason: "interface_wlan: the set of SSIDs a radio broadcasts, replaced wholesale",
		},
```

- [ ] **Step 6.** Write **`TestMovingARadioBetweenSSIDsAuditsTheInterface`** — §4 item 9 bullet 1, and the one a reviewer will check exercises the *fold* rather than merely existing. Modelled on `TestChangingAPortsVLANsIsAuditedOnThePort` (`vlans_test.go:86-155`), including its second half: the VLAN version was mutation-tested and the *tagged* field's `db` tag survived without a trunk case, so the multi-SSID case is not optional here either.

```go
func mustRadio(t *testing.T, s *SQLStore, ctx context.Context, assetID, name string) string {
	t.Helper()
	// A RADIO, not an RJ45 -- mustInterface hardcodes FFRJ45. Nothing in the
	// store branches on the form factor, so this would pass with rj45; it is a
	// radio because a fixture that describes an access point with a copper port
	// is a fixture nobody can read.
	iface, err := domain.NewInterface(NewID(), assetID, name, domain.FFRadio5G)
	if err != nil {
		t.Fatalf("building radio %s: %v", name, err)
	}
	if err := s.CreateInterface(ctx, testPermit, iface); err != nil {
		t.Fatalf("creating radio %s: %v", name, err)
	}
	return iface.ID
}

func mustWLAN(t *testing.T, s *SQLStore, ctx context.Context, name, ssid string, scope *string) string {
	t.Helper()
	w, err := domain.NewWirelessLAN(NewID(), name, ssid, "wpa2_personal", scope)
	if err != nil {
		t.Fatalf("building wireless lan %s: %v", ssid, err)
	}
	if err := s.CreateWirelessLAN(ctx, testPermit, w); err != nil {
		t.Fatalf("creating wireless lan %s: %v", ssid, err)
	}
	return w.ID
}

// TestMovingARadioBetweenSSIDsAuditsTheInterface. The set replacement CLAUDE.md
// names three times and this codebase has now got wrong four -- the fourth in
// SetInterfaceVLANs, the function SetInterfaceWLANs copies.
//
// IT MUST FAIL WHEN THE MEMBERSHIP IS WRITTEN WITHOUT FOLDING INTO THE
// INTERFACE'S AUDITED VALUE, not merely when the write itself breaks. That is
// why it asserts on change_log CONTENT and not only on the count: dropping the
// `db` tag from interfaceWLANAudit.WirelessLANs leaves the write working, leaves
// an entry being written (the interface's row_version and updated_at move), and
// silently empties the diff of everything that changed.
func TestMovingARadioBetweenSSIDsAuditsTheInterface(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			ap := mustAsset(t, s, ctx, domain.KindSwitch, "ap-oslo-1", nil)
			radio := mustRadio(t, s, ctx, ap, "radio0")
			corp := mustWLAN(t, s, ctx, "Corp", "corp", nil)
			guest := mustWLAN(t, s, ctx, "Guest", "guest", nil)

			set := func(ids ...string) {
				t.Helper()
				members := make([]domain.InterfaceWLAN, 0, len(ids))
				for _, id := range ids {
					members = append(members, domain.InterfaceWLAN{
						InterfaceID: radio, WirelessLANID: id,
					})
				}
				if err := s.SetInterfaceWLANs(ctx, testPermit, radio, members); err != nil {
					t.Fatalf("setting wireless membership: %v", err)
				}
			}

			set(corp)
			before, err := s.ListChangesForEntity(ctx, "interface", radio, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			set(guest)
			after, err := s.ListChangesForEntity(ctx, "interface", radio, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}

			if len(after) <= len(before) {
				t.Fatalf("moving the radio from corp to guest wrote no change_log entry "+
					"(%d before, %d after). A set replacement that produces no diff on "+
					"the parent is the failure CLAUDE.md names three times", len(before), len(after))
			}
			// THE DIFF MUST NAME THE SSIDs, or the entry records that something
			// changed without recording what -- which is what the missing `db`
			// tag produced the first four times.
			newest := after[0]
			if !strings.Contains(newest.Diff, "guest") {
				t.Errorf("the audit entry does not mention the SSID the radio moved TO: %s", newest.Diff)
			}
			if !strings.Contains(newest.Diff, "corp") {
				t.Errorf("the audit entry does not mention the SSID the radio moved FROM, so "+
					"a reader cannot tell what was lost: %s", newest.Diff)
			}

			// AND A SECOND SSID ADDED TO A RADIO THAT KEEPS ITS FIRST, which the
			// move above does not cover. One AP radio serves many SSIDs -- that
			// is the whole reason this is not a `link` row -- and adding one
			// while the others stay is the commonest wireless change there is.
			// Mutation testing on the VLAN twin showed the tagged field's db tag
			// survived without exactly this case.
			warehouse := mustWLAN(t, s, ctx, "Warehouse scanners", "warehouse-scan", nil)
			set(guest, corp)
			beforeAdd, _ := s.ListChangesForEntity(ctx, "interface", radio, 50)
			set(guest, corp, warehouse)
			afterAdd, err := s.ListChangesForEntity(ctx, "interface", radio, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(afterAdd) <= len(beforeAdd) {
				t.Fatalf("adding warehouse-scan wrote no change_log entry (%d before, "+
					"%d after). The other two SSIDs did not change, so only the folded "+
					"set could record this", len(beforeAdd), len(afterAdd))
			}
			if !strings.Contains(afterAdd[0].Diff, "warehouse-scan") {
				t.Errorf("the audit entry does not mention warehouse-scan: %s", afterAdd[0].Diff)
			}
		})
	}
}
```

- [ ] **Step 7.** **Prove it can fail — this is the step the whole task exists for.** Three mutations, each run to red on both engines and then restored:
  1. Change `db:"wireless_lans"` to `json:"wireless_lans"`. Expect the *content* assertions to fire while the count assertion still passes — that is precisely the failure mode, and if the count assertion fires too, the test is weaker than it looks and needs re-reading.
  2. Delete the `WirelessLANs` field entirely. Expect the same.
  3. Replace `w.ssid` with `iw.wireless_lan_id` in the read-back query. Expect the `strings.Contains(..., "guest")` assertion to fire — this guards the "read by people" half, which no count-based test can see.
- [ ] **Step 8.** `TestARadioBroadcastsManySSIDsAndThatIsNotAConflict` — set three SSIDs on one radio, assert no error and `ListWLANRadios` returns the radio for all three. This is §2.4 as a test: `CreateLink` would have refused the second association with `ErrConflict`, and this is the property that made `link` the wrong table.
- [ ] **Step 9.** `make lint && make test`. Commit.

---

## Task 6 — The fourth structure join

**Interfaces:**

```go
// internal/impact/structures.go
const StructureWLAN = "wlan"
```

No signature changes anywhere. `analyseStructures` is untouched — the two states it reports are already the right two, and §2.2 argues that an SSID on one AP is a **standing** finding the redundancy page reports permanently, not a simulation result.

- [ ] **Step 1.** `internal/impact/structures.go`: add `StructureWLAN = "wlan"` to the kind block, and a sentence to the type's doc comment noting the fourth kind arrived in WP-F1.
- [ ] **Step 2.** `emptiedDetail` and `reducedDetail` gain a case each. The sentences are assembled here because templates must not decide what is wrong (the type's own comment says so):

```go
	case StructureWLAN:
		return fmt.Sprintf("nothing is broadcasting it any more (all %d access points "+
			"carrying it are down)", total)
```

```go
	case StructureWLAN:
		return fmt.Sprintf("one access point left of %d — the SSID is on the air in one "+
			"place and does not survive the next failure", total)
```

- [ ] **Step 3.** `structureHref` gains `case StructureWLAN: return "/wireless/" + id`. **This must match the route Task 7 registers**, or the finding cannot be opened; `TestTheDetailNamesTheKind` (`structures_test.go:176`) asserts every finding has a non-empty `Href` but cannot check it resolves — Task 7's route registration test is what does that.
- [ ] **Step 4.** `internal/impact/structures_test.go`: add a WLAN to `structureFixture()` so the existing five tests cover the new kind without any of them being edited:

```go
		// An SSID on two access points. Fourth kind, WP-F1 -- and it goes in the
		// SHARED fixture rather than a test of its own so that every property
		// already asserted here (emptied, reduced, sorted worst-first, a
		// detail sentence, a working href) covers it without being rewritten.
		{Kind: StructureWLAN, ID: "w1", Name: "corp",
			AssetIDs: []string{"sw-a", "sw-b"}},
```

Then update `TestLosingEveryPortEmptiesTheStructure`'s name list to include `"corp"`.

- [ ] **Step 5.** `internal/store/graph.go` `loadStructures` — the fourth query, after the overlay one. **Both `wireless_lan` and `interface_wlan` must appear literally after a `FROM` or `JOIN` in this file**, or `TestEveryConnectiveTableIsAccountedForInTheImpactGraph` fails: it regex-scans `graph.go`'s text for `\b(?:FROM|JOIN)\s+(table)`, and `wireless_lan` has four foreign keys (`wireless_security`, `asset`, `vlan`, `service`) while `interface_wlan` has two, so **both** are "connective" by that test's rule and both would otherwise need an entry in `connectiveTablesOutsideTheGraph`. They are joined, so neither does.

```go
	// A wireless LAN's members are the radios broadcasting it (WP-F1).
	//
	// The FOURTH kind, and the argument for it is interface_vlan's, unchanged:
	// two laptops on `corp` are in one broadcast domain and no cable joins them,
	// so this is a fact no cable trace can produce. It is deliberately NOT a
	// link row and NOT a net_uplink -- a radio cannot tell you which way traffic
	// flows any more than a cable can, and docs/reachability-design.md already
	// answers that question at forwarder-group level.
	//
	// SCOPED NAMES, composed in Go rather than in SQL. The same SSID at two
	// sites is two structures (D6), and two findings both reading "guest" would
	// be unreadable in the exact moment they matter. The concatenation is done
	// here rather than with `||` because string concatenation is one more thing
	// two engines could disagree about for no gain.
	var wlanRows []struct {
		ID        string  `db:"id"`
		SSID      string  `db:"ssid"`
		ScopeName *string `db:"scope_name"`
		AssetID   string  `db:"asset_id"`
	}
	err = s.read(ctx, &wlanRows, `
		SELECT w.id, w.ssid, a.name AS scope_name, i.asset_id
		FROM wireless_lan w
		JOIN interface_wlan iw ON iw.wireless_lan_id = w.id
		JOIN interface i ON i.id = iw.interface_id
		LEFT JOIN asset a ON a.id = w.scope_asset_id
		WHERE w.lifecycle <> 'retired'
		ORDER BY w.ssid, i.asset_id`)
	if err != nil {
		return nil, fmt.Errorf("loading wireless membership for the graph: %w", err)
	}
	wlans := make([]memberRow, 0, len(wlanRows))
	for _, r := range wlanRows {
		name := r.SSID
		if r.ScopeName != nil && *r.ScopeName != "" {
			name = *r.ScopeName + " / " + r.SSID
		}
		wlans = append(wlans, memberRow{
			Kind: "wlan", ID: r.ID, Name: name, AssetID: r.AssetID,
		})
	}
	collect(wlans)
```

**No `lifecycle` filter on the interface or the asset**, matching the three queries above it: a member port on a retired asset still contributes, because the engine's `down` set is what decides survival and filtering here would hide a member the simulation is meant to count.

- [ ] **Step 6.** `make test`. `TestEveryConnectiveTableIsAccountedForInTheImpactGraph` must stay green with **no** edit to `connectiveTablesOutsideTheGraph`. If it fails naming `wireless_lan` or `interface_wlan`, the query text is wrong — fix the query, do not add an exemption.
- [ ] **Step 7.** **Prove it.** Temporarily rename the table in the `FROM` clause to a comment (`-- FROM wireless_lan`), run the coverage test, watch it fail naming `wireless_lan`, restore. That test is the whole reason WP-I1 exists as a standing audit rather than an afternoon's grep.
- [ ] **Step 8.** Write the four impact tests, §4 item 9 bullets 3–6, in `internal/store/wireless_test.go`. They go at the **store** level rather than in `internal/impact`, because bullet 7 asks for dual-engine and the fixture-level tests in `internal/impact` are pure Go: what needs proving on both engines is that `loadStructures`'s SQL produces the right structures, not that `analyseStructures` counts correctly (which `structures_test.go` already proves once).

```go
// wirelessEstate builds the smallest estate that can demonstrate all four
// findings, and returns the ids the tests name.
//
// THE SHAPE IS THE POINT and mirrors the seed fixture (internal/seed):
//   corp            two APs -> reduced to one by losing one
//   guest           two APs -> emptied by losing both
//   warehouse-scan  one AP  -> a standing single point of failure, NOT reported
func wirelessEstate(t *testing.T, s *SQLStore, ctx context.Context) (ap1, ap2 string) { ... }

// TestAnOutageDowningEveryAPEmptiesTheSSID. §4 item 9 bullet 3.
func TestAnOutageDowningEveryAPEmptiesTheSSID(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			ap1, ap2 := wirelessEstate(t, s, ctx)

			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{ap1, ap2}})
			if err != nil {
				t.Fatalf("simulating: %v", err)
			}
			f, ok := wirelessFinding(res, "guest")
			if !ok {
				t.Fatalf("guest is not reported, but every AP carrying it is down. "+
					"Findings: %+v", res.Structures)
			}
			if !f.Emptied() {
				t.Errorf("guest reports %d remaining, want 0", f.Remaining)
			}
			if f.Total != 2 {
				t.Errorf("guest reports %d assets before the outage, want 2", f.Total)
			}
			if f.Detail == "" || f.Href == "" {
				t.Errorf("the finding has no sentence or does not link anywhere: %+v", f)
			}
		})
	}
}

// TestAnOutageLeavingOneAPReportsReducedToOne. §4 item 9 bullet 4.
func TestAnOutageLeavingOneAPReportsReducedToOne(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			ap1, _ := wirelessEstate(t, s, ctx)

			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{ap1}})
			if err != nil {
				t.Fatalf("simulating: %v", err)
			}
			f, ok := wirelessFinding(res, "corp")
			if !ok {
				t.Fatalf("corp is not reported, but it is down to one AP of two. "+
					"Findings: %+v", res.Structures)
			}
			if f.Remaining != 1 || f.Total != 2 {
				t.Errorf("corp reports %d of %d, want 1 of 2", f.Remaining, f.Total)
			}
		})
	}
}

// TestAnSSIDThatHadOneAPAndStillHasOneIsNotReported. §4 item 9 bullet 5, and
// §2.2's pre-existing answer to the roadmap's "simulate a wireless bridge as a
// single point of failure": a simulation answers "what breaks if this fails",
// and for a thing that is already the only path there is nothing to simulate.
// The redundancy page says it permanently and without needing an outage.
func TestAnSSIDThatHadOneAPAndStillHasOneIsNotReported(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			ap1, _ := wirelessEstate(t, s, ctx)

			// ap1 is down; warehouse-scan lives on ap2 alone and ap2 is up.
			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{ap1}})
			if err != nil {
				t.Fatalf("simulating: %v", err)
			}
			if _, ok := wirelessFinding(res, "warehouse-scan"); ok {
				t.Error("warehouse-scan is reported, but it had one AP before this outage " +
					"and still has it -- that is a standing finding, not a consequence, " +
					"and repeating it here answers a question nobody asked")
			}
			// And the test must prove it looked: if corp is missing too, the
			// simulation returned nothing and this assertion is vacuous.
			if _, ok := wirelessFinding(res, "corp"); !ok {
				t.Fatal("corp is not reported either, so this test proved nothing")
			}
		})
	}
}

// TestTheSameSSIDAtTwoSitesIsTwoStructures. §4 item 9 bullet 6, and D6's whole
// point: `ssid` alone is deliberately not unique, so the Oslo `guest` and the
// Frankfurt `guest` are two rows, two structures, and two findings.
func TestTheSameSSIDAtTwoSitesIsTwoStructures(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			oslo := mustAsset(t, s, ctx, domain.KindSite, "dc-oslo", nil)
			fra := mustAsset(t, s, ctx, domain.KindSite, "colo-fra1", nil)
			apO := mustAsset(t, s, ctx, domain.KindSwitch, "ap-oslo-1", &oslo)
			apF := mustAsset(t, s, ctx, domain.KindSwitch, "ap-fra-1", &fra)

			guestO := mustWLAN(t, s, ctx, "Guest (Oslo)", "guest", &oslo)
			guestF := mustWLAN(t, s, ctx, "Guest (Frankfurt)", "guest", &fra)
			if guestO == guestF {
				t.Fatal("the two SSIDs share an id, so nothing below means anything")
			}

			radioO := mustRadio(t, s, ctx, apO, "radio0")
			radioF := mustRadio(t, s, ctx, apF, "radio0")
			mustSetWLANs(t, s, ctx, radioO, guestO)
			mustSetWLANs(t, s, ctx, radioF, guestF)

			g, err := s.LoadGraph(ctx)
			if err != nil {
				t.Fatalf("loading the graph: %v", err)
			}
			var ids []string
			for _, st := range g.Structures {
				if st.Kind == impact.StructureWLAN {
					ids = append(ids, st.ID)
				}
			}
			if len(ids) != 2 {
				t.Fatalf("the graph holds %d wireless structures, want 2: two estates "+
					"reusing one SSID name are two broadcast domains that have never "+
					"met. Got %v", len(ids), ids)
			}

			// AND THEY MUST NOT COLLAPSE INTO ONE FINDING. Downing the Oslo AP
			// empties the Oslo guest and leaves Frankfurt's alone; a shared
			// structure would report one finding covering both, which is the
			// failure a name-keyed implementation would produce.
			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{apO}})
			if err != nil {
				t.Fatalf("simulating: %v", err)
			}
			var emptied, untouched int
			for _, f := range res.Structures {
				if f.Kind != impact.StructureWLAN {
					continue
				}
				if f.Emptied() {
					emptied++
				}
			}
			if emptied != 1 {
				t.Errorf("losing the Oslo AP emptied %d wireless structures, want exactly "+
					"1 -- the Frankfurt guest is untouched. Findings: %+v",
					emptied, res.Structures)
			}
			_ = untouched
		})
	}
}
```

`wirelessFinding(res, name)` is a small helper matching on the finding's `Kind == impact.StructureWLAN` and its `Name` **suffix** (the name may be scope-prefixed). Write it in the test file.

- [ ] **Step 9.** `TestAnSSIDIsUniqueWithinItsScopeAndTheUnscopedPoolIsOne` — the index test, modelled on `TestAVIDIsUniqueWithinItsGroupAndTheUngroupedPoolIsOne` (`vlans_test.go:37-83`). Four sub-tests: same SSID at another site is **allowed**; same SSID twice at one site is **refused**; a second unscoped SSID of the same name is **refused**, with the message naming why (*"NULLs are distinct in SQL, so a composite over (ssid, scope_asset_id) enforces nothing here — and this is where every SSID starts"*); and a **retired** row does not block a new one of the same name.
- [ ] **Step 10.** `make lint && make test`. Commit.

---

## Task 7 — UI: list, detail, routes, nav

**Interfaces:**

```go
// internal/web/handlers/wireless.go
func (a *App) WirelessList(w http.ResponseWriter, r *http.Request)
func (a *App) WirelessDetail(w http.ResponseWriter, r *http.Request)
func (a *App) WirelessCreate(w http.ResponseWriter, r *http.Request)
func (a *App) WirelessRetire(w http.ResponseWriter, r *http.Request)
func (a *App) WirelessRadioAdd(w http.ResponseWriter, r *http.Request)
func (a *App) WirelessRadioRemove(w http.ResponseWriter, r *http.Request)
```

Routes, matching the VLAN shape exactly so `structureHref` resolves:

```go
	read("GET /wireless", app.WirelessList)
	read("GET /wireless/{id}", app.WirelessDetail)

	write("POST /wireless", app.WirelessCreate)
	write("POST /wireless/{id}/retire", app.WirelessRetire)
	write("POST /wireless/{id}/radios", app.WirelessRadioAdd)
	write("POST /wireless/{id}/radios/{ifaceID}/remove", app.WirelessRadioRemove)
```

- [ ] **Step 1.** `internal/web/handlers/wireless.go`, copying `vlans.go`'s structure. `WirelessList` renders the list plus the create form (`ListWirelessLANs`, `WirelessSecurities`, `ListVLANs`, `ListServices`, and the asset list for the scope picker). Every mutating handler branches on `HX-Request` through `render.Respond`; validation failure re-renders the form partial with **422**; success uses `render.Redirect` (`HX-Redirect`, not a 302).
- [ ] **Step 2.** `WirelessDetail` renders the SSID, its VLAN, its auth service, and `ListWLANRadios`. **The radio picker must be filtered through `writableInterfaceOptions`** and carry a `pickerHint` — `SetInterfaceWLANs` is `ScopeSubjectDerived` through the radio's owning asset, so a picker offering a radio on an asset the caller does not own is offered-and-refused, the defect fix-b item 3 closed for the dependency, link and VLAN pickers. Copy `vlans.go:107-138`'s comment and adapt it.
- [ ] **Step 3.** Per-row `CanWrite`/`ShowActions`, using the `vlanPortRow`/`vlanPortRows` pattern verbatim (`vlans.go:141-182`) and its comment. **Not a page-wide `.CanWrite`** — two radios on the same SSID can have different owners, and a page-wide flag over-offers Remove on a foreign radio and leaves the table header one column short of the writable row.
- [ ] **Step 4.** **`psk_ref` in the form and on the page.** The input is a plain text field labelled as a *reference*, with a hint that says what it is for and what it must not contain — e.g. `A path to where the key lives (kv/prod/wifi/corp/psk). Never the passphrase itself.` The detail page renders the reference as text and **never** as a link, and there is no "reveal" control, because there is nothing to reveal. State this in a template comment: the field looks like a password field and must not be one.
- [ ] **Step 5.** `web/templates/pages/wireless_list.html` and `wireless_detail.html`, following `vlan_list.html`/`vlan_detail.html`. `hx-swap="outerHTML"` on a wrapping element with a stable id; swap targets declared in the template, not chosen by the handler. No inline `<script>` beyond `x-data`. Every new file gets the AGPL comment header.
- [ ] **Step 6.** `internal/web/routes.go` — the six routes above. `internal/web/handlers/nav.go` — `{Label: "Wireless", Href: "/wireless", Nav: "wireless"}` in the **Network** group, after "Redundancy" (an SSID is a broadcast domain that lives on network hardware; Addressing is about prefixes and numbers).
- [ ] **Step 7.** `internal/web/handlers/help.go` — `wireless_security` into `vocabTopics`, with an intro sentence. **This is not optional decoration:** `vocabTable()` validates `?table=` against `store.VocabularyTables()`, which reads `vocabularyQueries` — so once Task 4 adds the query, `/vocabularies?table=wireless_security` resolves and, without a `vocabTopics` entry, renders a blank panel with no terms and no title. (`cost_kind`, `responsibility_role` and `storage_kind` are already in that state; this is a pre-existing latent gap and **not** F1's to fix, but F1 must not widen it.)
- [ ] **Step 8.** `internal/web/detail_pages_render_test.go:59` — add `{"wireless_lan", "/wireless/", "SELECT id FROM wireless_lan LIMIT 1"}` to the census. `web/templates/partials/rows.html:~104` — a `{{else if eq .EntityType "wireless_lan"}}` branch linking to `/wireless/{{.EntityID}}`. (VLAN search hits render unlinked today; that is pre-existing and out of scope.)
- [ ] **Step 9.** Optional but cheap: `internal/web/rbac_boundary_test.go`'s `resolveByPreceding` gains `"wireless": func() string { return fx.wirelessID }`. Not required — the map falls back to `store.NewID()`, and `ScopeTopology` means a project owner is refused at `tx.authorize` regardless — but a real id makes the refusal-layer assertion measure the layer it claims to.
- [ ] **Step 10.** `make lint && make test`. The route-registration and golden tests will pick up the new routes; if `TestNoWriteRouteIsReachableWithoutGoingThroughTheRouter` complains, the route is registered outside the `write` registrar and must move.

---

## Task 7b — The radios panel on an asset

- [ ] **Step 1.** `internal/store/wireless.go`:

```go
type AssetRadio struct {
	InterfaceID   string `db:"interface_id"`
	InterfaceName string `db:"interface_name"`
	FormFactor    string `db:"form_factor"`
	Enabled       bool   `db:"enabled"`
	// SSIDs is what this radio broadcasts, joined for display. Composed in Go
	// from one row per membership rather than with a SQL aggregate: neither
	// engine has a portable string aggregation, and this is the same reason
	// loadStructures composes its scoped names in Go.
	SSIDs []string
}
func (s *SQLStore) ListAssetRadios(ctx context.Context, assetID string) ([]AssetRadio, error)
```

The query returns one row per `(interface, wireless_lan)` with a `LEFT JOIN` so a radio broadcasting nothing still appears — **a radio with no SSID is a finding, not a row to hide.** Group in Go.

- [ ] **Step 2.** `internal/web/handlers/assets.go` — `Radios []store.AssetRadio` on the detail page struct, populated beside `Interfaces` (~line 555). Skip the query entirely when the asset has no radio-form-factor interface, so an estate with no wireless pays nothing.
- [ ] **Step 3.** `web/templates/pages/asset_detail.html` — a `Radios` panel after Interfaces, rendered only `{{if .Radios}}`. It answers "what is this AP broadcasting", which the flat interface table cannot.
- [ ] **Step 4.** `make lint && make test`. Commit.

---

## Task 8 — Seed

*(Assumes Decision A resolved. If `access_point` was refused, use `domain.KindSwitch` and say so in the phase comment.)*

- [ ] **Step 1.** `internal/seed/seed.go` — `WirelessLANs map[string]string` on `Refs` (keyed by SSID) and initialised in the literal at ~line 122.
- [ ] **Step 2.** `internal/seed/seed_engine.go` — a `b.wirelessLANs()` phase called from `engineEdges()` after `firstHopRedundancy()`, and a line added to `engineEdges`' fixture table (lines 30-38):

```
//	corp            two APs                       -> reduced to one by losing one
//	guest           two APs                       -> EMPTIED by losing both
//	warehouse-scan  one AP                        -> a standing single point of failure
```

- [ ] **Step 3.** The phase creates two APs under an existing site, three radios each (`radio_2g4`, `radio_5g`, `radio_6g` — which is what makes D2's "three, not one" visible in the demo), three SSIDs, and this membership:
  - `corp` on **ap-1/radio_5g and ap-2/radio_5g** → losing one AP reduces it to one.
  - `guest` on **both APs**, mapped to the existing `production-workloads` VLAN → losing both empties it.
  - `warehouse-scan` on **ap-2/radio_2g4 alone** → the standing single point of failure, so both states are on screen without a simulation.
  - `corp` gets `psk_ref = "kv/demo/wifi/corp/psk"` — **a path, and the demo is where somebody will look to see what the field is for.** No SSID gets a passphrase, because there is nowhere to put one.
  - One SSID (`corp`) gets `security = "wpa2_enterprise"` and `auth_service_id` pointing at an existing auth-kind service, so D5's recorded-but-not-an-edge column has a value on screen.

The membership goes through `ListInterfaceWLANMembers` → append → `SetInterfaceWLANs`, exactly as `vlanMembership()` does (`seed_engine.go:124-144`) — never a direct `INSERT`, which would skip the fold.

- [ ] **Step 4.** `internal/seed/seed_engine_test.go` — two fixture-honesty tests, in the style of `TestLosingASwitchEmptiesTheVLANThatOnlyLivedOnIt` (which asserts **by name, not by counting**, because "at least one emptied and at least one reduced" is satisfied by any arrangement that happens to produce both, and the fixture had once quietly stopped demonstrating what its comment claimed):
  - `TestLosingAnAccessPointEmptiesTheSSIDThatOnlyLivedOnIt` — down ap-2, assert `warehouse-scan` remaining == 0 and `corp` remaining == 1, by name.
  - `TestTheFixtureHasAnSSIDOnOneAPAndOneOnSeveral` — no outage needed; assert the fixture can show the difference at all.
- [ ] **Step 5.** `internal/seed/topup.go` — check whether the new phase needs a `topUp` guard (the builder's `topUp` mode makes creators skip what a hydrated database already holds). Follow whatever `vlanMembership` does; if it is unguarded, match it and note why in the phase comment.
- [ ] **Step 6.** `make lint && make test`, then `make dev` and look at `/wireless` and one AP's detail page with human eyes. Commit.

---

## Task 9 — Docs and the tidy-up

- [ ] **Step 1.** `docs/wireless-design.md` — change the status line from DRAFT to reflect that it is built, and correct §4 item 2's "data, no migration" to say what shipped (see Decision B). Do not delete the original phrasing; strike it through, as §7 does for the roadmap entries.
- [ ] **Step 2.** `docs/ROADMAP.md` — mark WP-F1 delivered, and record what it explicitly did **not** do (§5): no point-to-point bridge as a cut target (its dependency is WP-B3's undelivered engine half, named rather than half-built), no observed wireless state, no `link` rows, no authentication profile entity, no channel planning, no AP-model templates.
- [ ] **Step 3.** Delegate `technical-writer` for the user-facing page in `docs/manual` if wireless warrants one — after it works, not before.
- [ ] **Step 4.** Final `make lint && make test`, both engines, foreground, exit status read directly.

---

## Test plan (what proves this works, and at which level)

| Level | What | Where |
|---|---|---|
| Unit (Go, no DB) | `NewWirelessLAN` validation, incl. **nil `psk_ref` is valid for `wpa2_personal`** | `internal/domain/wireless_test.go` |
| Unit (Go, no DB) | fourth structure kind through the shared fixture — emptied, reduced, sort order, detail, href | `internal/impact/structures_test.go` |
| Store, **dual-engine** | the audit fold (§4/9 bullet 1) — move, and add-while-keeping | `internal/store/wireless_test.go` |
| Store, **dual-engine** | `psk_ref` redaction, both directions (§4/9 bullet 2) | `internal/store/wireless_test.go` |
| Store, **dual-engine** | emptied / reduced-to-one / one-of-one-not-reported / same-SSID-two-sites (bullets 3–6) | `internal/store/wireless_test.go` |
| Store, **dual-engine** | the two partial unique indexes, incl. the unscoped pool | `internal/store/wireless_test.go` |
| Store, **dual-engine** | retire refuses while radios broadcast it | `internal/store/wireless_test.go` |
| Store, **dual-engine** | one radio, many SSIDs, no conflict (§2.4 as a test) | `internal/store/wireless_test.go` |
| Standing audits (already exist, must stay green) | `TestEveryColumnIsClassified`, `TestEveryConnectiveTableIsAccountedForInTheImpactGraph`, `TestTheOnlyFactDeletingStatementIsThePrune`, `TestTheScopeClassificationCoversEveryAuditedEntityType`, `TestOnlyTheNamedFunctionsMintAPermit`, `TestEveryVocabularyTermHasADescription`, `TestGoConstantsExistInEveryVocabulary` | across the tree |
| Functional (web) | detail page renders (`detail_pages_render_test.go` census), RBAC boundary suite picks up the four new write routes automatically | `internal/web` |
| **E2E** | **exactly one flow** via `e2e-tester`: log in → `/wireless` → create an SSID → open it → add a radio → see it in the table → open the change log and see the interface entry naming the SSID. That last hop is the flow worth a browser: it is the only place the fold, the route, the template and the audit are all in one path. **No exhaustive UI coverage.** | `docs/E2E.md` suite |

**Bullet 7 of §4 item 9 ("dual-engine, as everything is") is satisfied by every store test above running under `for _, e := range Engines(t)`**, and is verified by `make test` bringing the Postgres container up itself. It is not a seventh test function. `go test ./...` on its own would report green on half the evidence.

**Deliberate test skips, to be stated in the PR body:** no unit test for the handlers' `HX-Request` branching (it goes through the shared `render.Respond`, which is already covered); no test for the nav entry; no test for the two `emptiedDetail`/`reducedDetail` sentences beyond `TestTheDetailNamesTheKind`'s non-empty assertion.

**Evidence gate — answer this in the PR body before any reviewer is invoked.** *What would be true if this were broken, and what did you run to show it isn't?*
- If the fold were broken: a radio would move between SSIDs and `change_log` would say nothing. Shown by Task 5 step 7's three mutations, each observed red on both engines and restored.
- If the redaction were broken: a Vault path would be in a permanent, widely-readable table. Shown by Task 4 step 7's two mutations.
- If the graph join were broken: losing an AP would report the services on it and say nothing about the SSID it was the only radio of. Shown by Task 6 step 7's rename-to-comment mutation and by four dual-engine simulation tests.
- If the unique index were the single-index form: two identical estate-wide SSIDs would both be accepted. Shown by Task 6 step 9's third sub-test, which fails under that form.
- "CI is green" is not an answer.

---

## Risks

- **The audit fold, and it is the only one that rates as high.** Four prior failures, the most recent in the function this copies. Mitigated by writing the audit struct first, by a test that asserts on diff *content* rather than on entry count, and by three named mutations run to red. If Task 5 step 7 mutation 1 does **not** make the test fail, stop and fix the test before continuing.
- **`wireless_lan` is itself a connective table** (four foreign keys), which is easy to miss because the *set* table looks like the only edge-shaped thing here. Both tables must be reachable by the coverage test's `FROM|JOIN` regex in `graph.go`. Caught by Task 6 step 6; do not silence it with an exemption entry.
- **The vocabulary/`vocabTopics` split** silently produces a blank admin panel. Caught by Task 7 step 7 and by looking at the page.
- **Migration safety on a live client DB:** 00061 is purely additive — three `CREATE TABLE`s, four indexes, and `INSERT`s into two lookup tables. No table rebuild, no `NO TRANSACTION` directive, no column dropped, no data rewritten. It is safe to run against a populated database and safe to roll back, with the one honest caveat recorded in the down migration: `DELETE FROM interface_form_factor` fails if any radio still exists, which is the correct failure rather than a silent orphan.
- **GDPR / data residency:** nothing here holds personal data. `change_log.actor` remains an opaque `app_user.id`; `psk_ref` is a path and is redacted from the audit trail; no SSID field accepts a person's name by design. Wireless client identifiers (MAC addresses of associated devices, RSSI per client) would be personal data — **and F1 records none of them**, which §2.6 already forbids for an unrelated reason. Worth stating in the PR body, because "no observed wireless state" turns out to be a data-protection property as well as a modelling one.
- **Suite time:** every test added here is small. No fixture builds hundreds of rows. `internal/store/engines_test.go` records a suite that crept to 586s and failed a release tag on Go's ten-minute default; keep the wireless fixture at two APs and three SSIDs.
- **Scale sanity:** the estate this targets is thousands of rows. `loadStructures` gains one more full-table join with the graph already loading everything in a fixed number of queries — no cache, no index beyond the two the design specifies, no service boundary. Nothing here justifies more.
