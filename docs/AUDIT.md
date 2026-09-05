# Audit rules: declared, observed, provenance

Normative detail behind the "Declared vs observed" section of `CLAUDE.md`. Read this
before writing any code that accepts input from a monitoring system, a discovery
reconciler, or any other non-human writer.

This exists because the original rule -- *"every mutation writes a `change_log` row,
no exceptions"* -- defeats itself once telemetry arrives. A 30-second health poll
produces roughly 2,880 rows per asset per day: ~60k/day for the demo estate, ~1.4M/day
for a 500-device one. The configuration change that caused an incident becomes
unfindable underneath. Splitting declared from observed *restores* the property the
original rule was protecting.

Every rule below was written against this codebase specifically and several cite live
defects found while drafting it. Where a rule looks arbitrary, the reason is given.

---

## The three kinds of fact

`HANDOVER.md` §3.5 says declared data is never silently overwritten by a discovery agent. That has a consequence the original rules did not spell out: these are different kinds of fact with different audit obligations, because they answer different questions.

**Declared** — what somebody asserts should be true: an asset exists, this service depends on that endpoint, this environment is in scope, this thing is retired, a human verified this edge. Configuration and intent. Changes rarely, always because a person decided. Every change is permanent record.

**Observed** — what the estate reports about itself: reachable, running, last seen at 09:41. Telemetry. Changes constantly, nobody decided it, most reports repeat the last one.

**Provenance** — `source` and `confidence`: not a fact about the world, a claim about *where a fact came from*. Governed separately because laundering provenance is how a fabricated fact becomes an authoritative one.

## Column classification — normative

Naming is a hint, not the rule. `desired_state` contains "state" and is declared; `verified_at` is a timestamp and is declared. Guess and you will be wrong. This table is mirrored in `domain.ObservedColumns` / `domain.ProvenanceColumns`; everything else is declared.

| Column | Class | Why |
|---|---|---|
| `asset_health.*` | observed | new table, migration `00006` |
| `observed_transition.*` | observed | new table, migration `00006` |
| `service_instance.observed_state`, `.observed_at` | observed | **moving to `asset_health` in `00006`** — see rule 1 |
| `service_instance.source` | provenance | |
| `service_instance.desired_state` | **declared** | intent. "Observed stopped, therefore desired stopped" is the intent-collapse that makes drift undetectable |
| `dependency.first_seen`, `.last_seen`, `.confidence` | observed | stay on the row — §3.5 makes them part of the fact, and a reconciler writes them at reconciler frequency, not poll frequency |
| `dependency.source` | provenance | decides whether the edge is authoritative |
| `dependency.verified_by`, `.verified_at` | **declared** | a *person's* attestation that an edge is legitimate. A machine credential may never write these — that is a rubber stamp on an undocumented `chd` edge and the firewall rule justified by it |
| `dependency.lifecycle`, `.nature`, `.identity_id`, `.firewall_rule_ref` | declared | |
| `app_user.last_login_at` | observed | see rule 11 |
| `app_user.role`, `.can_see_costs` | **declared** | WP-G1, migration `00058`. A role is a person's assignment of another person, and a cost-visibility grant is the same kind of decision — neither is telemetry about the estate and neither is a claim about where a fact came from, so this is declared rather than provenance despite `source` sitting on the same row. `role` defaults to `observer`: every existing account, and every account an unauthenticated LDAP first-login upserts before an administrator ever looks at it, becomes a reader rather than silently inheriting write access the way `INV_ADMIN_USERS` never expired anybody out of. `can_see_costs` defaults to `FALSE` and is read for both `observer` and `project_owner` — seeing what something costs is a separate grant from being able to change it. **Administrator never consults this column**: `Authorizer.CanSeeCosts` (Task 4) grants an administrator implicit visibility in code, so there is exactly one column and exactly one place deciding the grant, rather than a role-specific exception duplicated between the schema and the authorizer that could disagree with each other. |
| `asset_kind.*`, `service_kind.*`, `interface_form_factor.*`, `environment_role.*`, `ip_address_role.*`, `data_class.*`, `container_engine.*` | **declared** | the seven domain vocabularies (migration `00004`). A lookup row is somebody asserting that a kind of thing exists in this estate — nothing observes it, nothing reports it, and it changes only because a person decided. Not exempt: `ExemptTables` is for tables carrying no inventory fact, and a vocabulary is the authority the entity columns' foreign keys point at. Values arrive in the migration and no migration writes `change_log`; `/vocabularies` now adds and rewords them, and every one of those writes takes a `change_log` row like any other declared mutation |
| `project.*`, `project_asset.*`, `project_service.*`, `project_circuit.*` | **declared** | who owns what. Every row is somebody asserting that a thing belongs to a project, or that a project leans on somebody else's; nothing observes it and nothing derives it. The implied footprint — what is inside an owned asset and what runs on it — is computed at READ time and stored nowhere, deliberately: a derived fact written into this table would be indistinguishable from a declared one in `change_log`, which is the laundering rule 7 exists to prevent |
| `asset.eol_date`, `service.eol_date` | **declared** | when it stops being supportable. It reads like a fact about the world, which is the trap this table exists for: somebody read a contract and typed it. Nothing observes it, nothing derives it, and a monitoring credential may never write it — a machine that could set an EOL date could quietly age out any asset in the estate. Its passing is inert: it changes what a report says and nothing else, never `lifecycle`, per rule 3 |
| `asset_cost.*`, `service_cost.*`, `project_cost.*`, `cost_kind.*` | **declared** | somebody read an invoice and typed it. Nothing observes a price and nothing derives one. The ROLLUPS — what a project costs, what sits on its estate at somebody else's expense — are derived at READ time and stored nowhere, for the same reason the footprint is: a rolled-up total written back would be indistinguishable from a declared one in `change_log`, and it would go stale the moment a VM moved or a contract renewed. A cost line has its own id and lifecycle, so it is audited like a `dependency` rather than folded into its parent's diff |
| `team.*`, `responsibility_role.*`, `asset.team_id`, `asset.manager_role`, `service.team_id`, `service.manager_role`, `project.team_id`, `identity.team_id` | **declared** | who looks after a thing, and in what capacity. Somebody decided it; nothing observes it and nothing derives it. Two things follow. **`manager_role` is documentation, not authorization** — "who can manage it" means who to call, no code path branches on its value, and `authz.CanWrite` does not consult it; when LDAP group roles arrive this is where they will look, which is a reason to record it accurately and none at all to enforce it now. And **`team.contact_ref` holds a GROUP address, a queue or a channel, never an individual** — the application cannot tell the difference and does not try, so the rule lives here, in the field hint and in the help text, exactly as `identity.secret_ref` is documented to hold a path and never a secret. A CMDB kept forever with an append-only `change_log` must carry nothing anybody could ask to have erased, which is the same argument that made `change_log.actor` an opaque id |
| `certificate.*`, `certificate_san.*`, `certificate_asset.*`, `certificate_service.*` | **declared** | somebody asserts that this certificate, with this expiry, is deployed here. What a scanner finds actually being served on :443 is a DIFFERENT fact and belongs on the observed side — the disagreement between the two is the finding, and collapsing them would destroy it. `key_ref` holds a path to key material and is redacted from `change_log` like `secret_ref`; the private key itself must never reach this database, and the public certificate body is not stored either — a column that accepts certificate-shaped text is where a key eventually gets pasted. The SAN set is replaced wholesale like `asset_environment`, and `certificateAudit` folds it into the audited value so a change to what a certificate covers cannot produce an empty diff |
| `manufacturer.*`, `device_type.*`, `asset.device_type_id` | **declared** | the hardware catalogue, migration `00022`. Nobody observes when a model stops being supportable — which is precisely why a person has to write it down. The one worth reading twice is `device_type.eol_date`: it holds what the MANUFACTURER published, transcribed by a person, and `asset.eol_date` **overrides** it when set. The direction is deliberate and it is not "whichever date is later" — a private support contract can carry one box years past what its model promises, and a second-hand unit can fall short of it, so the more SPECIFIC assertion wins over the more general fact. That makes the resolved date only half the answer, so every view renders its **source** beside it (`domain.EOLFromAsset` / `EOLFromDeviceType`) the same way every view of `actor` renders `actor_kind`. `manufacturer.support_ref` is a portal or a contract reference and is governed by the `team.contact_ref` rule: never a person, never a credential |
| `power_source.*`, `power_panel.*`, `power_feed.*`, `power_input.*` | **declared** | the power chain, migration `00023`. The one worth stating is `power_input.draw_va`: it looks like a measurement and is not. It is **the whole nameplate load of this asset, not a share of it and not what it passes on to something downstream** (D7), in volt-amps, typed by a person, and nothing in the estate reports it. A measured draw arriving from a PDU would be observed state with a reporter, an age and a transition rule — a different contract (rules 1 and 3), and a new column beside this one rather than a reinterpretation of it. Ratings are nullable so "not recorded" stays distinguishable from zero; an unrated feed is counted as unrated, never reported as over-allocated. `power_source.kind` is behavioural, not descriptive: it decides whether two inputs converging there is a fault (a UPS or transfer switch, which fail together) or the design (a generator, which is what makes a utility failure survivable) |
| `vrf.*`, `prefix.vrf_id` | **declared** | address-space separation, migration `00029`. Somebody decided that this space is distinct from that one; nothing observes a VRF. A router can report which table it consulted, but that is a reading of a decision already recorded here, never the source of one. `vrf_id IS NULL` means the global table and is what every prefix written before this migration means — so the column is nullable by design, not by omission, and the uniqueness it scopes is enforced by **two partial indexes** rather than one composite: `NULL` is distinct from `NULL` on both engines, so `UNIQUE (vrf_id, cidr_text)` alone would enforce nothing at all for the global table and would silently retire the only constraint the table had |
| `ip_range.*` | **declared** | a span somebody set aside, migration `00030`. Nothing observes a reservation: a lease server knows which addresses it has ISSUED, and that is observed state about the addresses — a different fact from the reservation that gave it the range to issue from, and collapsing the two would let a quiet DHCP server retire a pool. Retired soft like every other entity, because the space becoming allocatable again must not erase the record that it once was not |
| `vlan.*`, `vlan_group.*`, `interface_vlan.*`, `prefix.vlan_ref_id` | **declared** | the VLAN model, migrations `00031`/`00036`. A VLAN exists because somebody configured it, a group exists because somebody chose where the numbering applies, and a port is in a VLAN because somebody put it there. A switch can REPORT which VLANs a trunk actually carries — that is observed state about the port, a different fact from the membership declared here, and the disagreement between the two is the finding, exactly as with a certificate declared as deployed versus one found being served. `interface_vlan` is a SET table replaced wholesale with its interface, so the interface's `change_log` entry must fold the membership into its audited value: a port moving from VLAN 10 to VLAN 20 that produces no diff is the fourth instance of the failure this rule already names three times. **`prefix.vlan_ref_id` is the only place a network's VLAN lives.** `00031` added it beside a loose `vlan_id` integer and `00036` dropped that integer, because the pair coexisted for one release and disagreed the moment anybody edited a prefix — the form wrote the integer, the VLAN pages read the reference, and neither page was wrong about its own column |
| `fhrp_group.*`, `fhrp_member.*`, `ip_address.fhrp_group_id` | **declared** | first-hop redundancy, migration `00032`. Somebody configured VRRP on two routers and gave the pair a virtual address. A router REPORTS which member is currently master — that is observed state about the group, a different fact from the membership declared here, and the pair of them is what makes "the VIP is up but only one router is left" sayable. `fhrp_member` is a SET table folded into the group's audited value: a router leaving a redundancy group is exactly the change somebody needs to find afterwards, and it is the fifth set table in this schema to carry that obligation |
| `rir.*`, `aggregate.*`, `asn.*` | **declared** | the registry layer, migration `00033`. A registry delegated a block and somebody wrote it down; nothing observes a delegation. A looking glass can report what is being ADVERTISED, and that is observed state about a route — a different fact from the allocation recorded here, and the disagreement between them (space allocated but not announced, or announced but not recorded) is the finding. `rir.is_private` is a flag rather than a magic name because code branches on it: an unused private aggregate is a tidiness note and an unused RIPE /22 is money |
| `l2vpn.*`, `l2vpn_termination.*` | **declared** | overlays, migration `00034`. Somebody configured a VXLAN and mapped a VLAN into it; nothing observes an overlay. A fabric controller can REPORT which VNIs are programmed on which leaf — observed state about the switch, a different fact from the overlay declared here, and the disagreement is the finding. A termination has its own id and lifecycle, so it is audited like a `dependency` rather than folded into the overlay's diff, and it is retired softly: an overlay that stops reaching a site must not erase the record that it once did |
| `provider.*`, `circuit.*`, `circuit_termination.*`, `circuit_cost.*` | **declared** | circuits, migration `00035`. Somebody signed a contract; nothing observes one. A router REPORTS whether the interface a circuit lands on is up — observed state about the port, a different fact from the contract here, and the pair is what makes "the line is up and the contract lapsed in March" sayable. **`circuit.contract_end` is not an end of support**: nothing stops working on the day, somebody is either renegotiating or being auto-renewed at a rate nobody checked, and it joins the expiry report because the question ("what needs a decision before a date") is the same one. `provider.account_ref` follows the `team.contact_ref` rule — an account reference, never a named person. `circuit_cost` is the fourth cost surface and carries the identical obligation to the other three |
| `cluster.*`, `cluster_member.*` | **declared** | clusters, migration `00037`. Somebody built a cluster and set its HA policy; nothing observes that. A hypervisor REPORTS whether it is in quorum — observed state about the host, a different fact from the policy declared here, and the disagreement ("HA is configured and the cluster is not in quorum") is the finding. **This is the first table the impact engine branches on rather than reports**: `ha_policy` decides whether a failed host's guests are relocated or lost, so a wrong value changes what a simulation concludes rather than only what a page shows. `cluster_member` is a SET table folded into the cluster's audited value — a host leaving moves guests, so a membership change with no diff on the parent alters the engine's answer and cannot be found afterwards |
| `asset.usable_depth_mm`, `.width_mm`, `.max_load_grams`, `device_type.depth_mm`, `.weight_grams`, `.airflow` | **declared** | physical fit, migration `00038`. A tape measure is held by a person and a datasheet is read by one; nothing in the estate reports its own dimensions. Three things are worth reading twice. **`usable_depth_mm` is measured, `width_mm` is external, and only one of them may be derived** — side clearance follows from width because EIA-310 fixes the equipment faceplate at 482.6mm, so a standard pins the missing term; nothing pins where a rear door sits, so depth deriving from an external figure would be the guess `domain/rack.go` already refuses to make for height. **`airflow` NULL is not `front_to_rear`** — it is the overwhelmingly common value, so defaulting to it would let every uncatalogued box pass the opposing-neighbours check in silence and an estate that had declared nothing would report perfect airflow; `passive` is a real declaration and a different thing from unknown. And these columns **warn, they never refuse**: two boxes in one unit is impossible so `CheckPlacement` rejects it, but a 780mm server in a 600mm cabinet is in there with the door open, and refusing the placement would stop it being recorded rather than stop it happening |
| `asset.replaces_asset_id` | **declared** | what this box took over from, migration `00042`. A person asserts a refresh; nothing observes one. A serial number changing is not evidence of lineage, and a reporter that could write this could rewrite the estate's purchasing history — so it is declared, audited, and closed to machine credentials like every other column here. It is why soft delete earns its keep: the predecessor is retired rather than deleted, so it is still there to compare a price against years later |
| `asset.cpu_cores`, `.memory_mb`, `.vcpu_provisioned`, `.vcpu_allocated`, `.memory_provisioned_mb`, `.memory_allocated_mb`, `.storage_kind`, `.raw_capacity_gb`, `asset_storage_claim.*`, `cluster.cpu_overcommit`, `project.priced_for_vcpu`, `.priced_for_memory_mb`, `storage_kind.*` | **declared** | how big things are and who was promised what, migrations `00044`–`00046`. **The whole group is one call and it is not a close one**: a person agrees a VM gets eight cores, an operator decides how far a cluster may oversubscribe, somebody quoted a price on an assumption. A hypervisor reporting thirty percent CPU, or `df` reporting a full filesystem, is telemetry about the same subject and a completely different fact — it arrives through `RecordObservation` and never touches a column here. That separation is why adding eleven columns added no new class of fact. Three things are worth reading twice. **`allocated` is the money column and `provisioned` is the limit** — they routinely differ, and the gap between them is a decision somebody made without pricing it, which is the whole reason both are kept. **`cpu_overcommit` is declared and never inferred from load**, because a quiet cluster would raise its own apparent safe ratio and licence exactly the overcommitment the finding exists to catch. And **`priced_for_*` is not `contracted`** — these contracts name no resources, so exceeding it is a margin signal and never a breach; a column named for a promise nobody made will one day be screenshotted and quoted at a client |
| `inflation_rate.*` | **declared** | what money did, migration `00043`. Reference data from outside the estate: somebody reads a published index and types it. Nothing observes it and **nothing fetches it** — invariant 7 forbids the outbound call, and a rate that arrived on its own would be a figure nobody chose and nobody could date. Corrected in place rather than superseded, unlike a cost line: a cost that changes is two facts, one price until a date and another after it, while a revised index for 2024 was always one figure somebody had wrong |
| `custom_field.*`, `custom_field_option.*`, `custom_field_value.*` | **declared** | estate-specific attributes, migration `00051` (WP-A4). An administrator defines a field once and it appears on every asset or service of that type; every column here is somebody asserting a fact, none is the estate reporting one, and nothing about the feature is telemetry. `custom_field.created_by`/`.retired_by` read like provenance and are not — provenance in this codebase is `source` and `confidence`, a claim about where a fact came from, and custom fields carry neither. These are a person's attestation, the same reasoning that classifies `dependency.verified_by`/`.verified_at` as declared above. `custom_field_value` is a set the field owns, replaced wholesale inside the entity's own transaction like `asset_environment`, and folded into the entity's audited value rather than the field's — the question a reader asks is "what changed about `vm-db-2`", not "what changed about the field". **`custom_field_value.value_text` is operator free text with no fixed vocabulary**, and nothing stops it holding a name, an email or a phone number the moment a field like "Owner email" or "Contact" exists — which is why the `team.*` row's own rule (above, line 49: "a CMDB kept forever with an append-only `change_log` must carry nothing anybody could ask to have erased") needs restating here rather than left to inference. It holds for the log's content, and unconditionally now: `foldCustomValues` (`internal/store/customvalues.go`) folds a **plain change counter** — `code@<n>`, `n` being `custom_field_value.row_version` — into the parent's audited shape instead of the value itself, so a change to a custom value still produces a diff (the property the fold exists for) while the value's text never reaches `change_log`. This replaced an earlier keyed HMAC-SHA256 digest (`code=#<digest>`), which was **pseudonymisation, not anonymisation** (GDPR Art. 4(5), Recital 26) — its key sat in the same database and backup as the log it protected — and was worse than that on inspection: identical values digested identically, and for a `select` or `boolean` field a reader needed no key at all to invert it, since `/changes` is readable by any authenticated user and a `select` field's option list is published on the registry (§4 below). A 48-bit digest collision on one (field, entity) pair could also write **no `change_log` row at all** — `diffJSON` reporting `changed=false` while `row_version` bumped and the value changed, a mutation of declared state with no audit entry, the exact failure this file's rule 7 exists to prevent. A counter has none of this: it is not personal data by any reading, because it carries no information about the value at all; it cannot collide between two values of the same field, because it is monotonic; and there is no key to hold outside the database, rotate correctly, or diverge on a restart. **The cost is unchanged and still accepted, not hidden**: a `change_log` entry shows that a value changed and which field, never what it changed to — the current value still lives in `custom_field_value` and on the entity's own page, only the audit trail's copy of it is gone. **The fix is forward-only, and it now has two boundaries instead of one**: `change_log` is append-only, so an entry written before the digest still holds the plaintext value it was written with, and an entry written under the digest (three days, in this deployment's history) still holds `code=#<digest>` — neither is rewritten, and a reader of an old entry meets a format that no longer exists. Only entries written from this change forward carry `code@<n>`. `custom_field.owner_team_id` (migration `00054`) is declared for the same reason as `team_id` on `asset` and `service`: an administrator assigning a field to a team is an assertion of who to ask, not a measurement of anything. It replaces `created_by` as the feature's answer to "who do I ask" — a senior review found that `CreatedByName` names an INDIVIDUAL, which is the wrong answer to exactly the scenario this feature exists to defend against: the person who defined `cost_centre` leaves the company, and `customfields.go`'s own `created_by`/`retired_by` `LEFT JOIN` comment already documents that a GDPR erasure request against that person leaves the field "readable... without a name to show for it" — the registry's only attribution surface silently goes blank. `team.contact_ref` (line 49 above) is reused rather than inventing a second contact concept, because it is already argued non-personal by construction: "a GROUP address, a ticket queue or a channel... never an individual". `owner_team_id` is nullable in the schema — the eleven fields that predate this migration cannot be given an owner retroactively, and finding and assigning them is separate, out-of-scope follow-up work — but required on every field created or edited through the application from this point on, with no escape hatch (`domain.checkOwnerTeam`). A RETIRED team is not offered as the owner of a NEW field but keeps displaying, marked retired, on a field that already names it — the identical rule already applied to a retired `custom_field_option` above: what is STORED must keep displaying, what is RETIRED must not be newly selectable. |
| `change_log.batch_id` | **declared** | WP-G7 piece 2 (docs/ownership-report-design.md §4/§5), first used by team-retirement reassignment. Groups several `change_log` rows written by ONE bulk operation into one reconstructable set — it does not replace the per-entity rows, which stay one per entity because each entity's ownership change is its own declared-state mutation. `batch_id` carries no fact about the world and no provenance claim of its own; it is written by `tx.log`, the same single writer as every other `change_log` column, on the same person-driven act as the rest of the row, so it inherits that row's class rather than needing a second decision. NULL for the overwhelming majority of rows, which are one edit to one row and need nothing else to explain them |
| `tag.*` | **declared** | piece 1 of WP-G4a (`docs/tags-design.md`), migration `00056`. An administrator names a tag in a registry before it can be applied to anything — the identical explicit-creation shape `custom_field` already follows, and the identical reason `description` is `NOT NULL` and non-empty: an administrator who cannot say why a tag exists is the origin of the support call the feature defends against. `created_by`/`.retired_by` read like provenance and are not, for the same reason `custom_field.created_by`/`.retired_by` are not (line 65 above): provenance in this codebase is `source` and `confidence`, a claim about where a fact came from, and a tag carries neither — these are a person's attestation. **`code` is editable, deliberately, even after the tag has been applied to something (piece 2)**: `docs/tags-design.md` §4 folds an entity's tag SET as the tag's stable **id**, never its code, so that a rename here can never rewrite the fold — and therefore the audited value — of every entity carrying it, the exact hazard `docs/custom-fields-design.md` §7 already documents for a custom field's code and which `TestCodeStaysEditableWithValues` proves holds for `custom_field` today. Applying and removing tags (`entity_tag`, piece 2) is its own set-replacement obligation on the CARRYING entity's audited value, not on `tag` itself, the same way `custom_field_value` folds into the entity rather than the field — this row covers only the tag's own definition, creation, edit, retirement and restoration. |
| `entity_tag.*` | **declared** | applying tags to entities, piece 2 of WP-G4a (`docs/tags-design.md` §3/§4), migration `00057`. `entity_type` and `entity_id` name what somebody tagged, `tag_id` names which tag, and `created_at`/`created_by` are a person's attestation that they applied it — nothing here is the estate reporting on itself, and there is no `source`/`confidence` pair to make any of it provenance. This is a SET the ENTITY owns (not the tag), replaced wholesale inside that entity's own transaction exactly like `custom_field_value`, and folded into `assetAudit.Tags` / `serviceAudit.Tags` / the new `projectAudit.Tags` (`internal/store/entitytags.go`) so the replacement cannot produce an empty diff — the failure this file's rule 7 already names four times over for `asset_environment`, `dependency_data_class` and twice in WP-A4. **The fold is the tag's stable id, sorted, never its code** — `tag.code` stays editable (line 67 above), and folding it would mean a rename rewrites the fold of every entity carrying that tag, manufacturing a spurious diff on the next unrelated save of each; `TestReorderingTagsIsNotAChange` and `TestTheTagFoldIsStableAcrossARename` (`internal/store/entitytags_test.go`) are the tests this row exists for. `entity_id` carries **no foreign key** — it is polymorphic like `journal_entry` and `asset_health`, so nothing at the database level stops a row pointing at nothing; compensated by `entity_type`'s `CHECK` (limiting it to `asset`, `service`, `project`) and by `SQLStore.TagIntegrityViolations`, a store-level integrity scan tested on both engines rather than assumed away. **Tags are never inherited through `asset_closure`** (`docs/tags-design.md` §4b) — an inherited tag is not a fact anybody asserted about the child, so nothing here walks the closure table; "everything tagged `dr` including what a tagged datacentre contains" is a read-time filter (piece 3), storing and asserting nothing. |
| `user_project.*` | **declared** | which projects a person is assigned to, WP-G1 piece 3, migration `00059`. A person grants or releases scope over a project; nothing here is ever written by an observation, and nothing consults these values for authorization yet — `Authorizer.Permit` is Task 12, the gate flip is Task 13. Shaped like `custom_field` rather than `project_asset`: its own `id` and `row_version` rather than a composite key with `lifecycle` toggled on one row, because a released-then-later-re-granted assignment is kept as two distinct rows so "when did the first grant end" stays answerable — the partial unique index on `(user_id, project_id) WHERE lifecycle = 'active'` is what makes the second row possible, the same shape `custom_field`'s live-code index established for the same reason (a total `UNIQUE` would mean an operator who releases somebody could never re-add them). `ReleaseProject` retires the row; nothing here is ever deleted. |
| everything else | declared | |

A migration that adds a column classifies it in this table **and** in `domain`. `TestEveryColumnIsClassified` reads the live schema on both engines and fails the build on an unclassified column — so a new column cannot default into the gap.

## Write-authorization scope classification — normative

The Go mirror of this table is `internal/domain/role.go`'s `entityScope` map,
which `ScopeClassOf` consults from `tx.log` on every declared write — see "Since
WP-G1, this is an authorization document as well as an audit one" above. This
table exists for the same reason the column classification table above does:
naming does not decide the class, a reader looks here first, and a change that
moves an entity between classes without updating this table is exactly the kind
of drift `TestTheWriteScopeTableMatchesEntityScope` (`internal/domain/role_scope_doc_test.go`)
now fails the build on, in both directions.

Four classes, in `ScopeClassOf`'s own order:

- **`ScopeProjectLinked`** — carries a real `project_id` relationship (or
  is the project-link table itself); a project owner may write it for their
  own projects.
- **`ScopeSubjectDerived`** — carries no project relationship of its own, but
  a narrow, per-row store check can resolve its real subject (the asset,
  service or person it belongs to) and mint a permit scoped to that one row.
  Never authorized by project membership directly — always by the row's own
  subject, checked at write time.
- **`ScopeEstateConfig`** — estate-wide configuration; applies to every
  project and is not owned by any one of them. Administrator-only.
- **`ScopeTopology`** — physical and logical structure between projects, or
  an entity explicitly excluded from the project-owner carve-out even where
  a subject-derived check would otherwise be possible. Administrator-only.

### ScopeProjectLinked

The three, and only three, entity types a project actually links to
(`docs/rbac-design.md` §4; migrations `00009`, `00041`).

| Entity type | Notes |
|---|---|
| `asset` | owned via `project_asset` |
| `circuit` | owned via `project_circuit` |
| `service` | owned via `project_service` |

### ScopeSubjectDerived

No project relationship of its own. Authorized by a narrow, per-row store
function — never by `ScopedPermit.projects` directly — that resolves the
row's real subject and mints a permit covering only that row.
`auth.Authorizer.Permit` builds `ScopedEntities` buckets only for `asset`,
`service` and `circuit`, so an ordinary project-owner permit has an empty
bucket for every type below and covers nothing until one of these
per-row functions runs.

| Entity type | Notes |
|---|---|
| `asset_cost` | **WP-1.1 item 3.** Subject is the owning asset — `authorizeCostSubject` (`internal/store/costs.go`) checks it and refuses every other cost table (`service_cost`, `project_cost`, `circuit_cost` stay `ScopeTopology`, deliberately — see the `ScopeTopology` section below). Gated on a **second, independent seam**: `middleware.RequireCostVisibility` also requires the caller's `can_see_costs` grant before the request reaches the store at all, so a project owner who cannot see costs cannot write them either — the store-level scope check alone would let them blind-write a price they are not permitted to read. `domain.Permit` itself was **not widened** to carry a cost dimension: it stays fixed at the three width-locked methods (`TestThePermitInterfaceCannotBeWidenedWithoutSayingSo`), so cost visibility is enforced once, in the request-gating middleware, rather than duplicated into every `Covers` call a permit could ever be asked. |
| `dependency` | **WP-1.1 item 1, two-ended.** Subjects are the two services it connects — the consumer directly, and the provider one hop away (an endpoint's own `service_id`, or a route's frontend endpoint's `service_id`). `authorizeDependencySubjects` (`internal/store/deps.go`) requires **both** ends in the caller's project scope; checking only one would let a project owner point their service at anybody's socket, or attach anybody's service as a consumer of their own. |
| `interface` | subject is the owning asset (`authorizeInterfaceSubject`, `internal/store/network.go`) |
| `ip_address` | subject is the owning asset, one hop through its interface |
| `journal_entry` | subject is the entity the note is attached to |
| `link` | **WP-1.1 item 2, two-ended.** Subjects are the two assets it cables together — an interface carries no project of its own, so a link is two hops from each end (`a_interface_id` → asset, `b_interface_id` → asset). `authorizeLinkSubjects` (`internal/store/network.go`) requires **both** interfaces' owning assets in scope; checking only one would let a project owner cable their own asset to anybody else's port. |
| `saved_view` | subject is the view's own owner (`internal/store/savedviews.go`) — the row a person may always write is their own, regardless of project membership |
| `service_instance` | subject is the owning service (`authorizeInstanceSubjects`) |

### ScopeEstateConfig

Applies to every project and is owned by none of them. Administrator-only,
including `user_project` — the table that decides a project owner's own
scope, so a project owner able to write it could grant themselves every
project and become an Administrator in all but name.

| Entity type | Notes |
|---|---|
| `app_user` | accounts and roles |
| `asset_kind` | vocabulary |
| `container_engine` | vocabulary |
| `cost_kind` | vocabulary |
| `custom_field` | field definitions (values fold into the owning entity's own `change_log` row, not this type) |
| `data_class` | vocabulary |
| `device_type` | catalogue |
| `environment` | catalogue |
| `environment_role` | vocabulary |
| `identity` | credential references |
| `inflation_rate` | reference data |
| `interface_form_factor` | vocabulary |
| `ip_address_role` | vocabulary |
| `manufacturer` | catalogue |
| `observed_transition` | the retention prune's own audit entries — an administrator's maintenance action against the whole estate, not any one project's |
| `project` | a project owner does not own the `project` row itself, only their `user_project` membership — editing a project's own name/description/lifecycle stays Administrator-only |
| `project_asset` | linking an *existing* asset to a project (create-vs-link distinction, `docs/rbac-design.md` §4) |
| `project_circuit` | linking an existing circuit to a project |
| `project_service` | linking an existing service to a project |
| `provider` | catalogue |
| `responsibility_role` | vocabulary |
| `service_kind` | vocabulary |
| `storage_kind` | vocabulary |
| `tag` | tag definitions (`entity_tag` application folds into the tagged entity's own `change_log` row) |
| `team` | teams |
| `unmatched_observation` | telemetry that matched no entity — the prune's own audit entries, same reasoning as `observed_transition` |
| `user_project` | **load-bearing.** Decides a project owner's own scope; only an Administrator grants it |

### ScopeTopology

The default for topology, cross-cutting facts, and anything not yet proven
project-linked — including entities that could theoretically resolve a
subject but are excluded on purpose.

| Entity type | Notes |
|---|---|
| `aggregate` | addressing |
| `asn` | addressing |
| `backend_member` | load-balancing |
| `backend_pool` | load-balancing |
| `certificate` | **considered and rejected for WP-1.1** (`docs/ROADMAP.md`). A certificate is many-to-many with assets and services (`certificate_asset`, `certificate_service`) — it has no single owning subject the way `asset_cost` has one owning asset. "Every member in scope" is **vacuously true for an undeployed certificate** (no members to fail the check against), which would make every unattached certificate writable by every project owner in the estate. Stays Administrator-only until a real subject-resolution rule is designed, not merely "not needed yet". |
| `circuit_cost` | explicitly excluded from the `asset_cost` carve-out — a circuit is already the unit of attribution, and nothing has asked for a project owner to write this |
| `circuit_termination` | topology |
| `cluster` | **considered and rejected for WP-1.1**, same shape as `certificate`. Many-to-many with assets via `cluster_member`, no single owning subject, and "every member in scope" is **vacuously true for an empty cluster** — a cluster with no hosts yet would be writable by every project owner. Stays Administrator-only. |
| `endpoint` | topology |
| `fhrp_group` | topology |
| `health_override` | a person overruling a monitor; estate-wide operational surface |
| `ip_range` | addressing |
| `l2vpn` | overlays |
| `l2vpn_termination` | overlays |
| `net_anchor` | topology |
| `net_attachment` | topology |
| `net_group` | topology |
| `net_group_member` | topology |
| `net_uplink` | topology |
| `port_pass_through` | physical topology |
| `power_feed` | physical topology |
| `power_input` | physical topology |
| `power_panel` | physical topology |
| `power_source` | physical topology |
| `prefix` | addressing |
| `project_cost` | costs attached to the project itself, not to one of its assets — explicitly excluded from the `asset_cost` carve-out |
| `rir` | addressing |
| `route` | topology |
| `rt_container` | runtime topology |
| `rt_k8s` | runtime topology |
| `rt_systemd` | runtime topology |
| `rt_windows` | runtime topology |
| `service_cost` | explicitly excluded from the `asset_cost` carve-out — a service is already the unit of attribution |
| `vlan` | topology |
| `vlan_group` | topology |


## The rules

> **Since WP-G1, this is an authorization document as well as an audit one.**
> Object-level authorization is enforced inside `tx.log` — the permit check
> and the `change_log` insert are the same code path, deliberately, because
> the audit choke point was already the one place every declared mutation had
> to pass through.
>
> The consequence, stated plainly so nobody has to infer it: **"every declared
> mutation writes a `change_log` row in the same transaction" is now an
> authorization invariant, not only an audit rule.** A declared mutation that
> does not log is not merely an untraceable change — it is an *unauthorized
> change that succeeded*. A second `INSERT INTO change_log` anywhere in this
> codebase is a second authorization bypass.
>
> `internal/store/audit_matrix_test.go` is the **positive** proof: it asserts
> that each declared mutation writes exactly one row. Every other boundary
> test in this repo is negative — they assert that forbidden things are
> refused, and a mutation that quietly stopped logging would sail past all of
> them green, because a write that never reaches `tx.log` is never refused by
> it either. That asymmetry is why the positive test exists and why it is not
> redundant with the rest.
>
> If you are relaxing this rule, adding a second logging path, or moving the
> permit check out of `tx.log`, this is the fact you have to meet first.

**1. Separation is structural, and the mechanism is named.** Naming a column `observed_*` is not separation: a mixed row produces a mixed audit entry that no portable query can classify, because the only distinguishing information lands inside `change_log.diff` and querying inside JSON is banned. Migration `00006` moves `service_instance.observed_state`/`observed_at` into `asset_health (entity_type, entity_id, reporter, state, state_since, reported_at, last_report_at, PRIMARY KEY (entity_type, entity_id, reporter))`. Do it before the first webhook ships — the estate is a fixture today, and this is the only cheap moment. `UpdateInstance` currently writes `desired_state` and `observed_state` in one statement from a round-tripped struct, so a stale read silently reverts a concurrent operator edit and `logUpdate` attributes the revert to the human; that is the bug this rule exists to prevent and it is in the tree today. Enforcement is three things, not intent: observed writes live only in `internal/store/observed.go`; the webhook handler's store field is typed `store.ObservedStore` (`RecordObservation`, `GetObservedState`) and never `*store.SQLStore`, so overreach is a compile error; and `TestObservedPathTouchesNoDeclaredTable` parses that file and fails on any write naming a table or column off the observed allowlist. If a boundary can only be described as "must not call X", it is not implemented.

**2. Observed state never becomes intent, and never decides alone.** Reported `down` does not become `lifecycle = 'retired'` — nor `'maintenance'`, nor `'deprecated'`; a missing unit does not delete its `service_instance` nor set `desired_state = 'stopped'`. That is an allowlist, not a blocklist: an observed write may write only the observed columns above. Beyond that, observed state must never be the sole input to an output that recommends or authorises a change to declared state, a firewall rule, a scope determination, or a reboot. **Today it does not feed `internal/impact` at all** (`Instance.Disabled` derives from `desired_state` only) and wiring it in is an architecture decision requiring sign-off, not an implementation detail. If that is ever agreed, every report consuming an observed input labels it as observed with its reporter and age — an operator reading a computed "ok" must be able to see the "ok" was asserted by a credential rather than established by configuration. `dependency.last_seen` is subject to this too: it justifies keeping or withdrawing a firewall rule, so a cleanup report may never act on it unattended.

**3. Audit the transition, not the heartbeat — and never lose the onset.** A *transition* is a change in the `state` column alone. Never a struct diff: `diffJSON` compares every `db`-tagged field except `updated_at`, so routing an observation through `logUpdate` logs every heartbeat via the moving timestamp — the exact unbounded growth this rule exists to prevent. Observed writes call neither `logUpdate` nor `logCreate`; they call `RecordObservation`, which writes to `observed_transition` and only on a state change. Three timestamps, and an implementer may not collapse them:

- `state_since` — server clock, written **only** on transition. This is "down since when", the first question anyone asks at 03:00. Never touched by a repeat report.
- `last_report_at` — server clock, every report. Drives staleness, ordering, monotonicity and retention.
- `reported_at` — the reporter's own clock, **display only**. A caller must never write the field that decides how fresh its own data looks.

The first observation (prior value NULL) **is** a transition and is logged; it is the entry an incident reviewer looks for. Observed writes do not bump `updated_at` (that column means "when a person last changed this row") and do not call `indexEntity` — reindexing on every heartbeat rewrites the FTS table thousands of times a day against a single SQLite writer.

**4. Observed writes are idempotent, ordered, and off the write path when they change nothing.** Identity is `(entity_type, entity_id, reporter)`; `reporter` is in the key because two monitors watching one asset must not overwrite each other into a permanent flap. A repeated `observation_id` is a no-op. A report whose `reported_at` is not newer than the stored one is **discarded, not applied** — a retry is newer or identical, a replay is older, and last-write-wins on arrival order turns a delayed `down` landing after `up` into two transitions that never happened, three minutes late, pointing an incident review at the wrong cause. Reject `reported_at` more than 300s ahead of server time (a broken or hostile clock otherwise poisons ordering for everything it touches, and RFC3339 TEXT sorts lexicographically, so a future date pins to the top of every `ORDER BY`). A report that changes nothing must not open a write transaction on the request path: buffer no-op `last_report_at` bumps and flush on a 30s interval. On SQLite the writer pool is one connection — a design where heartbeats can occupy it makes "retire this asset" time out at exactly the moment it matters. Transitions and declared mutations write through immediately.

**5. Attribution is server-assigned and structurally unforgeable.** `change_log.actor` is free TEXT, so a caller-supplied name forges a human's audit entry. The actor for a non-user write is derived by the server from the authenticated credential and is **never** read from the request body or a header. Namespace it `monitor:<credential-id>` via `domain.AgentActor(id)` — never a struct literal, or a typo becomes a `CHECK` failure inside a webhook at 03:00. Startup fails if an agent name collides with an `app_user.username`. Every template that renders `actor` renders `actor_kind` beside it: `changes.html` does, `dashboard.html:90`, `asset_detail.html:143` and `service_detail.html:201` do not, and until they do, "tell a monitor from a human at a glance" is false in three views out of four — including the two an incident review actually opens.

**A read-only (WP-A2) credential is deliberately not held to the collision check above.** `buildAgentSurface` calls `st.UsernamesMatching` against every monitoring credential id before the process starts serving; `buildReaderSurface` does not, and that is not an oversight. The collision rule exists because `monitor:<id>` reaches `change_log.actor` — a colliding id would let a monitoring credential's writes be misread as a named person's, or the reverse. A `Reader` (`internal/auth/reader.go`) carries no `domain.Actor` at all: it never writes anything, so there is no audit row for a colliding id to misattribute. The only place a reader credential id appears in any log is a security event line, `credential=<id>` (`auth.EventReaderScopeDenied` and its siblings) — the same shape this codebase already sanctions for agent ids in the same lines, and one `change_log` never touches. If a reader is ever given a write path, this exemption ends with it and the id joins the same startup check agents already pass.

**6. Authorization is separate, and separation means a different principal type.** A monitoring credential is not an `app_user` (whose `source` CHECK has no room for one anyway), never appears in `INV_ADMIN_USERS`, and never reaches `authz.CanWrite` — adding it there is the path of least resistance and it grants every write route in `routes.go`, including `POST /assets/{id}/retire` and `POST /dependencies/{id}/verify`. It authenticates by bearer token from `INV_AGENT_TOKENS` (`id:token`, comma-separated, compared with `subtle.ConstantTimeCompare`), on one route mounted under `middleware.RequireAgent` only: no session, no `RequireWrite`, and a CSRF exemption registered for **that exact path** — never a prefix or glob, or the planned `/api/inventory` inherits it for free. The route rejects a request carrying a session cookie, caps body size and batch size, and rate-limits per credential; a breach is logged as a security event, not silently dropped. A credential is scoped to an explicit set of environments — a dev collector's token must not be able to write health for an `in_scope` environment — and an out-of-scope report is 403 and recorded. No handler reachable by an agent principal returns an `identity` row or anything derived from `secret_ref`. An observation for an unknown entity is **404, never created**: `INSERT ... ON CONFLICT` is the natural verb and it turns a deliberately narrow token into an inventory-write vector. Unmatched reports go to an `unmatched_observation` queue and surface as drift — an asset the estate has and the inventory does not is a finding, not noise.

**7. No `agent` actor may write the provenance value `declared`.** *(Amended 2026-07-28. This rule originally read "only a `user` actor may write `declared`", which contradicts rule 10's own statement that `SystemActor` seeds the declared inventory and that `UpsertLDAPUser` creates accounts as `Kind:"system"`. Both cannot hold once the check is enforced at the store entry points, and the strict reading was verified to fail the seed outright. The threat is a credential that arrives over the network and asserts more authority than it was issued; `SystemActor` is this process and is not reachable from outside, so denying it closes nothing. `domain.CheckProvenanceWrite` denies `agent`.)* `service_instance.source` is unconstrained TEXT today, unlike `dependency.source`; `00006` gives it a matching `CHECK` and Go constant set. A machine credential may set only discovered-subset values. No machine may assert that a fact was hand-declared — that laundering is how a fabricated workload inside an `in_scope` environment renders to an operator as hand-asserted fact and never reaches the conflict queue. Flipping an edge between `declared` and `discovered_*` is an operator act with a `change_log` row. `confidence` is likewise set by the store from the credential, never from the payload: self-attested confidence is not a control.

**8. Silence is not health, and staleness is computed at read time.** Each reporter declares an interval; a value older than 3× it renders as `unknown (stale since T)` in every view and export, never as its last value. Do not invent transitions with a background job — compute from `last_report_at` on read. An intruder's first act is killing the collector, and under transition-only logging a dead collector and a healthy estate are otherwise the same picture, forever. A reporter that stops entirely is one alertable event, not a thousand entities quietly going green.

**9. Flapping is compressed, never suppressed — the timing is the diagnosis.** Above `domain.FlapThreshold` (5) transitions in `domain.FlapWindow` (5 min), per `(entity, reporter)`, stop emitting one row per oscillation and open a flap episode. Constants in `internal/domain`, never env, never a per-row column, and never writable by the reporter — a writer that can raise its own suppression threshold has a mute button. `flap_open` and `flap_close` rows are **unconditional**, and `flap_close` carries the count, window, first/last values and the settled value, so a reader always sees that suppression happened and how much it hid. A transition to a value **not already seen in the window** is always its own row — a novel value is by definition not part of the oscillation being compressed. That is what stops a stolen token from deliberately tripping the floor so the real `down` five minutes later reads as a monitoring artefact. The current value and `state_since` keep tracking throughout: "flapping" is displayed *alongside* the state, never instead of it, or a box that died twenty minutes ago still shows as flapping.

**10. Retention differs, and the separation is by table, not by predicate.** `change_log` is append-only: no `UPDATE change_log`, no `DELETE FROM change_log`, anywhere, ever. Correcting a wrong entry means writing a new one. `observed_transition` and `unmatched_observation` are the only prunable tables, and the only ones the prune job can reach — its store methods take no table name. Both are telemetry: a transition the estate reported about itself, and a report that matched no entity. Neither is a fact about the estate, which is what makes age-based removal retention rather than deletion. No third table joins them without the reasoning rule 16 sets out. **Never prune on `actor_kind`.** That column says *who wrote a row*, not *what kind of fact it is*, and this repo already writes declared state under non-user kinds: `UpsertLDAPUser` creates accounts as `Kind:"system"`, `SystemActor` seeds the declared inventory, and post-M5 discovery agents create `dependency` rows — including `chd`-carrying edges — as `agent`. `DELETE FROM change_log WHERE actor_kind='agent'` destroys exactly the scope-validation evidence the tool exists to produce, silently. Pruning is an admin-invoked `cmd/invctl` subcommand, never handler code, never an automatic side effect of a write path, and never below 365 days for an entity resolving to an environment with `in_scope = TRUE`. Each run writes one `change_log` row (`entity_type='observed_transition'`, `action='delete'` — the existing CHECK allows `delete` and does not allow `prune`, and altering a CHECK in SQLite means rebuilding the table) recording window, row count and actor. This prune was the only `DELETE FROM` permitted in this codebase until rule 16 named a second, narrower one; the soft-delete rule in `CLAUDE.md` has been amended to say so.

**11. The unaudited-write exception list is closed.** `app_user.last_login_at`, written by `TouchLogin`, is the one pre-existing unaudited write. It is hereby classified **observed** — a login is telemetry about a person, not a configuration change — and is exempt from rules 3–9 (no reporter, no transitions). It is the only write permitted to bypass `SQLStore.write`. `TouchLogin` is not a template: a code comment explaining why a write is unaudited is not authorization. Adding a second requires editing this file first. Separately, authentication success and failure, authorization denial and agent-token rejection are **security events**, never heartbeats, and are never subject to transition-only logging.

**12. Secret references never enter the audit trail.** `snapshotJSON` serialises every `db`-tagged field, so `CreateIdentity`'s `logCreate` writes `identity.secret_ref` — the full Vault path inventory — into `change_log.diff`, which renders to every authenticated reader including read-only ones, and stays forever. **This is a live defect, not a hypothetical.** Maintain `domain.RedactedFields` and apply it in both `snapshotJSON` and `diffJSON`, the way `CreateUser` already redacts `password_hash` ("an audit trail is read by more people than the user table is" — same reasoning, not applied here). Record *that* `secret_ref` changed, never what to. A path is not a secret, but a complete map of secret paths readable by every account is a reconnaissance gift.

**13. Observed state is never indexed and never free text.** `search_index` is built from declared columns only; filtering by health is a query against the entity table, not a search. Otherwise a decoy row carrying a real hostname surfaces to a responder pasting that hostname mid-incident. `state` is an enum — `CHECK (state IN ('up','degraded','down','unknown'))` plus a Go constant set, like every other enum here. Vendor vocabularies (`firing`, `NotReady`, `2`) are mapped at the adapter per reporter; an unmapped value is 422 with the offending value echoed, never coerced, never stored raw. A column an external credential authors and an operator reads during an incident is the last place to accept arbitrary strings.

**14. An operator may override an observation, and the override is declared.** A person who knows a reading is wrong writes a `health_override` row (`entity_type`, `entity_id`, `asserted_state`, `reason`, `actor`, `created_at`, `expires_at`) — never by editing an observed column, or it is clobbered by the next poll 30 seconds later and unattributed besides. Create, amend and clear each write a `change_log` row and sit behind CSRF and `RequireWrite` like any declared mutation. `reason` is mandatory; `expires_at` is mandatory and capped at 24h, because a permanent override is how a real outage stays invisible for six weeks. An override *shadows* the observation at read time and never mutates it — the reporter keeps recording the truth underneath, because when the override lapses you need to know what actually happened while it was in force. Every view of an overridden entity shows that it is overridden, by whom, why, and until when. Planned-maintenance suppression uses this same primitive.

**15. The audit views must survive volume, and default to declared.** `ListRecentChanges` is `SELECT * FROM change_log ORDER BY at DESC LIMIT ?` with no filter; `/changes` caps at 200 and the dashboard at 12. Forty instances transitioning in two minutes would clear the declared change that caused the cascade off both screens — the same failure the heartbeat rule prevents, only faster. So: observed transitions are not in `change_log` at all (rule 10), and the incident timeline is a read-side `UNION ALL` of both tables ordered by timestamp. Both are RFC3339 UTC TEXT, which sorts correctly and runs unmodified on both engines — so the split costs nothing at read time and buys a physical guarantee at write time. `/changes` needs pagination and a date range before the first webhook ships. Provide `ChangesForEntity` folded with that entity's **one-hop declared neighbours**; "what changed just before this broke" is the 03:00 question, and if it is not a store method it will never be built. Fold in Go — `diff` is JSON and querying inside it is banned.

**16. A saved view is erased, not soft-deleted, when its owner is scrubbed — the second named exception to soft-delete.** Rule 10's `DELETE FROM observed_transition` removes telemetry that was never a fact about the estate. `ScrubUser` (`internal/store/users_admin.go`) adds a second, narrower one: `DELETE FROM saved_view WHERE user_id = ?`, run inside the scrub's own transaction so the erasure is atomic — a scrub either removes the person's views along with their details, or does not happen. The reasoning is different from rule 10's, and worth stating on its own rather than folding it in as a bare list entry: soft-delete exists to preserve **estate history** — a retired asset is how the estate records that something used to be there, and that history is worth keeping even after the person who created it is gone. A saved view is not estate history. It is one person's shortcut through the UI, and it exists *only* for them; nobody else's understanding of the estate depends on it surviving. Once its owner is erased on request, the view belongs to nobody and serves nothing, so retaining it is holding personal data — the params it searched the estate by, its name — with no purpose GDPR's storage-limitation principle would recognise. The deletion writes no `change_log` row of its own: the erasure's audit entry is the `app_user` update the same transaction already writes. The `change_log` rows the view accrued before the scrub survive — `entity_id` in `change_log` is a bare `TEXT` column with no foreign key to `saved_view`, so the `DELETE` cannot cascade into it, and the entries keep the opaque `app_user.id` and the view's name, the same position every other audit entry takes. The delete is a bare `t.exec`, deliberately not routed through `tx.log`: `systemPermit.Covers` excludes only `app_user` (`internal/domain/role.go:127-129`), so it happens to cover `saved_view` too, and that coverage must not become load-bearing for an erasure that is supposed to leave no trace of its own.

> **This is a CMDB, not a time-series database.** Second-resolution history belongs in Prometheus/Loki. What `invctl` keeps is the transition ledger plus enough recent detail to diagnose a flap.

## Definition of done, amended

Replace the existing list with:

- [ ] Queries use `?` placeholders and run on both engines
- [ ] No forbidden Postgres-only feature introduced
- [ ] Mutation of **declared** state writes a `change_log` row in the same transaction
- [ ] Observed write logs only on a transition, is idempotent and monotonic, does not bump `updated_at`, does not reindex, and writes nothing to `change_log`
- [ ] New columns classified in the table above **and** in `domain.ObservedColumns` / `domain.ProvenanceColumns`
- [ ] Domain constructor validates; DB `CHECK` matches the Go constant set — **for behavioural enums**. The seven domain vocabularies (migration `00004`) are the exception: the constructor checks shape only (`checkVocabulary`), a `FOREIGN KEY` into the lookup table is the authority on existence, and the Go constants are well-known members of an open set rather than the set itself. A lookup table whose values a Go slice still gates would defeat its own purpose
- [ ] Handler branches correctly on `HX-Request`
- [ ] Validation failure returns 422 with the form partial re-rendered
- [ ] Non-GET route is behind CSRF and `RequireWrite` — **or** is the observed-state webhook and satisfies rule 6 in full
- [ ] Table-driven test added; `make test` green on both engines
- [ ] Boundary tests green (see below)
- [ ] `gofmt`, `go vet`, `staticcheck` clean
- [ ] No new dependency, or it was agreed first

### Boundary tests — required, both engines

The boundary is tested or it does not exist. `TestEveryMutationWritesChangeLog` is a spot-check on `asset` create/update/retire; it does not enumerate store methods, so it would not catch a new unlogged one. Splitting the rule in two without these leaves both halves weaker than the single rule they replace.

| Test | Catches |
|---|---|
| `TestEveryColumnIsClassified` | a new column defaulting into the unclassified gap |
| `TestObservedWriteLeavesDeclaredColumnsUntouched` | snapshot declared columns, observe, re-read, assert byte-equal incl. `updated_at` |
| `TestObservedPathTouchesNoDeclaredTable` | parses `internal/store/observed.go` for writes off the allowlist |
| `TestRepeatObservationWritesNoRow` | 100 identical reports → zero rows, `state_since` unmoved |
| `TestStaleObservationRejected` | apply t2 then t1 → t2 survives, no second row |
| `TestChangeLogIsAppendOnly` | greps the tree for `UPDATE change_log` / `DELETE FROM change_log` |
| `TestPruneNeverRemovesDeclaredEntries` | writes declared rows as `AgentActor`, prunes, asserts survival |
| `TestSnapshotRedactsSecretRef` | `secret_ref` absent from every `change_log.diff` |
| `TestOnlyUserActorWritesDeclaredSource` | provenance laundering |
| `TestObservedStateNotIndexed` | `search_index` free of observed values |

## Never do this, amended

Replace the two audit bullets with:

- Mutate declared state without a `change_log` entry
- Let an observed writer touch a declared column, or a machine actor write `verified_by`, `verified_at`, or `source = 'declared'`
- Derive `lifecycle`, `desired_state`, or an impact/maintenance verdict from observed health
- Write an observed value through `logUpdate`/`logCreate`, or into `change_log`
- Prune, filter or classify audit rows by `actor_kind` — agents write declared state
- `UPDATE` or `DELETE FROM change_log` (the rule 10 prune targets `observed_transition` only)
- Take an actor name, a `source`, a `confidence`, or an ordering timestamp from a request body
- Put `secret_ref` in a snapshot, a diff, or a log line
- Auto-create an entity from an observation