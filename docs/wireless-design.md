<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Wireless LANs — design

**Status: DRAFT 2026-09-07, awaiting challenge.** WP-F1's roadmap entry read:

> Wireless LANs, groups, links between interfaces, authentication attributes.
> Links are reachability edges like cables; a wireless bridge is a single point
> of failure worth simulating.

**Both halves of the second sentence were wrong**, and the dependency line was
worse. §2.1 and §7 record what and why; the entry is corrected.

## 1. What this is for

An estate runs SSIDs. `corp` is on twelve access points across three sites,
`guest` is on all of them and lands in a different VLAN, and `warehouse-scan`
is on four APs in one building and is the only thing a stock terminal can use.

None of that is recordable today. **There is no wireless modelling in this
codebase at all** — no table, no column, no vocabulary entry, no template. The
`interface_form_factor` vocabulary is seeded with nine codes and none is a
radio; `asset_kind` has twelve and none is an access point.

The question this answers is the one asked during an outage: **this AP is down —
what is no longer being broadcast, and where?** And its quieter twin, asked
during a change: *is anything left carrying `warehouse-scan` if I take this
one out?*

## 2. The hard parts

### 2.1 An SSID is a STRUCTURE, not an edge

The roadmap said wireless links are "reachability edges like cables". Cables are
**not** reachability edges. `internal/store/graph_coverage_test.go` excludes
`link` from the impact graph and records why beside the exclusion:

> "a cable is physical inventory and path tracing (WP-B3), not a reachability
> edge. docs/reachability-design.md models reachability at FORWARDER GROUP
> level … because a cable genuinely cannot tell you which way traffic flows, so
> it is declared rather than guessed. Adding link here would be a second,
> disagreeing answer to a question net_attachment already answers."

A radio cannot tell you which way traffic flows either. Every word of that
reason applies unchanged.

**The right precedent is the VLAN, and `00031_vlans.sql` makes the argument
better than this document could:**

> "**INTERFACES ARE WHERE THE EDGE LIVES.** A VLAN with prefixes and no ports is
> still just a record: it says an address range exists and nothing about what
> can talk. `interface_vlan` is the adjacency — two access ports in VLAN 30 are
> in one broadcast domain whether or not anybody drew a cable between them,
> **and that is a fact no cable trace can produce**."

An SSID broadcast by six APs is exactly that fact. Two laptops on `corp` are in
one broadcast domain, and no cable joins them.

So a wireless LAN joins `Structures` — the shape VLANs, FHRP groups and
overlays already use — and **not** `net_uplink`. That is not a shortcut: it is
the only shape that does not contradict a decision already made and written
down.

### 2.2 It is already built, which is the reason to do it this way

`internal/store/graph.go`'s `loadStructures` builds structures from three
parent-plus-member joins. `internal/impact/structures.go`'s `analyseStructures`
already reports the two states worth reporting:

> "Emptied: every asset holding a member is down, so the structure is declared
> and carries nothing. Reduced to one: it survived and will not survive the next
> failure … A structure that had one member and still has one is NOT reported.
> It was already a single point of failure before this outage and saying so here
> would answer a question nobody asked — the redundancy page says it,
> permanently and without needing a simulation."

**That last sentence is a pre-existing answer to F1's second half.** The roadmap
wanted a wireless bridge simulated as a single point of failure. A simulation
answers *what breaks if this fails*; for a thing that is already the only path,
nothing needs simulating and this codebase decided so deliberately. An SSID on
one AP is reported permanently, not on request.

So the impact work here is **one join**, and the analysis is free.

### 2.3 A radio is an `interface`, and no new port model

`interface.form_factor` is a foreign key into `interface_form_factor`, an
editable vocabulary. `internal/domain/network.go` is explicit about what that
means:

> "It is NOT the permitted set … so 400G optics land as an INSERT and appear in
> this slice never … **Nothing branches on a form factor** — it is rendered and
> passed through — which is exactly why this vocabulary became a table."

And `00028_pass_through.sql` already made this exact move for panel ports:

> "**ONE TABLE, AND NO NEW PORT MODEL.** A panel's ports are ordinary
> `interface` rows — form_factor is already an editable vocabulary, so `lc` and
> `sc` are data rather than a migration."

**A radio is an ordinary `interface` row** with a new form-factor code. That is
a seed row, not a migration, and it inherits MAC, enabled, `is_mgmt`, the LAG
column it will not use, and every existing test.

**One consequence, and it is benign.** `handlers/neighbourhood.go`'s
`virtualForms` maps three form factors to "virtual" for diagram colouring, and
a radio is not in it, so a radio link draws as physical. That is **correct**
rather than correct-by-accident: `virtualForms` means *inside one box* — veth,
LAG, loopback — and a radio path is between boxes and breaks when either box
does. Noted because a reader will wonder.

*A latent issue, not this work's to fix:* `virtualForms` is a hardcoded Go map
over a vocabulary table, which is the thing `00004_vocabulary_lookups.sql`'s own
"TWO CAVEATS, FLAGGED NOT RESOLVED" warns about — a value added by INSERT gets a
behaviour nobody chose. Adding `radio` makes it one entry larger and no worse.

### 2.4 A wireless association is NOT a `link` row

The roadmap said "links between interfaces", and `link` is the wrong table:

```sql
SELECT COUNT(*) FROM link
WHERE lifecycle = 'active'
  AND (a_interface_id IN (?, ?) OR b_interface_id IN (?, ?))
```

> "A port has one active cable. Catching the second one here gives a usable
> error instead of a silently duplicated topology."

**One AP radio serves many clients.** That rule is right for a cable and wrong
for a radio, and `CreateLink` would refuse the second association with
`ErrConflict`. `medium` and `length_m` are cable vocabulary with no validation
to stop them being repurposed, and `link` carries no `created_at`,
`updated_at` or `row_version` to hang anything on.

**So F1 writes no `link` rows.** The adjacency is which radio broadcasts which
SSID, exactly as `interface_vlan` is a VLAN's adjacency.

A point-to-point wireless **bridge** genuinely is link-shaped — two radios, one
path, one direction of traffic to declare. That is a different thing from an
SSID and it is deferred; see §5.

### 2.5 The PSK must never reach this database

`identity.secret_ref` is the precedent and its rule is absolute
(`00003_services.sql`):

> "A Vault path or similar. **NEVER the secret itself.** If a code path would
> put an actual secret here, that is a bug to raise, not to work around."

`docs/AUDIT.md` rule 12 adds the audit half — *"Record **that** `secret_ref`
changed, never what to. A path is not a secret, but a complete map of secret
paths readable by every account is a reconnaissance gift"* — and the
certificate rule generalises to a passphrase word for word:

> "the private key itself must never reach this database, and **a column that
> accepts certificate-shaped text is where a key eventually gets pasted**."

A `passphrase TEXT` column is that column. This design has **no** field that
accepts a key, only a reference to one.

### 2.6 Declared and observed collide harder here than anywhere

An SSID, its security mode, its VLAN mapping and a **planned** channel are
declared: somebody decided them. A radio's *operating* channel, its transmit
power as reported, RSSI and associated-client counts are observed — high-churn
telemetry nobody decided.

`docs/AUDIT.md` has answered this shape five times (VLANs, FHRP, L2VPN,
circuits, certificates) and always identically: *the disagreement between the
declared and the observed value is the finding, and collapsing them destroys
it.* The sharpest warning is `power_input.draw_va`:

> "**it looks like a measurement and is not** … A measured draw arriving from a
> PDU would be observed state with a reporter, an age and a transition rule — a
> different contract, and **a new column beside this one rather than a
> reinterpretation of it**."

**F1 declares only.** No observed wireless column, no channel-in-use, no client
count. When those arrive they arrive beside these, through
`internal/store/observed.go`, under rules 1–9 — never as a reinterpretation of
a declared field.

## 3. Decisions

### D1. A wireless LAN is a `Structure` — **decided**

§2.1 and §2.2. No new edge type, no `net_uplink` row, no change to
`impact.Request`.

### D2. Radios are `interface` rows with new form-factor codes — **decided**

Seeded vocabulary rows, not a migration: `radio_2g4`, `radio_5g`, `radio_6g`.
Three rather than one because an AP has separate radios per band that fail and
are disabled independently, and because "which band is `guest` on here" is a
question the estate asks. §2.3.

### D3. Security mode is a DOMAIN VOCABULARY, not a behavioural enum — **decided**

`00004_vocabulary_lookups.sql` draws the line: behavioural enums are "read by Go
to select a code path" and keep `TEXT` + `CHECK` so a new value requires a
release; a domain vocabulary "only describes the estate" and becomes a table.

**Nothing in F1 branches on the security mode** — it is recorded and rendered.
So it is a lookup table, `wpa4` arrives as an INSERT, and no release is needed.

**If a later work package wants a finding like "this SSID is open", that branch
must be a behaviour column on the lookup row** — the `asset_kind.can_host_instances`
and `environment_role.is_transit` pattern — and never a hardcoded set in Go.
Writing that down here is the whole point of the 00004 caveat.

### D4. A PSK is a reference, never a value — **decided**

`psk_ref` holds a path, joins `domain.RedactedFields` beside `secret_ref` and
`key_ref`, and the audit records *that* it changed and never what to. There is
no passphrase column and there will not be one. §2.5.

### D5. Enterprise authentication is a `dependency`, not a new table — **decided**

An 802.1X SSID authenticates against a RADIUS service. This estate already
models "this thing needs that service" as `dependency`, with a data
classification, an auth method and a two-ended permit. Inventing a
`wireless_auth_profile` table would be a second, narrower answer to a question
`dependency` answers generally.

So an enterprise WLAN declares a dependency on the RADIUS `service` like
anything else does, and F1 adds no authentication entity at all.

### D6. Scope is an asset, reusing the VLAN trick — **decided**

`wireless_lan.scope_asset_id` is a nullable reference to `asset`, and NULL means
estate-wide. `00031_vlans.sql` explains why this needs no polymorphism:

> "**THE SCOPE IS AN ASSET, WHICH IS THE WHOLE TRICK.** NetBox needs a
> polymorphic scope here … because those are five different tables over there.
> In this schema a site IS an asset, a rack IS an asset and a cluster IS an
> asset, so one nullable reference covers every case with no type column, no
> polymorphism, and no query that has to branch on which kind of parent it
> found."

An SSID scoped to a site says *this is the Oslo `guest`*, and two estates
reusing one SSID name are two rows.

### D7. `interface_wlan` is a SET TABLE, replaced wholesale — **decided**

Exactly `interface_vlan`'s contract:

> "A SET TABLE, replaced wholesale with its interface, like `asset_environment`:
> the membership belongs to the port and has no life of its own … The CLAUDE.md
> rule applies — the parent's `change_log` entry must record the change, or a
> port moving from VLAN 10 to VLAN 20 produces no diff at all."

The set folds into the interface's audited value. **This has gone wrong three
times in this codebase already** — CLAUDE.md records that a set replacement
produced no audit entry at all on three separate occasions, twice for rows that
decide audit scope — so the fold is a build item with its own test, not a
detail.

## 4. What gets built

1. **Migration** (paired sqlite/postgres, or `shared/`): `wireless_lan` —
   `id`, `name`, `ssid`, `security` → `wireless_security(code)`,
   `scope_asset_id` → `asset(id)` nullable, `vlan_id` → `vlan(id)` nullable,
   `psk_ref`, `notes`, `lifecycle`, `created_at`, `updated_at`, `row_version`.
   Plus `wireless_security` seeded with `open`, `wpa2_personal`,
   `wpa2_enterprise`, `wpa3_personal`, `wpa3_enterprise`, `wpa3_transition`.
   Plus `interface_wlan` — `(interface_id, wireless_lan_id)` primary key.
   **`ssid` is not unique**: the same SSID at two sites is two rows, which is
   the point of D6.
2. **Three form-factor seed rows** (D2) — data, no migration.
3. **`domain.WirelessLAN`** with a constructor that validates, and `psk_ref`
   added to `domain.RedactedFields` (D4).
4. **Column classification** — every new column in `docs/AUDIT.md`'s table
   **and** `internal/domain/classification.go`. All declared (§2.6).
   `TestEveryColumnIsClassified` fails the build otherwise, in both directions.
5. **Store**: CRUD, soft-retire, and `SetInterfaceWLANs` following
   `SetInterfaceVLANs` and `SetClusterMembers` — wholesale replace inside the
   parent's transaction, folded into the parent's audited value (D7).
6. **`loadStructures` gains a fourth join**, `wireless_lan` via
   `interface_wlan` → the asset holding each radio. This is also what keeps
   `TestEveryConnectiveTableIsAccountedForInTheImpactGraph` green:
   `interface_wlan` has two foreign keys, so it must either be joined in
   `graph.go` or listed with a reason. It is joined.
7. **UI**: a wireless LAN list and detail, the SSID's radios, and a radios
   panel on an asset. `CanWrite` gated like every other topology surface.
8. **Seed**: an estate with two APs carrying `corp` and `guest`, and one AP
   carrying a third SSID alone — so the *reduced to one* finding has something
   to find and the demo shows both states.
9. **Tests**, and the first two are the ones that would otherwise rot:
   - moving a radio between SSIDs produces a **`change_log` diff on the
     interface** (D7 — the failure this codebase has had three times)
   - `psk_ref` is redacted in both `snapshotJSON` and `diffJSON`, and a rotation
     is still **visible as having happened** — `TestSnapshotRedactsSecretRef`
     asserts both directions and this must too
   - an outage downing every AP carrying an SSID reports it **emptied**
   - an outage leaving one AP reports **reduced to one**
   - an SSID that had one AP and still has one is **not** reported (§2.2)
   - the same SSID name scoped to two sites is two structures, not one
   - dual-engine, as everything is

## 5. What this explicitly does not do

- **No point-to-point wireless bridge as a cut target.** It is genuinely
  link-shaped and genuinely a single path, and making it simulatable needs
  `impact.Request` to accept a cut edge that is not a circuit — which is
  precisely **WP-B3's undelivered engine half**, recorded in `docs/ROADMAP.md`.
  F1 does not close that, and a bridge modelled as an SSID would be a lie.
  Deferred with its dependency named rather than half-built.
- **No observed wireless state.** No RSSI, no channel-in-use, no client counts,
  no band steering. §2.6: those arrive beside the declared columns, never as a
  reinterpretation of them.
- **No `link` rows.** §2.4.
- **No authentication profile entity.** D5 — that is a `dependency`.
- **No channel planning, no RF survey, no coverage map.** An inventory records
  what somebody declared; a coverage map is a measurement and a different
  product.
- **No AP-model templates.** F1's dependency was "A3 light", A3 is not built
  (§7), and the roadmap's own appendix says to build concretely and extract on
  the second caller. Three radios per AP is a seed detail here, not a mechanism.

## 6. For the challenge round

Attack these specifically:

1. **Is `Structure` actually right**, or does an SSID want to be an edge after
   all? The argument rests on `interface_vlan`'s "a fact no cable trace can
   produce". Say if that transfers less well than it looks.
2. **D2's three radio form factors.** One code or three? Three assumes per-band
   radios fail independently, which is true of hardware and may be more
   precision than the estate records.
3. **D3 — is security mode really not behavioural?** If a reviewer can name a
   plausible near-term finding that must branch on it, the 00004 rule says it
   should be `TEXT` + `CHECK` now, because that decision is expensive to
   reverse under live data.
4. **D5 — is an enterprise SSID's RADIUS relationship really a `dependency`?**
   Check `dependency`'s actual columns and permit shape before agreeing.
5. **D7's audit fold.** This has failed three times here. Is the build item
   specific enough to not fail a fourth?
6. **Does `loadStructures` joining `interface_wlan` genuinely satisfy the graph
   coverage test**, or does the test want something else?
7. Anything unmentioned that will bite — particularly anything where this
   reasons correctly and then stops one step short of a consequence, which is
   how every spec in this project has failed so far.

## 7. Noted: this work package's dependency does not exist

F1's entry read **"depends: A3 light"**. That phrase appears exactly once in the
repository — in that line — with no definition anywhere and no recorded decision
behind it.

**And WP-A3 is not built.** There is no component-template table on either
engine, no module, module-bay or inventory-item table, and no identifier in the
tree matching `instantiat` outside roadmap prose. WP-C1 is the same one layer
down: it delivered the catalogue and serial tracking and none of the component
templates or modules its entry promises, including the "module types" in its own
title. Both entries are corrected in `docs/ROADMAP.md`, struck through rather
than deleted.

Neither correction schedules work, and F1 is not blocked. The roadmap's own
appendix already argued A3 should not be built up front — *"extract a shared
mechanism when the second caller appears, not before … a generic templating
mechanism designed before WP-C1 and WP-B1 both exist will be wrong in a way that
is expensive to unpick"* — so building concretely here is following the recorded
advice, not working around a gap.
