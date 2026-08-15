// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Column classification: which kind of fact each column in the schema holds.
//
// This is the Go mirror of the normative table in docs/AUDIT.md, and it exists
// because naming is a hint rather than a rule. `desired_state` contains "state"
// and is declared intent; `verified_at` is a timestamp and is a person's
// attestation. Guess and you will be wrong, and the two things that are
// miserable to retrofit -- the portability constraint and the audit trail --
// are exactly what a wrong guess damages.
//
// The census is DELIBERATELY EXHAUSTIVE. AUDIT.md's table ends with "everything
// else is declared", which reads as a default -- but a default makes
// TestEveryColumnIsClassified impossible to write, because nothing can ever be
// unclassified and a new column silently inherits a class nobody chose. So
// every column of every fact-bearing table is listed exactly once, and a column
// present in the live schema but absent here has no class at all. That is the
// gap the test looks for.
//
// Adding a column therefore means three edits: the migration, the table in
// docs/AUDIT.md, and this file. That is the intended friction.

package domain

import "sort"

// ColumnClass is which of the three kinds of fact a column holds.
type ColumnClass string

const (
	// ClassDeclared: somebody asserts this should be true. Configuration and
	// intent. Changes rarely, always because a person decided, and every change
	// is a permanent record in change_log.
	ClassDeclared ColumnClass = "declared"
	// ClassObserved: the estate reporting about itself. Telemetry. Changes
	// constantly, nobody decided it, most reports repeat the last one -- so it
	// logs transitions to observed_transition and never touches change_log.
	ClassObserved ColumnClass = "observed"
	// ClassProvenance: not a fact about the world but a claim about where a fact
	// came from. Governed separately because laundering provenance is how a
	// fabricated fact becomes an authoritative one (rule 7).
	ClassProvenance ColumnClass = "provenance"
	//
	// BOOKKEEPING COLUMNS -- created_at, updated_at, row_version -- are listed
	// as declared alongside the fields they timestamp. None is a fact anybody
	// asserts, but this classification's job is to decide what an OBSERVED
	// writer may touch, and the answer for all three is "nothing": a heartbeat
	// must not bump updated_at (rule 3) and must not consume a version. A
	// fourth class would have to be named explicitly by every rule, for no
	// decision it would change.
)

// ObservedTables are tables where every column is observed, so a new column on
// one of them needs no entry below. These are the tables migration 00008
// creates for exactly that purpose: the separation is physical, and that is
// what lets rule 10 say "observed_transition is the only prunable table"
// instead of writing a predicate somebody has to keep correct.
//
// health_override is deliberately NOT here. An override is a person overruling
// a monitor -- declared state that happens to be about observed state (rule 14).
var ObservedTables = map[string]bool{
	"asset_health":          true,
	"observed_transition":   true,
	"unmatched_observation": true,
}

// ExemptTables carry no inventory fact, mapped to why they are exempt. Listing
// them explicitly rather than skipping by prefix is the point: a table that
// appears in the live schema and in neither this map nor the census below is a
// failure, not a shrug.
//
// The search_index_* entries are FTS5's private shadow tables and exist only on
// SQLite; sqlite_sequence likewise. If a future SQLite adds another, the test
// fails and a human looks -- which is the correct outcome.
var ExemptTables = map[string]string{
	"goose_db_version":         "migration bookkeeping, owned by goose",
	"goose_db_version_dialect": "migration bookkeeping for the dialect-split migrations",
	"sessions":                 "session store, owned by scs; holds no inventory fact",
	"search_index":             "derived index over declared columns; rebuilt, never authored",
	"search_index_config":      "FTS5 shadow table (SQLite only)",
	"search_index_content":     "FTS5 shadow table (SQLite only)",
	"search_index_data":        "FTS5 shadow table (SQLite only)",
	"search_index_docsize":     "FTS5 shadow table (SQLite only)",
	"search_index_idx":         "FTS5 shadow table (SQLite only)",
	"sqlite_sequence":          "SQLite internal",
}

// ObservedColumns are the observed columns of otherwise-declared tables, keyed
// by table. This is the half of docs/AUDIT.md's table that is easiest to get
// wrong, so each entry says why.
var ObservedColumns = map[string][]string{
	// A login is telemetry about a person, not a configuration change. This is
	// also the one pre-existing unaudited write in the codebase (TouchLogin),
	// and rule 11 closes that exception list around it: adding a second means
	// editing docs/AUDIT.md first.
	"app_user": {"last_login_at"},

	// first_seen/last_seen/confidence stay on the dependency row rather than
	// moving to a health table: HANDOVER §3.5 makes them part of the fact, and
	// a reconciler writes them at reconciler frequency, not at poll frequency,
	// so they do not carry the unbounded-growth problem that moved
	// service_instance.observed_state out.
	//
	// AUDIT.md's opening paragraph groups `confidence` with `source` as
	// provenance, while its normative table classifies dependency.confidence as
	// observed. The table wins here, because the table is the part labelled
	// normative -- but rule 7 still governs who may WRITE it ("set by the store
	// from the credential, never from the payload"), and classification decides
	// the audit obligation, not the authorisation. Worth resolving upstream.
	"dependency": {"confidence", "first_seen", "last_seen"},

	// The four net_* tables carry the same provenance/observation column set as
	// dependency, added by 00007 for the same reason, and are classified the
	// same way. AUDIT.md's table predates them and does not list them; applying
	// its rule column-by-column is the only reading that does not leave a third
	// of the schema unclassified.
	"net_anchor":     {"confidence", "first_seen", "last_seen"},
	"net_attachment": {"confidence", "first_seen", "last_seen"},
	"net_group":      {"confidence", "first_seen", "last_seen"},
	"net_uplink":     {"confidence", "first_seen", "last_seen"},
}

// ProvenanceColumns record where a fact came from. Every one of these decides
// whether a row is authoritative, which is why rule 7 governs them separately:
// no machine may assert that a fact was hand-declared.
//
// app_user.source is deliberately absent. Its vocabulary is local|ldap -- which
// authentication backend owns the account -- and shares no value with the
// declared/discovered_* provenance vocabulary, so rule 7 has nothing to say
// about it. It is a property of the account, and declared.
var ProvenanceColumns = map[string][]string{
	"dependency":       {"source"},
	"net_anchor":       {"source"},
	"net_attachment":   {"source"},
	"net_group":        {"source"},
	"net_uplink":       {"source"},
	"service_instance": {"source"},
}

// DeclaredColumns is the rest of the schema, enumerated so that nothing
// defaults. Order within a table follows the migration, so a diff against the
// CREATE TABLE reads straight down.
var DeclaredColumns = map[string][]string{
	"app_user": {
		"id", "username", "display_name", "email", "source",
		"password_hash", "is_active", "created_at",
	},
	"asset": {
		"id", "kind", "name", "parent_id", "serial", "asset_tag", "vendor",
		"model", "device_type_id",
		// What this took over from (migration 00042). Declared: a person says
		// this box succeeded that one. Nothing observes a refresh, and nothing
		// derives it -- a serial number changing is not evidence of lineage.
		"replaces_asset_id",
		// Compute capacity (migration 00044). Declared: a person agrees a VM
		// gets eight cores. A hypervisor REPORTING thirty percent CPU is
		// telemetry and belongs to the observed path with its own audit
		// obligations, which is why adding these adds no new class of fact.
		"cpu_cores", "memory_mb",
		"vcpu_provisioned", "vcpu_allocated",
		"memory_provisioned_mb", "memory_allocated_mb",
		// A storage POOL's own size (migration 00046). Declared for the same
		// reason: somebody put the disks in, and the raw figure is what an
		// operator knows. How much survives replication is arithmetic, which
		// is why the ratio lives on storage_kind and not here.
		"storage_kind", "raw_capacity_gb",
		// Where it physically sits (migration 00027). Declared: somebody put it
		// there. u_height on a RACK is its capacity; a mounted box's height comes
		// from its catalogued model.
		"u_height", "rack_position", "rack_face",
		// What the CABINET measures (migration 00038). Declared, and not a
		// close call in either direction: a tape measure is held by a person,
		// and nothing reports these. usable_depth_mm is measured rather than
		// derived; width_mm is external and side clearance follows from it,
		// because EIA-310 fixes the equipment width.
		"usable_depth_mm", "width_mm", "max_load_grams",
		"lifecycle", "team_id", "manager_role",
		"eol_date", "attrs",
		"created_at", "updated_at",
		"row_version",
	},
	// Journal entries (migration 00039). DECLARED, and the interesting part is
	// that it is not a close call: a note is somebody choosing to write
	// something down, which is the definition. Nothing observes it and nothing
	// derives it, so every write takes a change_log row like any other entity
	// -- including the withdrawal, because removing an inconvenient note
	// without trace is what the audit trail exists to prevent.
	//
	// `author` holds an app_user.id and never a name, the same rule as
	// change_log.actor: the log is kept forever, so it must carry nothing
	// anybody could ask to have erased. `body` is free text a human wrote and
	// is the one column here where personal data could legitimately arrive --
	// which is a deployment policy matter, said out loud in the field hint
	// rather than pretended away.
	"journal_entry": {
		"id", "entity_type", "entity_id", "kind", "body", "author",
		"lifecycle", "created_at", "updated_at", "row_version",
	},
	"asset_closure":     {"ancestor_id", "descendant_id", "depth"},
	"asset_environment": {"asset_id", "environment_id", "note"},
	// What a workload holds in a pool (migration 00046). DECLARED, and the
	// distinction is the usual one: this is what somebody agreed the workload
	// gets, never what df(1) reports. A filesystem filling up is telemetry and
	// would arrive through the observed path with its own obligations.
	"asset_storage_claim": {"asset_id", "pool_id", "allocated_gb", "note",
		"created_at", "updated_at"},
	"backend_member": {"pool_id", "endpoint_id", "weight", "is_backup"},
	"backend_pool":   {"id", "service_id", "name", "lb_algorithm"},
	// The seven domain vocabularies (migration 00004). Declared, and not a
	// close call: a lookup row is somebody asserting that a kind of thing
	// exists in this estate. Nothing observes it, nothing reports it, and it
	// changes only because a person decided -- which is the definition.
	//
	// They are not exempt. ExemptTables is for tables carrying no inventory
	// fact -- goose bookkeeping, the session store, the derived search index --
	// and a vocabulary is the opposite of derived: it is the authority the
	// entity columns' foreign keys point at. Exempting it would mean a future
	// UI for adding a value could mutate declared state with no change_log row
	// and no test objecting, which is exactly the gap this census exists to
	// close. No such UI exists today; values arrive in the migration, and no
	// migration in this repository writes change_log.
	//
	// Consequence to be aware of: ClassifiedTables feeds ChangeEntityTypes, so
	// all seven appear in the /changes entity filter. They join the other
	// audited-but-currently-quiet tables already there (asset_closure,
	// backend_member, rt_k8s); the filter offers what may be audited, not what
	// happens to have rows.
	// Declared, including the behaviour flags: what a kind is permitted to do is
	// something an operator asserts, not something the estate reports.
	"asset_kind": {"code", "label", "sort_order", "description", "can_host_instances", "is_attachable"},
	// raw_per_usable is the same kind of fact as can_host_instances: what a
	// technology costs in raw capacity is asserted by whoever configured it,
	// and a pool never reports its own replication factor here.
	"storage_kind":          {"code", "label", "sort_order", "description", "raw_per_usable"},
	"service_kind":          {"code", "label", "sort_order", "description"},
	"interface_form_factor": {"code", "label", "sort_order", "description"},
	"environment_role":      {"code", "label", "sort_order", "description", "is_transit"},
	"ip_address_role":       {"code", "label", "sort_order", "description"},
	"data_class":            {"code", "label", "sort_order", "description"},
	"container_engine":      {"code", "label", "sort_order", "description"},
	// The audit trail of declared change is itself declared: it is written by a
	// person's act and is append-only forever (rule 10).
	"change_log": {
		"id", "entity_type", "entity_id", "action", "actor", "actor_kind",
		"diff", "ticket_ref", "at",
	},
	"dependency": {
		"id", "consumer_service_id", "consumer_instance_id", "provider_endpoint_id",
		"provider_route_id", "nature", "tolerance_seconds", "failure_mode",
		"identity_id", "auth_method", "firewall_rule_ref",
		// verified_by/verified_at are a PERSON's attestation that an edge is
		// legitimate, which is why a machine credential may never write them:
		// that is a rubber stamp on an undocumented chd edge and on the firewall
		// rule justified by it.
		"verified_by", "verified_at",
		"lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"dependency_data_class": {"dependency_id", "data_class"},
	"endpoint": {
		"id", "service_id", "name", "l4_proto", "port", "unix_path", "bind_scope",
		"ip_address_id", "l7_proto", "tls_mode", "certificate_id", "exposure",
		"created_at", "updated_at", "row_version",
		"lifecycle",
	},
	"environment": {"id", "code", "name", "role", "in_scope", "criticality", "created_at", "updated_at", "row_version"},
	// Rule 14: an operator overruling a monitor is a declared act, audited like
	// any other. The row is about observed state; it is not observed state.
	"health_override": {
		"id", "entity_type", "entity_id", "asserted_state", "reason", "actor",
		"lifecycle", "created_at", "updated_at", "expires_at",
	},
	"identity": {
		"id", "kind", "name", "realm", "secret_ref", "rotation_days",
		"last_rotated", "team_id", "lifecycle",
	},
	"interface": {
		"id", "asset_id", "name", "form_factor", "speed_mbps", "mac", "mtu",
		"lag_parent_id", "is_mgmt", "enabled",
		"created_at", "updated_at", "row_version",
	},
	"ip_address": {"id", "addr_text", "addr_family", "addr_start", "interface_id", "fhrp_group_id", "role", "created_at", "updated_at", "row_version"},
	"link":       {"id", "a_interface_id", "b_interface_id", "medium", "length_m", "lifecycle"},
	"net_anchor": {
		"id", "code", "name", "scope", "group_id", "environment_id", "plane",
		"lifecycle", "verified_by", "verified_at", "created_at", "updated_at",
	},
	"net_attachment": {
		"id", "asset_id", "group_id", "plane", "lifecycle",
		"verified_by", "verified_at", "created_at", "updated_at",
	},
	"net_attachment_member": {"attachment_id", "asset_id", "interface_id"},
	"net_group": {
		"id", "code", "name", "kind", "role", "availability", "min_healthy",
		"failover_mode", "environment_id", "lifecycle",
		"verified_by", "verified_at", "attrs", "created_at", "updated_at",
	},
	"net_group_member": {"group_id", "asset_id", "role", "lifecycle", "created_at", "updated_at"},
	"net_uplink": {
		"id", "group_id", "upstream_group_id", "plane", "lifecycle",
		"verified_by", "verified_at", "created_at", "updated_at",
	},
	"prefix": {
		"id", "cidr_text", "addr_family", "addr_start", "addr_end",
		"environment_id", "role", "vrf_id", "vlan_ref_id",
		"created_at", "updated_at", "row_version",
	},
	// Clusters, migration 00037. Declared: somebody built a cluster and set its
	// HA policy. A hypervisor REPORTS whether it is in quorum, and that is
	// observed state about the host -- a different fact from the policy declared
	// here, and the disagreement (HA configured, cluster not in quorum) is the
	// finding.
	//
	// This is the first table the IMPACT ENGINE branches on rather than
	// reports, so a wrong value here changes what a simulation concludes.
	"cluster": {
		"id", "name", "kind", "ha_policy", "min_hosts", "description",
		"lifecycle", "created_at", "updated_at", "row_version",
		"cpu_overcommit",
	},
	// A set table, replaced wholesale with its cluster and audited on it.
	"cluster_member": {"cluster_id", "asset_id"},
	// Circuits, migration 00035. Declared throughout: somebody signed a
	// contract. Nothing observes a circuit -- a router REPORTS whether the
	// interface it lands on is up, and that is observed state about the port,
	// a different fact from the contract recorded here.
	//
	// provider.account_ref is governed by the team.contact_ref rule: an account
	// or customer reference, never a named person. A CMDB kept for ever with an
	// append-only change_log must carry nothing anybody could ask to erase.
	"provider": {
		"id", "name", "account_ref", "portal_url", "description", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	"circuit": {
		"id", "cid", "provider_id", "service_type", "commit_mbps", "install_date",
		"contract_end", "description", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"circuit_termination": {
		"id", "circuit_id", "side", "asset_id", "interface_id", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// The fourth cost surface. Same obligation as the other three: somebody
	// read an invoice and typed it, and the rollups are derived at read time.
	"circuit_cost": {
		"id", "circuit_id", "kind", "period", "amount_minor", "note",
		"valid_from", "valid_until", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
	// Overlays, migration 00034. Declared: somebody configured a VXLAN and
	// mapped a VLAN into it. A fabric controller can REPORT which VNIs are
	// programmed on which leaf, and that is observed state about the switch --
	// a different fact from the overlay declared here, and the disagreement
	// between them is the finding.
	"l2vpn": {
		"id", "name", "kind", "identifier", "description", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// Its own id and lifecycle, so it is audited like a dependency rather than
	// folded into the overlay's diff.
	"l2vpn_termination": {
		"id", "l2vpn_id", "vlan_id", "interface_id", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// The registry layer, migration 00033. Declared: a registry delegated a
	// block to this organisation and somebody wrote that down. Nothing observes
	// a delegation -- a looking glass can report what is being ADVERTISED, and
	// that is observed state about a route, a different fact from the allocation
	// recorded here.
	"rir": {
		"id", "name", "is_private", "description", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	"aggregate": {
		"id", "cidr_text", "addr_family", "addr_start", "addr_end", "rir_id",
		"allocated_on", "description", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"asn": {
		"id", "number", "name", "rir_id", "description", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// First-hop redundancy, migration 00032. Declared: somebody configured VRRP
	// on two routers and gave the pair a virtual address. A router REPORTS which
	// of them is currently master, and that is observed state about the group --
	// a different fact from the membership declared here, and the one worth
	// having beside it during an incident.
	"fhrp_group": {
		"id", "protocol", "group_number", "name", "description", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// A set table, replaced wholesale with its group and audited on it.
	"fhrp_member": {"group_id", "interface_id", "priority"},
	// The VLAN model, migration 00031. Declared throughout: a VLAN exists
	// because somebody configured it, a group exists because somebody chose
	// where the numbering applies, and a port is in a VLAN because somebody put
	// it there. A switch can REPORT which VLANs a trunk carries, and that would
	// be observed state about the port -- a different fact from the membership
	// declared here, and the disagreement between them is the finding.
	"vlan": {
		"id", "vid", "name", "group_id", "role", "environment_id", "description",
		"lifecycle", "created_at", "updated_at", "row_version",
	},
	"vlan_group": {
		"id", "name", "scope_asset_id", "description", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// A set table, replaced wholesale with its interface and audited on it.
	"interface_vlan": {"interface_id", "vlan_id", "mode"},
	// A reservation is declared: somebody set the space aside. Nothing observes
	// a DHCP pool -- a lease server knows what it has issued, and that would be
	// observed state about ADDRESSES, a different fact from the reservation
	// that gave it the range to issue from.
	"ip_range": {
		"id", "start_text", "end_text", "addr_family", "addr_start", "addr_end",
		"vrf_id", "role", "description", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// A VRF is configuration in the purest sense: somebody decided that this
	// address space is separate from that one. Nothing observes a VRF -- a
	// router can report which table it consulted, but that is a reading of a
	// decision already made here, never the source of one.
	"vrf": {
		"id", "name", "rd", "description", "lifecycle",
		"created_at", "updated_at", "row_version",
	},
	// Projects are declared through and through: somebody asserts that a thing
	// belongs to a project. Nothing here is ever written by an observation, and
	// the derived footprint is computed at read time and stored nowhere.
	// The three cost surfaces (migration 00012). Declared without argument:
	// somebody read an invoice and typed it. Nothing observes a price and
	// nothing derives one -- the ROLLUPS are derived, and they are computed at
	// read time and stored nowhere for exactly that reason.
	"asset_cost": {
		"id", "asset_id", "kind", "period", "amount_minor", "note",
		"valid_from", "valid_until", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"service_cost": {
		"id", "service_id", "kind", "period", "amount_minor", "note",
		"valid_from", "valid_until", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"project_cost": {
		"id", "project_id", "kind", "period", "amount_minor", "note",
		"valid_from", "valid_until", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"cost_kind": {"code", "label", "sort_order", "description"},
	"project": {
		"id", "code", "name", "description", "team_id", "lifecycle",
		"created_at", "updated_at",
		"row_version",
		// What the deal was priced on (migration 00045). Declared: a person
		// records the assumption a quote was built on. Deliberately not called
		// "contracted" -- these contracts name no resources, and a column named
		// for a promise nobody made will one day be quoted as one.
		"priced_for_vcpu", "priced_for_memory_mb",
	},
	// Who looks after what (migration 00014). Declared without argument:
	// somebody decided that this team is answerable for this box. Nothing
	// observes it and nothing derives it -- and `manager_role` is documentation
	// rather than authorization, so no code path branches on its value.
	"team": {
		"id", "code", "name", "description", "contact_ref", "lifecycle",
		"created_at", "updated_at",
		"row_version",
	},
	"responsibility_role": {"code", "label", "sort_order", "description"},
	// What a patch panel does (migration 00028). Declared: somebody patched it.
	// `position` anticipates breakout in WP-B4 and is 1 for every 1:1 panel,
	// which is all of them until then.
	"port_pass_through": {
		"id", "front_interface_id", "rear_interface_id", "position",
		"lifecycle", "created_at", "updated_at",
		"row_version",
	},
	// Import jobs (migration 00025). Declared: a record of something a person
	// did, carrying the same OPAQUE actor id change_log does, so this table
	// holds no personal data either. It writes no change_log row of its own --
	// the rows an import creates each write theirs, naming the same actor, and
	// "who put this box in the inventory" is the obligation rather than "who
	// ran a batch". `rows_done` counts rows EXAMINED, never rows you have: the
	// import is one transaction, so a run that stopped halfway wrote nothing.
	"import_job": {
		"id", "kind", "filename", "actor", "actor_kind", "status",
		"rows_total", "rows_done", "created", "message", "problems",
		"created_at", "started_at", "finished_at",
	},
	// The hardware catalogue (migration 00022). Declared throughout, and the
	// interesting one is `device_type.eol_date`: it is what a MANUFACTURER
	// published, transcribed by a person, and an asset that states its own
	// overrides it. Neither is observed -- nothing in the estate reports when it
	// stops being supportable, which is exactly why somebody has to write it
	// down. `support_ref` is a portal or a contract reference and is governed by
	// the same rule as team.contact_ref: never a person, never a credential.
	"manufacturer": {
		"id", "code", "name", "support_ref", "lifecycle",
		"created_at", "updated_at",
		"row_version",
	},
	// The power chain (migration 00023). Declared throughout, and worth stating
	// because one column looks observed and is not: `power_input.draw_va` is a
	// NAMEPLATE or allocated figure a person typed, not a measurement. Nothing in
	// the estate reports it. A measured draw arriving from a PDU would be
	// observed state with a reporter, an age and a transition rule -- a different
	// contract entirely (rules 1 and 3), and it would be a new column beside this
	// one rather than a reinterpretation of it.
	//
	// `max_utilisation` is a local electrical decision somebody recorded, and
	// nothing derives it. The ratings are nullable on purpose: "not recorded"
	// must stay distinguishable from zero, or an unrated feed reports as
	// over-allocated the moment anything draws on it.
	// What sits above a panel (migration 00024). Declared: somebody asserts that
	// this board is fed by that UPS. Nothing observes it. `kind` is behavioural
	// rather than descriptive -- it decides whether two inputs converging here is
	// a fault or the design -- and `asset_id` is the optional link to the same
	// thing as an inventory item, which is how a UPS's battery end-of-life
	// reaches the expiry report.
	"power_source": {
		"id", "parent_id", "site_id", "asset_id", "name", "kind", "notes",
		"lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"power_panel": {
		"id", "site_id", "source_id", "name", "voltage", "amperage", "phase", "notes",
		"lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"power_feed": {
		"id", "panel_id", "name", "voltage", "amperage", "phase", "max_utilisation",
		"notes", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"power_input": {
		"id", "asset_id", "feed_id", "name", "draw_va", "notes",
		"lifecycle", "created_at", "updated_at",
		"row_version",
	},
	"device_type": {
		"id", "manufacturer_id", "model", "part_number", "u_height", "full_depth",
		// The model's physical facts (migration 00038). Declared: they come
		// off a datasheet somebody read. airflow NULL means nobody said, which
		// is a third state and not front_to_rear.
		"depth_mm", "weight_grams", "airflow",
		// Where the ports are (migration 00040). Declared, off a datasheet.
		"port_face",
		"eol_date", "notes", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
	// Certificates (migration 00015). Declared: somebody asserts that this
	// certificate, with this expiry, is deployed here. What a scanner finds
	// actually being served is a different fact for the observed side, and the
	// disagreement between the two is the interesting part. `key_ref` is a path
	// and is redacted from the audit trail like secret_ref.
	"certificate": {
		"id", "subject_cn", "issuer", "fingerprint", "serial",
		"not_before", "not_after", "key_ref", "team_id", "manager_role",
		"lifecycle", "attrs", "created_at", "updated_at",
		"row_version",
	},
	"certificate_san":     {"certificate_id", "name"},
	"certificate_asset":   {"certificate_id", "asset_id", "note", "lifecycle", "created_at", "updated_at"},
	"certificate_service": {"certificate_id", "service_id", "note", "lifecycle", "created_at", "updated_at"},
	"project_asset": {
		"project_id", "asset_id", "relation", "note", "lifecycle",
		"created_at", "updated_at",
	},
	"project_service": {
		"project_id", "service_id", "relation", "note", "lifecycle",
		"created_at", "updated_at",
	},
	"project_circuit": {
		"project_id", "circuit_id", "relation", "note", "lifecycle",
		"created_at", "updated_at",
	},
	// A published statistic somebody typed. Declared: nothing observes it and
	// invariant 7 forbids fetching it, so it can only ever arrive from a person.
	"inflation_rate": {
		"year", "basis_points", "source", "created_at", "updated_at", "row_version",
	},
	"route": {
		"id", "frontend_endpoint_id", "match_type", "match_value",
		"backend_pool_id", "tls_termination", "priority",
	},
	"rt_container": {
		"instance_id", "engine", "container_name", "compose_project", "compose_service",
		"image_repo", "image_tag", "image_digest", "restart_policy", "network_mode", "rootless",
	},
	"rt_k8s": {
		"instance_id", "cluster_asset_id", "namespace", "workload_kind",
		"workload_name", "replicas_desired", "service_account", "image_digest",
	},
	"rt_systemd": {
		"instance_id", "unit_name", "unit_type", "exec_start", "run_as_user",
		"run_as_group", "restart", "unit_after", "unit_requires", "drop_ins",
	},
	"rt_windows": {
		"instance_id", "service_name", "display_name", "binary_path", "start_type",
		"logon_identity_id", "depends_on_svc", "recovery_action",
	},
	"service": {
		"id", "code", "name", "kind", "environment_id",
		"availability", "min_healthy", "failover_mode", "tier", "rto_minutes",
		"rpo_minutes", "team_id", "manager_role", "lifecycle", "eol_date", "attrs",
		"created_at", "updated_at",
		"row_version",
	},
	// desired_state is DECLARED and stays here after 00008 moved observed_state
	// and observed_at out. "Observed stopped, therefore desired stopped" is the
	// intent-collapse that makes drift undetectable (rule 2).
	"service_instance": {
		"id", "service_id", "host_asset_id", "runtime_type", "role", "shard",
		"ordinal", "desired_state", "lifecycle", "created_at", "updated_at",
		"row_version",
	},
}

// ClassifyColumn returns the class of a column. ok is false when the column is
// not classified at all, which is a failure rather than a default.
func ClassifyColumn(table, column string) (ColumnClass, bool) {
	if ObservedTables[table] {
		return ClassObserved, true
	}
	if containsString(ProvenanceColumns[table], column) {
		return ClassProvenance, true
	}
	if containsString(ObservedColumns[table], column) {
		return ClassObserved, true
	}
	if containsString(DeclaredColumns[table], column) {
		return ClassDeclared, true
	}
	return "", false
}

// IsExemptTable reports whether a table is outside the classification, and why.
func IsExemptTable(table string) (string, bool) {
	why, ok := ExemptTables[table]
	return why, ok
}

// ClassifiedTables lists every table the census covers, sorted.
func ClassifiedTables() []string {
	seen := map[string]bool{}
	for t := range ObservedTables {
		seen[t] = true
	}
	for _, m := range []map[string][]string{DeclaredColumns, ObservedColumns, ProvenanceColumns} {
		for t := range m {
			seen[t] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ClassificationConflicts reports contradictions inside this file: a column
// claimed by two classes, or a table both exempt and classified.
//
// The maps are hand-maintained and a copy-paste can put one column in two of
// them, at which point ClassifyColumn silently returns whichever it checks
// first and the census looks complete while being wrong. A test asserting this
// is empty costs nothing and removes that whole failure mode.
func ClassificationConflicts() []string {
	var out []string
	for _, table := range ClassifiedTables() {
		if _, exempt := ExemptTables[table]; exempt {
			out = append(out, table+": both exempt and classified")
		}
		if ObservedTables[table] {
			for _, m := range []map[string][]string{DeclaredColumns, ObservedColumns, ProvenanceColumns} {
				if len(m[table]) > 0 {
					out = append(out, table+": every column is observed, so it needs no per-column entry")
					break
				}
			}
			continue
		}
		seen := map[string]ColumnClass{}
		for class, cols := range map[ColumnClass][]string{
			ClassDeclared:   DeclaredColumns[table],
			ClassObserved:   ObservedColumns[table],
			ClassProvenance: ProvenanceColumns[table],
		} {
			for _, c := range cols {
				if prev, dup := seen[c]; dup {
					out = append(out, table+"."+c+": classified as both "+string(prev)+" and "+string(class))
					continue
				}
				seen[c] = class
			}
		}
	}
	sort.Strings(out)
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
