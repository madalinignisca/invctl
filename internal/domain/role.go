// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

// Roles a person is given, replacing "a comma-separated list of usernames in
// an environment variable grants write to everything" (WP-G1, migration
// 00058). Nothing in this package consults a role for authorization yet --
// that is Authorizer.CanWrite/CanSeeCosts, WP-G1 task 4. This file exists
// only to give the column a typed Go vocabulary that migration 00058's CHECK
// constraint can be checked against, per
// TestTheGoConstantSetMatchesTheDatabaseCheck.
const (
	// RoleAdministrator has full write access.
	RoleAdministrator = "administrator"
	// RoleObserver is read-only. It is also the default -- see migration
	// 00058's comment and NewAppUser below for why that default is the
	// security decision in this whole piece of work.
	RoleObserver = "observer"
	// RoleProjectOwner manages their own estate but is not an administrator.
	RoleProjectOwner = "project_owner"
)

// Roles is the Go side of the app_user.role CHECK constraint added by
// migration 00058. Keep this in lockstep with that migration's CHECK clause
// -- TestTheGoConstantSetMatchesTheDatabaseCheck reads the clause off disk and
// fails if the two disagree in either direction.
var Roles = []string{RoleAdministrator, RoleObserver, RoleProjectOwner}

// ValidateRole checks Role against the constant set. The DB CHECK constraint
// is the second line of defence, not the first.
//
// Deliberately not folded into a broader AppUser.Validate: NewAppUser never
// takes a role (see its doc comment), so nothing constructs an AppUser with a
// caller-supplied role today, and adding validation nothing calls invites
// exactly the kind of build-ahead this task is told not to do. Task 4, which
// does let a role be set from outside the default, is what wires this in.
func (u *AppUser) ValidateRole() error {
	ve := &ValidationError{}
	checkEnum(ve, "role", u.Role, Roles)
	return ve.OrNil()
}

// ---------------------------------------------------------------------------
// Permit -- WP-G1 Task 7
// ---------------------------------------------------------------------------

// Permit is an authorization decision, handed to a store write transaction in
// place of a bare Actor.
//
// isPermit IS THE WHOLE POINT. It is unexported, so no type declared outside
// this package can implement Permit -- the exact analogue of
// internal/store/boundary_source_test.go's ObservedStore, where *SQLStore is
// deliberately kept from satisfying the observed path's narrow interface. A
// handler holding only a domain.Actor cannot manufacture a Permit by wrapping,
// embedding or promoting one; it can only ask one of this package's three
// minting functions for a real decision. That is what turns the 148-site
// store conversion (Task 10) into a compile error at every call site that
// still passes an Actor, rather than a convention a reviewer has to notice
// was skipped.
//
// THREE METHODS, AND THAT IS FIXED BY TestThePermitInterfaceCannotBeWidenedWithoutSayingSo.
// A fourth method compiles fine and quietly grows what a Permit can be asked
// to do; the width lock exists so that change is a failing test, not a
// silent capability increase. In particular, a MUTATING method is exactly
// the kind of fourth method this test is written to catch -- see the
// "immutable" note below.
type Permit interface {
	// Actor is who this permit was minted for, i.e. what change_log.actor and
	// .actor_kind record for the write it authorizes.
	Actor() Actor
	// Covers reports whether this permit authorizes a write to the row named
	// by (entityType, entityID). tx.log calls this immediately before the
	// INSERT INTO change_log that is the only insertion point into the audit
	// trail (internal/store/store.go) -- see that function's comment for why
	// authorization rides the audit chokepoint rather than being duplicated
	// at every store method.
	Covers(entityType, entityID string) bool
	isPermit()
}

// AdministratorPermit authorizes every write. Minted for a user whose role is
// RoleAdministrator, or who is named in the INV_ADMIN_USERS break-glass list
// (auth.Authorizer.isAdministrator) -- see auth.Authorizer.Permit.
func AdministratorPermit(a Actor) Permit { return administratorPermit{actor: a} }

type administratorPermit struct{ actor Actor }

func (p administratorPermit) Actor() Actor { return p.actor }

// Covers is unconditional. An Administrator who could write everything
// yesterday and is refused today because a new entity type shipped without a
// classification entry is not a security improvement -- it is a denial of
// service on the one role the whole estate depends on to run it. See
// TestAnAdministratorPermitCoversEveryCircuit.
func (administratorPermit) Covers(entityType, entityID string) bool { return true }

func (administratorPermit) isPermit() {}

// SystemPermit authorizes a write made by invctl itself rather than by a
// signed-in person: the seeder, a migration-adjacent backfill, the LDAP
// upsert on first bind. reason exists for the writer to say, in its own call
// site, why a system actor is writing here at all -- it carries no
// authorization weight and Covers does not consult it; it is a debugging and
// review aid, the same role a code comment plays, kept as a real parameter
// instead of a comment so it cannot rot silently out of sync with the call.
func SystemPermit(reason string) Permit { return systemPermit{reason: reason} }

type systemPermit struct{ reason string }

func (p systemPermit) Actor() Actor { return SystemActor }

// Covers authorizes everything EXCEPT app_user. A system actor creating
// circuits during seeding, or upserting net_group members during
// reconciliation, is ordinary; a system actor granting itself a role or
// creating an account is the exact privilege-escalation shape WP-G1 exists to
// close -- see Task 9, which is the caller that actually exercises this
// exclusion. The exclusion lives here, on the permit, rather than as a
// convention its few callers are expected to remember.
func (systemPermit) Covers(entityType, entityID string) bool {
	return entityType != "app_user"
}

func (systemPermit) isPermit() {}

// ScopedEntities is the set of specific rows a ScopedPermit authorizes,
// keyed by entity type and then by id.
//
// This is deliberately a flat, caller-supplied set rather than a live query
// against project_asset/project_service/project_circuit: Task 7 proves the
// mechanism -- that Covers gates the write and that a miss rolls the
// transaction back -- without also committing to how the set is computed.
// Deriving it from a signed-in project owner's actual project membership,
// and from the "both endpoints of a relationship must be in scope" rule
// (docs/rbac-design.md §4), is Task 13/14's job, downstream of the request
// gate (Task 12) that will call auth.Authorizer.Permit.
type ScopedEntities map[string]map[string]bool

// covers reports whether e contains (entityType, entityID). A nil map, like
// a nil slice, is safely queryable and answers false -- an empty scope is a
// permit that authorizes nothing, not a permit that panics.
func (e ScopedEntities) covers(entityType, entityID string) bool {
	return e[entityType][entityID]
}

// ScopedPermit authorizes exactly the entities named in entities, and only
// for entity types classified ScopeProjectLinked -- see ScopeClassOf. A
// project owner may write asset/service/circuit rows their project owns or
// uses; everything estate-wide or purely topological stays
// Administrator-only regardless of what entities happens to contain, which
// is what stops a caller from widening the scope by mis-populating the set.
//
// projects is kept alongside entities, unused by Covers today, because
// Task 14's create-and-link routes need to check a NEW entity's declared
// project against the list the permit was minted with -- a check that has
// nothing to do with an existing row's id and so cannot be expressed through
// Covers. Carrying it here now means Task 14 adds a call site, not a new
// Permit constructor.
func ScopedPermit(a Actor, projects []string, entities ScopedEntities) Permit {
	// Copied rather than aliased: the permit is immutable for the life of a
	// request (see the note on scopedPermit below), and a caller holding the
	// original slice or map must not be able to reach into a permit already
	// handed to a store call and change what it authorizes out from under
	// it.
	//
	// entities is deep-copied, not just the outer map: entities is what
	// Covers actually consults, so aliasing it would leave the guarantee
	// this comment already claims for projects unenforced for the field
	// that matters. A caller holding the original map could add an id, or
	// an entire entity type, to a permit already minted and in flight --
	// see TestScopedPermitEntitiesAreNotAliased.
	cp := make([]string, len(projects))
	copy(cp, projects)
	entitiesCopy := make(ScopedEntities, len(entities))
	for entityType, ids := range entities {
		idsCopy := make(map[string]bool, len(ids))
		for id, ok := range ids {
			idsCopy[id] = ok
		}
		entitiesCopy[entityType] = idsCopy
	}
	return &scopedPermit{actor: a, projects: cp, entities: entitiesCopy}
}

// scopedPermit carries NO transaction-scoped or otherwise mutable state.
//
// An earlier design let the store remember, inside scopedPermit, which ids a
// transaction had minted so far -- so that "create this asset and link it to
// my project in one transaction" could be told apart from "link an existing
// asset into my project" at Covers-check time. It worked, and it was
// reviewed. It was removed because a reviewer asked the question that makes
// mutable authorization state unaffordable: what happens to that recorded
// set when writeSerializable's retry loop discards the transaction and
// starts over? Getting the answer right (reset it) is possible; getting the
// QUESTION to not need asking at all is strictly better, because a
// resettable field is also a forgettable one at the next call site somebody
// adds without reading this comment. The replacement is a routing decision
// -- project-scoped create routes that mint the id and the link in the same
// handler, Task 14 -- not a Permit feature. See
// TestAPermitIsUnchangedByARolledBackTransaction, which is the test that
// keeps this claim honest: it exists specifically so that a mutable field
// added here later fails a test instead of only failing a production retry.
//
// A pointer receiver, not because Covers writes anything, but so that
// reflect.DeepEqual comparisons taken by that test compare the one object
// every retry attempt was actually handed, rather than incidental copies.
type scopedPermit struct {
	actor    Actor
	projects []string
	entities ScopedEntities
}

func (p *scopedPermit) Actor() Actor { return p.actor }

// Covers denies anything ScopeClassOf does not recognise, and anything it
// recognises as estate-config or topology -- a project owner's scope is
// project-linked entities only, and ONLY the three types the schema actually
// links a project to (docs/rbac-design.md §4). An unclassified entityType
// hits the same "not project-linked" branch as a classified-but-wrong-class
// one: this is deliberately not a distinct code path, because the last thing
// a fail-closed check needs is a second way for a reviewer to convince
// themselves the unrecognised case is fine "for now". See
// TestAnUnclassifiedEntityTypeFailsLoudlyRatherThanBeingAllowed for how the
// loudness is achieved instead -- through tx.log's wrapped error, which
// names entityType regardless of which of these two reasons produced the
// refusal.
func (p *scopedPermit) Covers(entityType, entityID string) bool {
	if ScopeClassOf(entityType) != ScopeProjectLinked {
		return false
	}
	return p.entities.covers(entityType, entityID)
}

func (*scopedPermit) isPermit() {}

// ScopeClass groups an entity type into one of the three write-authorization
// buckets docs/rbac-design.md §4 and §6 describe. It answers exactly one
// question -- "may a project owner ever write this, subject to owning the
// specific row" -- and nothing finer: read scope, cost visibility and
// Administrator-only middleware are separate axes handled elsewhere.
type ScopeClass string

const (
	// ScopeProjectLinked entities carry a project_id relationship in the
	// schema (project_asset, project_service, project_circuit) and are the
	// only entity types a ScopedPermit can ever cover. Verified against the
	// migrations, not assumed: no other table in this codebase has a column
	// named project_id.
	ScopeProjectLinked ScopeClass = "project_linked"
	// ScopeEstateConfig entities are estate-wide and apply to every project
	// at once -- teams, vocabularies, custom-field definitions, tags, users,
	// projects themselves (docs/rbac-design.md §4's explicit exclusion list),
	// plus the catalogue and lookup tables that share the same property: a
	// project owner editing one changes what every OTHER project sees.
	// Administrator-only, and stays that way; there is no project by which
	// to scope a rename of a vocabulary term.
	ScopeEstateConfig ScopeClass = "estate_config"
	// ScopeTopology entities are the physical and logical structure between
	// project-linked entities, or facts about entities not yet reachable
	// through a project at all: interfaces, cabling, network groups, power,
	// dependencies, runtime placements. docs/rbac-design.md's route survey
	// counts roughly forty writes that plausibly WILL derive their scope from
	// an owning asset/service/circuit once Task 13/14 builds that derivation
	// -- but "nothing else carries project_id anywhere in any migration" is
	// a fact about today's schema, and this classification defaults every
	// entity type not already proven project-linked to Administrator-only
	// rather than guessing which of the forty a project owner should reach.
	// Widening this bucket into ScopeProjectLinked for a specific type is a
	// design decision for whichever task builds that derivation, not a
	// classification fix.
	ScopeTopology ScopeClass = "topology"
)

// entityScope classifies every entity_type value this codebase writes to
// change_log today.
//
// THE CENSUS IS DELIBERATELY EXHAUSTIVE, for the reason
// domain/classification.go's census is: a default ("everything unlisted is
// topology") would make TestTheScopeClassificationCoversEveryAuditedEntityType
// impossible to write as a positive check, because nothing could ever be
// missing. Enumerated by parsing every call in internal/store to
// tx.log/logCreate/logUpdate/logUpdateBatch and resolving each entityType
// argument -- literal or through the small number of package-level constants
// and struct fields (costTable.entity, vocabularyQueries' keys) that stand in
// for one -- against the actual arguments, not the shorter list the WP-G1
// plan's own route survey estimated (~54; the real figure, counted this way,
// is 71). Recount with `go test ./internal/store/ -run TestZZZExtractEntityTypes`
// against a scratch copy of that walk if this file is ever suspected stale;
// the walk itself is not checked in, because a second copy of it would be a
// second place for the two lists to drift.
var entityScope = map[string]ScopeClass{
	// The three, and only three, entity types a project actually links to
	// (docs/rbac-design.md §4; migrations 00009, 00041).
	"asset":   ScopeProjectLinked,
	"service": ScopeProjectLinked,
	"circuit": ScopeProjectLinked,

	// Estate-wide configuration -- rbac-design.md §4's explicit list, plus
	// the catalogue/lookup tables and the project-linking tables themselves
	// (linking an EXISTING entity to a project is Administrator-only; see
	// §4's create-vs-link distinction).
	"app_user":              ScopeEstateConfig,
	"custom_field":          ScopeEstateConfig,
	"device_type":           ScopeEstateConfig,
	"environment":           ScopeEstateConfig,
	"identity":              ScopeEstateConfig,
	"inflation_rate":        ScopeEstateConfig,
	"manufacturer":          ScopeEstateConfig,
	"project":               ScopeEstateConfig,
	"project_asset":         ScopeEstateConfig,
	"project_circuit":       ScopeEstateConfig,
	"project_service":       ScopeEstateConfig,
	"provider":              ScopeEstateConfig,
	"tag":                   ScopeEstateConfig,
	"team":                  ScopeEstateConfig,
	"asset_kind":            ScopeEstateConfig,
	"service_kind":          ScopeEstateConfig,
	"interface_form_factor": ScopeEstateConfig,
	"environment_role":      ScopeEstateConfig,
	"ip_address_role":       ScopeEstateConfig,
	"data_class":            ScopeEstateConfig,
	"container_engine":      ScopeEstateConfig,
	"cost_kind":             ScopeEstateConfig,
	"responsibility_role":   ScopeEstateConfig,
	"storage_kind":          ScopeEstateConfig,
	// The retention prune's own audit entries (rule 10): an administrator's
	// maintenance action against the whole estate, not any one project's.
	"observed_transition":   ScopeEstateConfig,
	"unmatched_observation": ScopeEstateConfig,

	// Topology, cross-cutting facts, and everything not yet proven
	// project-linked -- see ScopeTopology's doc comment for why the default
	// lands here rather than being guessed into ScopeProjectLinked.
	"aggregate":           ScopeTopology,
	"asn":                 ScopeTopology,
	"asset_cost":          ScopeTopology,
	"backend_member":      ScopeTopology,
	"backend_pool":        ScopeTopology,
	"certificate":         ScopeTopology,
	"circuit_cost":        ScopeTopology,
	"circuit_termination": ScopeTopology,
	"cluster":             ScopeTopology,
	"dependency":          ScopeTopology,
	"endpoint":            ScopeTopology,
	"fhrp_group":          ScopeTopology,
	"health_override":     ScopeTopology,
	"interface":           ScopeTopology,
	"ip_address":          ScopeTopology,
	"ip_range":            ScopeTopology,
	"journal_entry":       ScopeTopology,
	"l2vpn":               ScopeTopology,
	"l2vpn_termination":   ScopeTopology,
	"link":                ScopeTopology,
	"net_anchor":          ScopeTopology,
	"net_attachment":      ScopeTopology,
	"net_group":           ScopeTopology,
	"net_group_member":    ScopeTopology,
	"net_uplink":          ScopeTopology,
	"port_pass_through":   ScopeTopology,
	"power_feed":          ScopeTopology,
	"power_input":         ScopeTopology,
	"power_panel":         ScopeTopology,
	"power_source":        ScopeTopology,
	"prefix":              ScopeTopology,
	"project_cost":        ScopeTopology,
	"rir":                 ScopeTopology,
	"route":               ScopeTopology,
	"rt_container":        ScopeTopology,
	"rt_k8s":              ScopeTopology,
	"rt_systemd":          ScopeTopology,
	"rt_windows":          ScopeTopology,
	"service_cost":        ScopeTopology,
	"service_instance":    ScopeTopology,
	"vlan":                ScopeTopology,
	"vlan_group":          ScopeTopology,
}

// ScopeClassOf returns entityType's class, or "" (the zero ScopeClass, which
// equals none of the three named constants) if nothing in entityScope
// recognises it.
//
// THE HOLE IS LOUD, NOT EMPTY -- the same lesson
// internal/store/boundary_source_test.go's dynamicTable sentinel exists to
// teach. Returning the zero value rather than defaulting to ScopeTopology
// means an unclassified type can never accidentally satisfy a `==
// ScopeProjectLinked` check by falling through to a class that happens to
// compare equal to nothing; scopedPermit.Covers denies it, exactly as it
// denies ScopeEstateConfig and ScopeTopology, and tx.log's wrapped
// domain.ErrForbidden names the entityType regardless of which of those three
// reasons produced the refusal -- see
// TestAnUnclassifiedEntityTypeFailsLoudlyRatherThanBeingAllowed.
func ScopeClassOf(entityType string) ScopeClass {
	return entityScope[entityType]
}
