// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"context"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// ---------- the demo's own spread ----------
//
// StageCustomFields above answers WP-A4's own test obligation: one asset and
// one service fully populated, one of each deliberately empty, so a detail
// page's null-everywhere state has a real fixture entity to render. That is
// correct for the test suite and thin as a demo -- a visitor clicking through
// the estate lands on "not recorded" almost everywhere and reasonably
// concludes the feature is unused.
//
// THIS FILE IS THE DEMO'S OWN LAYER, gated the same way seed_company.go is:
// off unless a deployment asks for it, so TestTheSeedFixtureCoversEveryCustomFieldKind
// keeps observing exactly what it observes today. It calls StageCustomFields
// itself nowhere in this file -- it only ADDS values against the fields that
// call already defined, plus one field and one option this phase defines and
// retires for itself. Running this after a fixture StageCustomFields has
// never touched would find no fields to hang values on, so the demo wiring in
// cmd/invctl/main.go always calls StageCustomFields first.
//
// WHY A SEPARATE FUNCTION RATHER THAN WIDENING StageCustomFields ITSELF: the
// two have different audiences. StageCustomFields' hv-02/vault-full,
// sw-oob-1/backup-agent-empty shape is asserted by name in
// TestTheSeedFixtureCoversEveryCustomFieldKind and must not move. Folding a
// wider spread into the same function would either break that test or force
// it to special-case which values are "the real assertion" -- fragile for no
// gain, when a second function costs nothing and keeps the boundary explicit.

// StageCustomFieldSpread populates a realistic proportion of the company
// estate's assets and services with the fields StageCustomFields already
// defined, adds one field that is populated and then retired (a value stays
// visible after its field is retired), and retires one option of
// criticality_tier while an asset still holds it (the option itself keeps
// displaying, marked retired, on the value that already chose it).
//
// Idempotent the same coarse way StageCustomFields is: guarded on the
// presence of the "legacy_ticket_ref" field this phase alone defines, so a
// repeated dev-mode start does not collide on the unique (entity_type, code)
// index or re-retire an option twice. The same caveat StageCustomFields
// documents applies here too -- a process that dies partway through leaves
// the demo half-seeded until a human drops the database.
func StageCustomFieldSpread(ctx context.Context, s *store.SQLStore, adminUsername string) error {
	existingAssetFields, err := s.ListCustomFields(ctx, domain.CustomFieldEntityAsset, true)
	if err != nil {
		return fmt.Errorf("checking for the demo's own custom field spread: %w", err)
	}
	for _, f := range existingAssetFields {
		if f.Code == "legacy_ticket_ref" {
			return nil
		}
	}

	admin, err := s.GetUserByUsername(ctx, adminUsername)
	if err != nil {
		return fmt.Errorf("looking up %s for the demo's custom field spread: %w", adminUsername, err)
	}
	actor := domain.UserActor(admin)
	now := s.Now()

	assetFieldIDs := fieldIDsByCode(existingAssetFields)
	for _, code := range []string{"procurement_ref", "power_budget_watts", "last_physical_audit", "is_leased", "criticality_tier"} {
		if _, ok := assetFieldIDs[code]; !ok {
			return fmt.Errorf("staging the demo's custom field spread: asset field %s is not defined yet "+
				"-- StageCustomFields must run first", code)
		}
	}

	existingServiceFields, err := s.ListCustomFields(ctx, domain.CustomFieldEntityService, true)
	if err != nil {
		return fmt.Errorf("checking for the demo's own service custom values: %w", err)
	}
	serviceFieldIDs := fieldIDsByCode(existingServiceFields)
	for _, code := range []string{"runbook_url", "sla_minutes", "last_dr_test", "customer_facing", "support_tier"} {
		if _, ok := serviceFieldIDs[code]; !ok {
			return fmt.Errorf("staging the demo's custom field spread: service field %s is not defined yet "+
				"-- StageCustomFields must run first", code)
		}
	}

	// ---------- assets ----------
	//
	// Owned hardware (procurement_ref, is_leased=false where recorded, dates
	// where somebody plausibly walked the rack) against rented and leased
	// capacity (is_leased=true, no procurement reference -- a lease has a
	// contract, not a purchase order, and this demo does not invent one).
	// Criticality follows each asset's actual role: the two core switches and
	// the production firewall pair are high; a dev-only firewall or top-of-
	// rack switch is low; everything else that matters but is not load-
	// bearing on its own is medium. hv-02 (StageCustomFields) and sw-oob-1
	// (deliberately empty) are untouched here.
	assetValues := map[string]map[string]string{
		// The core of the original rack: owned, catalogued, audited.
		"hv-01": {
			"criticality_tier":    "high",
			"power_budget_watts":  "750",
			"procurement_ref":     "PO-2023-0091",
			"last_physical_audit": domain.FormatDate(now.AddDate(0, 0, -45)),
		},
		// hv-03 is deliberately uncatalogued for the expiry report (seed.go);
		// its custom fields are a different feature and get a sparse
		// entry -- an estate that has not audited a box in a while still
		// knows it owns it.
		"hv-03": {
			"is_leased":        "false",
			"criticality_tier": "medium",
		},
		"pdu-a1": {
			"procurement_ref": "PO-2022-1187",
			"is_leased":       "false",
		},
		"sw-core-1": {
			"criticality_tier":   "high",
			"power_budget_watts": "120",
			"is_leased":          "false",
		},
		"sw-core-2": {
			"criticality_tier":   "high",
			"power_budget_watts": "120",
		},
		"fw-edge-1": {
			"criticality_tier":    "high",
			"procurement_ref":     "PO-2023-0034",
			"last_physical_audit": domain.FormatDate(now.AddDate(0, 0, -60)),
			"is_leased":           "false",
		},
		"fw-edge-2": {
			"criticality_tier":   "high",
			"power_budget_watts": "90",
		},
		"ceph-block": {
			"criticality_tier":   "high",
			"procurement_ref":    "PO-2023-0210",
			"power_budget_watts": "2200",
			"is_leased":          "false",
		},
		// The half-installed box: sparse, and honestly so. Nobody has
		// finished commissioning it, so nobody has filled in much about it
		// either.
		"srv-backup-proxy-1": {
			"is_leased": "false",
		},

		// The colo tier: rented rack, rented switch, rented boxes.
		"colo-rack-07": {
			"is_leased":       "true",
			"procurement_ref": "COLO-CONTRACT-4471",
		},
		"sw-colo-1": {
			"is_leased":          "true",
			"criticality_tier":   "medium",
			"power_budget_watts": "60",
		},
		"srv-colo-1": {
			"is_leased":          "true",
			"criticality_tier":   "medium",
			"power_budget_watts": "550",
		},
		"srv-colo-2": {
			"is_leased":          "true",
			"criticality_tier":   "medium",
			"power_budget_watts": "550",
		},
		"srv-colo-3": {
			"is_leased":          "true",
			"criticality_tier":   "medium",
			"power_budget_watts": "550",
		},

		// The per-environment firewalls, full detail on the one that carries
		// production -- the second entity to hold every field this phase
		// defines, alongside hv-02 and vault from StageCustomFields.
		"fw-prod-1": {
			"criticality_tier":    "high",
			"procurement_ref":     "PO-2024-0501",
			"is_leased":           "false",
			"power_budget_watts":  "45",
			"last_physical_audit": domain.FormatDate(now.AddDate(0, 0, -20)),
		},
		// fw-dev-1 and sw-tor-a2 hold "low" -- the criticality_tier option
		// this phase retires below, deliberately while these two still carry
		// it. That is the fixture for "an operator asks what happens when we
		// stop offering an option": the widget keeps showing it, marked
		// retired.
		"fw-dev-1": {
			"criticality_tier": "low",
			"is_leased":        "false",
		},
		"fw-colo-1": {
			"is_leased":        "true",
			"criticality_tier": "medium",
		},
		"sw-tor-a1": {
			"criticality_tier":   "medium",
			"power_budget_watts": "40",
		},
		"sw-tor-a2": {
			"criticality_tier":   "low",
			"power_budget_watts": "40",
		},

		// The DR site: owned metal in a rented building.
		"dr-rack-01": {
			"procurement_ref": "PO-2024-0777",
			"is_leased":       "false",
		},
		"sw-dr-1": {
			"criticality_tier":   "high",
			"power_budget_watts": "90",
		},
		"fw-dr-1": {
			"criticality_tier": "high",
			"is_leased":        "false",
		},
		"fw-stg-1": {
			"criticality_tier": "low",
			"is_leased":        "false",
		},

		// The three new clusters this company owns outright.
		"hv-win-01": {
			"criticality_tier":   "high",
			"is_leased":          "false",
			"power_budget_watts": "800",
		},
		"hv-esx-01": {
			"criticality_tier": "medium",
			"is_leased":        "false",
		},
		"hv-dr-01": {
			"criticality_tier": "high",
			"is_leased":        "false",
		},

		// The rented and cloud tier: leased by definition, nothing owned
		// here to give a procurement reference to.
		"srv-hz-1": {
			"is_leased":        "true",
			"criticality_tier": "low",
		},
		"srv-hz-2": {
			"is_leased":        "true",
			"criticality_tier": "low",
		},
		"vps-mon-1": {
			"is_leased":        "true",
			"criticality_tier": "medium",
		},
		"bkp-hz-1": {
			"is_leased":        "true",
			"criticality_tier": "medium",
		},
	}

	for name, vals := range assetValues {
		resolved := make(map[string]string, len(vals))
		for code, v := range vals {
			resolved[assetFieldIDs[code]] = v
		}
		if err := setAssetCustomValues(ctx, s, actor, name, resolved); err != nil {
			return fmt.Errorf("staging the demo's custom field spread: %w", err)
		}
	}

	// ---------- services ----------
	//
	// Most of the estate, not all of it: mimir-ingester and orders-api-dev
	// are left with no values, the same honest gap as an asset nobody has
	// gotten to yet. backup-agent stays untouched -- StageCustomFields'
	// deliberately empty case.
	serviceValues := map[string]map[string]string{
		"pgsql-core": {
			"runbook_url":     "https://wiki.example.internal/runbooks/pgsql-core",
			"sla_minutes":     "30",
			"last_dr_test":    domain.FormatDate(now.AddDate(0, 0, -45)),
			"customer_facing": "false",
			"support_tier":    "gold",
		},
		// The edge load balancer that nobody owns (seed_services.go): no
		// runbook, no DR test on record either -- the same gap the fixture's
		// team field already makes, told again through a different field.
		"haproxy-edge": {
			"sla_minutes":     "10",
			"customer_facing": "true",
			"support_tier":    "gold",
		},
		"orders-api": {
			"runbook_url":     "https://wiki.example.internal/runbooks/orders-api",
			"sla_minutes":     "30",
			"customer_facing": "true",
			"support_tier":    "gold",
		},
		"orders-web": {
			"customer_facing": "true",
			"support_tier":    "silver",
		},
		"sso": {
			"runbook_url":     "https://wiki.example.internal/runbooks/sso",
			"sla_minutes":     "30",
			"last_dr_test":    domain.FormatDate(now.AddDate(0, 0, -100)),
			"customer_facing": "true",
			"support_tier":    "gold",
		},
		"rabbitmq": {
			"runbook_url":  "https://wiki.example.internal/runbooks/rabbitmq",
			"support_tier": "silver",
		},
		"partner-gateway": {
			"runbook_url":     "https://wiki.example.internal/runbooks/partner-gateway",
			"sla_minutes":     "60",
			"customer_facing": "true",
			"support_tier":    "gold",
		},
		"log-shipper": {
			"support_tier":    "bronze",
			"customer_facing": "false",
		},
	}

	for code, vals := range serviceValues {
		resolved := make(map[string]string, len(vals))
		for fcode, v := range vals {
			resolved[serviceFieldIDs[fcode]] = v
		}
		if err := setServiceCustomValues(ctx, s, actor, code, resolved); err != nil {
			return fmt.Errorf("staging the demo's custom field spread: %w", err)
		}
	}

	// ---------- the retired field ----------
	//
	// A ticketing reference from before the estate moved to its current
	// system: exactly the "we stopped using this, what happens to the old
	// values" question a client asks. Populated first, retired second.
	//
	// WHERE THIS SHOWS, AND WHERE IT DELIBERATELY DOES NOT. A retired FIELD
	// vanishes from the asset pages entirely -- do not expect these three
	// references to appear there, and do not "fix" it if they do not.
	// Keeping a retired field's value off every surface is what PROTECTS it:
	// a value the form draws is a value a clear-all can enumerate and
	// destroy, so it is drawn nowhere (docs/custom-fields-design.md, "a
	// submission may only name what the operator was shown"). The
	// demonstration is on /custom-fields, which lists this field as retired
	// with "3 values retained" and a Restore button -- storage kept, surface
	// withdrawn, reversible. That is the honest answer to the client's
	// question, and it is a better one than "the values still show".
	//
	// The retired OPTION below is the opposite case and the one where a
	// value does keep displaying. The two read alike and behave oppositely.
	platformTeamID, err := lookupTeamID(ctx, s, "platform")
	if err != nil {
		return fmt.Errorf("resolving the platform team for the legacy ticket reference field: %w", err)
	}
	legacy, err := domain.NewCustomField(store.NewID(), domain.CustomFieldEntityAsset,
		"legacy_ticket_ref", "Legacy Ticket Reference", domain.CustomFieldText,
		"the reference in the ticketing system this estate used before the 2024 migration; "+
			"kept for lookup on older assets, nothing new is filed against it", admin.ID, platformTeamID, now)
	if err != nil {
		return fmt.Errorf("building the legacy ticket reference field: %w", err)
	}
	if err := s.CreateCustomField(ctx, actor, legacy); err != nil {
		return fmt.Errorf("staging the legacy ticket reference field: %w", err)
	}
	legacyValues := map[string]string{
		"hv-01":     "RT-2019-00231",
		"sw-core-1": "RT-2020-00877",
		"pdu-a1":    "RT-2018-00042",
	}
	for name, ref := range legacyValues {
		if err := setAssetCustomValues(ctx, s, actor, name, map[string]string{legacy.ID: ref}); err != nil {
			return fmt.Errorf("staging legacy ticket references: %w", err)
		}
	}
	if err := s.RetireCustomField(ctx, actor, legacy.ID); err != nil {
		return fmt.Errorf("retiring the legacy ticket reference field: %w", err)
	}

	// ---------- the retired option ----------
	//
	// criticality_tier drops "low": three tiers turned out to be one too
	// many in practice -- nothing distinguished "low" from "no tier", and
	// administrators kept picking it out of habit. fw-dev-1 and sw-tor-a2,
	// set above, still hold it, so the option keeps displaying on their
	// pages, marked retired, exactly as design.md §3 describes -- and it is
	// the single clearest way to show off B1's fix live.
	critField, err := s.GetCustomField(ctx, assetFieldIDs["criticality_tier"])
	if err != nil {
		return fmt.Errorf("reading criticality_tier to retire its low option: %w", err)
	}
	remaining := selectOptions(optionSpec{"medium", "Medium"}, optionSpec{"high", "High"})
	if err := s.SetCustomFieldOptions(ctx, actor, critField.ID, critField.RowVersion, remaining); err != nil {
		return fmt.Errorf("retiring the low criticality_tier option: %w", err)
	}

	// ---------- the ownership report's own gaps (WP-G7) ----------
	//
	// StageCustomFields already stages the two states its OWN fixture
	// assertion counts exactly (TestTheSeedFixtureCoversEveryCustomFieldKind's
	// neighbour, in customfields_test.go: "1 unowned", "1 owned by a
	// since-retired team") -- widening that file would mean widening that
	// assertion too, for no gain. This layer is additive and untested by
	// count, so it is where the report's other two conditions get real data
	// instead: a DEPRECATED owner (design §2's "arguably the most
	// interesting finding" -- a team on its way out that still owns
	// something, which a binary active/retired check misses silently) and
	// an ACTIVE owner nobody can reach (no contact_ref at all).
	//
	// Reassigning support_tier and sla_minutes -- already defined and
	// already carrying values across the estate -- rather than defining new
	// fields: SetCustomFieldOptions and the value maps above are untouched,
	// so this cannot disturb hv-02/vault's "every field gets a value"
	// coverage the way adding a field would.
	if err := assignAndDeprecateOwner(ctx, s, actor, serviceFieldIDs["support_tier"], "sunset-support"); err != nil {
		return fmt.Errorf("staging a deprecated owner for support_tier: %w", err)
	}
	if err := assignToUnreachableOwner(ctx, s, actor, serviceFieldIDs["sla_minutes"], "silent-ops"); err != nil {
		return fmt.Errorf("staging a contactless owner for sla_minutes: %w", err)
	}

	return nil
}

// assignAndDeprecateOwner builds a small, single-purpose ACTIVE team (so the
// reassignment itself passes requireActiveOwnerTeam), assigns it as fieldID's
// owner through the real store path, and then moves the team to DEPRECATED --
// not retired. That is the transitional case docs/ownership-report-design.md
// §2 singles out: the team has not fully gone, it still owns this, and a
// check that only tests for `lifecycle = 'retired'` would miss it silently.
func assignAndDeprecateOwner(ctx context.Context, s *store.SQLStore, actor domain.Actor, fieldID, teamCode string) error {
	team, err := domain.NewTeam(store.NewID(), domain.TeamSpec{
		Code: teamCode, Name: "Support Contract Winding Down",
		Description: str("On its way out as its work is handed off, but still named as the owner of record."),
		ContactRef:  str(teamCode + "@example.com"),
	}, s.Now())
	if err != nil {
		return fmt.Errorf("building the soon-to-deprecate team: %w", err)
	}
	if err := s.CreateTeam(ctx, domain.AdministratorPermit(actor), team); err != nil {
		return fmt.Errorf("creating the soon-to-deprecate team: %w", err)
	}

	field, err := s.GetCustomField(ctx, fieldID)
	if err != nil {
		return fmt.Errorf("reading field %s to reassign its owner: %w", fieldID, err)
	}
	field.OwnerTeamID = &team.ID
	if err := s.UpdateCustomField(ctx, actor, &field.CustomField); err != nil {
		return fmt.Errorf("reassigning field %s to the soon-to-deprecate team: %w", fieldID, err)
	}

	team.Lifecycle = domain.LifecycleDeprecated
	if err := s.UpdateTeam(ctx, domain.AdministratorPermit(actor), team); err != nil {
		return fmt.Errorf("deprecating team %s: %w", teamCode, err)
	}
	return nil
}

// assignToUnreachableOwner builds an ACTIVE team with NO contact_ref and
// assigns it as fieldID's owner -- finding 3 (design §2): the owner exists
// and can still act, and is unreachable anyway because nobody ever gave it a
// group address, a queue or a channel.
func assignToUnreachableOwner(ctx context.Context, s *store.SQLStore, actor domain.Actor, fieldID, teamCode string) error {
	team, err := domain.NewTeam(store.NewID(), domain.TeamSpec{
		Code: teamCode, Name: "Owns This, Nobody Can Reach It",
		Description: str("An active team that owns something and was never given a contact."),
	}, s.Now())
	if err != nil {
		return fmt.Errorf("building the contactless team: %w", err)
	}
	if err := s.CreateTeam(ctx, domain.AdministratorPermit(actor), team); err != nil {
		return fmt.Errorf("creating the contactless team: %w", err)
	}

	field, err := s.GetCustomField(ctx, fieldID)
	if err != nil {
		return fmt.Errorf("reading field %s to reassign its owner: %w", fieldID, err)
	}
	field.OwnerTeamID = &team.ID
	if err := s.UpdateCustomField(ctx, actor, &field.CustomField); err != nil {
		return fmt.Errorf("reassigning field %s to the contactless team: %w", fieldID, err)
	}
	return nil
}

// fieldIDsByCode indexes a ListCustomFields result by code, for callers that
// address a definition StageCustomFields already created rather than
// carrying its return value across a separate process invocation the way
// defineCustomFields' own caller can.
func fieldIDsByCode(rows []store.CustomFieldRow) map[string]string {
	ids := make(map[string]string, len(rows))
	for _, r := range rows {
		ids[r.Code] = r.ID
	}
	return ids
}
