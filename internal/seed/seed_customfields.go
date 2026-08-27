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
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// ---------- the estate's own attributes ----------
//
// WP-A4, custom fields: what an administrator can declare that invctl itself
// does not model. Every kind lands on both an asset field and a service field
// -- a field of a kind nobody exercises is a field no test has checked, which
// is how WP-A2 published a column nobody had checked. The select kind carries
// real options rather than an empty list, for the same reason.
//
// hv-02 and vault hold a value for every field this phase defines. sw-oob-1
// and backup-agent hold none of them, DELIBERATELY -- the null-everywhere
// case is a real state a detail page has to render and needs a fixture entity
// that is actually in it.

// customFieldSpec is one definition this phase declares.
type customFieldSpec struct {
	code, label, kind, description string
	// options is set only for the select kind.
	options []domain.CustomFieldOption
}

// optionSpec is one value/label pair for selectOptions. A STRUCT RATHER THAN
// A FLAT PAIRS LIST, DELIBERATELY: CLAUDE.md forbids panic outside main, and
// this repo has no exception for "an odd-length argument list is a caller
// bug". A flat list can only fail an even-length check at runtime; a slice of
// this two-field struct makes an unpaired value unrepresentable at compile
// time instead, so there is no error to invent and nothing to check.
type optionSpec struct{ value, label string }

// selectOptions builds a select field's option list, positioned in the order
// given.
func selectOptions(specs ...optionSpec) []domain.CustomFieldOption {
	out := make([]domain.CustomFieldOption, 0, len(specs))
	for i, s := range specs {
		out = append(out, domain.CustomFieldOption{Value: s.value, Label: s.label, Position: i})
	}
	return out
}

// StageCustomFields declares WP-A4's demo fixture: an estate's own custom
// field definitions, attached to entities the base estate already contains.
//
// A SEPARATE PHASE FROM Load, AND CALLED ONLY AFTER A REAL ADMINISTRATOR
// EXISTS -- the identical shape and the identical reason as StageDemoOverride
// below. custom_field.created_by is `REFERENCES app_user(id)` (migration
// 00051): unlike change_log.actor, which is opaque and carries no such
// constraint, this column needs a genuine row to point at. Folding this into
// Load's declared phases -- which runs before cmd/invctl ever creates the
// seeded administrator, see ensureAdmin, called only afterwards -- would
// either fail outright against the foreign key or force Load to invent its
// own throwaway app_user. The latter is worse than a failure: ensureAdmin's
// bootstrap check is "does any app_user exist yet", so a phantom account
// created inside Load would make it skip creating the administrator this
// whole demo authenticates as. See docs/custom-fields-design.md §4.
//
// Idempotent, the same way loadDemoData is: an estate that already carries a
// live asset custom field is left alone rather than colliding on the unique
// (entity_type, code) index, so a repeated dev-mode start does not fail.
//
// THAT GUARD IS COARSE, AND KNOWINGLY SO: IT CANNOT TELL "fully staged" FROM
// "partially staged". A process that dies partway through -- after the asset
// fields exist but before the service fields or the values are written --
// leaves the demo permanently half-seeded, because the next start sees a
// live asset field and skips the whole function, silently, with a one-time
// slog.Warn at the call site as the only evidence anything went wrong. This
// is the same risk class already accepted for StageDemoOverride below, for
// the same reason: it is presentation data for a demo, not declared state an
// operator depends on, and a human re-running `make seed-topup`-adjacent
// tooling or dropping the database is an acceptable answer. Not re-engineered
// here on purpose.
func StageCustomFields(ctx context.Context, s *store.SQLStore, adminUsername string) error {
	// includeRetired MUST stay true. This is read-only reconnaissance, not a
	// write, but the count still has to include a field an OPERATOR retired
	// deliberately -- if it counted live rows only, a demo where every asset
	// field this phase defines had since been retired would read as "never
	// staged" and stage a fresh, colliding set on the next restart, which is
	// invctl overwriting a person's own decision. See CLAUDE.md: "Observed
	// state never becomes intent" and its sibling here -- a seed must never
	// undo one either.
	existing, err := s.ListCustomFields(ctx, domain.CustomFieldEntityAsset, true)
	if err != nil {
		return fmt.Errorf("checking for existing custom fields: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}

	admin, err := s.GetUserByUsername(ctx, adminUsername)
	if err != nil {
		return fmt.Errorf("looking up %s for custom field attribution: %w", adminUsername, err)
	}
	actor := domain.UserActor(admin)
	now := s.Now()

	// Owner teams, resolved by code -- domain.NewCustomField requires one,
	// with no escape hatch, the same rule the create form now enforces.
	// "platform" owns the asset fields (cross-cutting estate attributes,
	// the same team that owns "the shared substrate everything else runs
	// on" per seed_teams.go); "developers" owns the service fields, since
	// runbooks and DR posture are squarely an application team's business.
	platformTeamID, err := lookupTeamID(ctx, s, "platform")
	if err != nil {
		return fmt.Errorf("resolving the platform team for custom field ownership: %w", err)
	}
	developersTeamID, err := lookupTeamID(ctx, s, "developers")
	if err != nil {
		return fmt.Errorf("resolving the developers team for custom field ownership: %w", err)
	}

	assetFields, err := defineCustomFields(ctx, s, actor, admin.ID, platformTeamID, now, domain.CustomFieldEntityAsset, []customFieldSpec{
		{"procurement_ref", "Procurement Reference", domain.CustomFieldText,
			"the purchase order or vendor quote this asset was bought against", nil},
		{"power_budget_watts", "Power Budget (W)", domain.CustomFieldNumber,
			"the wattage this asset is provisioned for, for rough capacity planning outside the power chain", nil},
		{"last_physical_audit", "Last Physical Audit", domain.CustomFieldDate,
			"the date somebody last walked the rack and confirmed this asset is where the record says", nil},
		{"is_leased", "Leased", domain.CustomFieldBoolean,
			"whether this asset is leased rather than owned outright", nil},
		{"criticality_tier", "Criticality Tier", domain.CustomFieldSelect,
			"the estate's own coarse tiering, distinct from the built-in project criticality",
			selectOptions(optionSpec{"low", "Low"}, optionSpec{"medium", "Medium"}, optionSpec{"high", "High"})},
	})
	if err != nil {
		return err
	}

	serviceFields, err := defineCustomFields(ctx, s, actor, admin.ID, developersTeamID, now, domain.CustomFieldEntityService, []customFieldSpec{
		{"runbook_url", "Runbook URL", domain.CustomFieldText,
			"where the on-call runbook for this service lives", nil},
		{"sla_minutes", "SLA (minutes to restore)", domain.CustomFieldNumber,
			"the restoration target this service is held to, in minutes", nil},
		{"last_dr_test", "Last DR Test", domain.CustomFieldDate,
			"the date this service's disaster-recovery procedure was last exercised", nil},
		{"customer_facing", "Customer Facing", domain.CustomFieldBoolean,
			"whether an external customer notices this service being down", nil},
		{"support_tier", "Support Tier", domain.CustomFieldSelect,
			"the estate's own support tiering, sold separately from the built-in criticality",
			selectOptions(optionSpec{"bronze", "Bronze"}, optionSpec{"silver", "Silver"}, optionSpec{"gold", "Gold"})},
	})
	if err != nil {
		return err
	}

	// ---------- ownership is a MIX, deliberately ----------
	//
	// Every field above was just created through defineCustomFields, which
	// means every one of them has a live, active owner -- domain.NewCustomField
	// admits no other outcome. Left there, this demo estate would have
	// nothing for the ownership report the next work package builds to
	// find: that report's whole job is surfacing an UNOWNED field and a
	// field whose owner has since RETIRED, and a fixture where neither
	// condition ever occurs makes a correct empty result indistinguishable
	// from a broken one. Two of the ten fields above are deliberately
	// disturbed after the fact to give it something real to report.
	if err := orphanCustomFieldOwner(ctx, s, assetFields["power_budget_watts"]); err != nil {
		return fmt.Errorf("orphaning power_budget_watts's owner for the ownership-report fixture: %w", err)
	}
	if err := assignAndRetireOwner(ctx, s, actor, assetFields["criticality_tier"], "decommissioned"); err != nil {
		return fmt.Errorf("staging a since-retired owner for criticality_tier: %w", err)
	}

	// hv-02: every field, a value. sw-oob-1 is left untouched -- see the note
	// above.
	if err := setAssetCustomValues(ctx, s, actor, "hv-02", map[string]string{
		assetFields["procurement_ref"]:     "PO-2024-0417",
		assetFields["power_budget_watts"]:  "750",
		assetFields["last_physical_audit"]: domain.FormatDate(now.AddDate(0, 0, -30)),
		assetFields["is_leased"]:           "false",
		assetFields["criticality_tier"]:    "high",
	}); err != nil {
		return err
	}

	// vault: every field, a value. backup-agent is left untouched.
	return setServiceCustomValues(ctx, s, actor, "vault", map[string]string{
		serviceFields["runbook_url"]:     "https://wiki.example.internal/runbooks/vault",
		serviceFields["sla_minutes"]:     "15",
		serviceFields["last_dr_test"]:    domain.FormatDate(now.AddDate(0, 0, -90)),
		serviceFields["customer_facing"]: "false",
		serviceFields["support_tier"]:    "gold",
	})
}

// defineCustomFields declares one entity type's fields and returns
// code -> id, so the caller can address them by name rather than carrying ids
// around. createdBy is the app_user id the FK requires; actor is the same
// identity, used to attribute the change_log entry the way the real handler
// does (internal/web/handlers/customfields.go).
func defineCustomFields(ctx context.Context, s *store.SQLStore, actor domain.Actor, createdBy, ownerTeamID string,
	now time.Time, entityType string, specs []customFieldSpec) (map[string]string, error) {
	ids := make(map[string]string, len(specs))
	for _, spec := range specs {
		f, err := domain.NewCustomField(store.NewID(), entityType, spec.code, spec.label,
			spec.kind, spec.description, createdBy, ownerTeamID, now)
		if err != nil {
			return ids, fmt.Errorf("building custom field %s: %w", spec.code, err)
		}
		if err := s.CreateCustomField(ctx, actor, f); err != nil {
			return ids, fmt.Errorf("seeding custom field %s: %w", spec.code, err)
		}
		ids[spec.code] = f.ID

		if spec.kind == domain.CustomFieldSelect {
			if err := s.SetCustomFieldOptions(ctx, actor, f.ID, f.RowVersion, spec.options); err != nil {
				return ids, fmt.Errorf("setting options of custom field %s: %w", spec.code, err)
			}
		}
	}
	return ids, nil
}

// setAssetCustomValues writes one asset's submission through SetCustomValues,
// carrying the asset's own row_version the way a rendered form would.
func setAssetCustomValues(ctx context.Context, s *store.SQLStore, actor domain.Actor, assetName string, vals map[string]string) error {
	id, err := lookupAssetID(ctx, s, assetName)
	if err != nil {
		return fmt.Errorf("setting custom values on asset %s: %w", assetName, err)
	}
	row, err := s.GetAsset(ctx, id)
	if err != nil {
		return fmt.Errorf("reading asset %s for its custom values: %w", assetName, err)
	}
	if err := s.SetCustomValues(ctx, actor, domain.CustomFieldEntityAsset, id, row.RowVersion, vals); err != nil {
		return fmt.Errorf("setting custom values on asset %s: %w", assetName, err)
	}
	return nil
}

// setServiceCustomValues is the service half of the same thing.
func setServiceCustomValues(ctx context.Context, s *store.SQLStore, actor domain.Actor, serviceCode string, vals map[string]string) error {
	id, err := lookupServiceID(ctx, s, serviceCode)
	if err != nil {
		return fmt.Errorf("setting custom values on service %s: %w", serviceCode, err)
	}
	row, err := s.GetService(ctx, id)
	if err != nil {
		return fmt.Errorf("reading service %s for its custom values: %w", serviceCode, err)
	}
	if err := s.SetCustomValues(ctx, actor, domain.CustomFieldEntityService, id, row.RowVersion, vals); err != nil {
		return fmt.Errorf("setting custom values on service %s: %w", serviceCode, err)
	}
	return nil
}

// lookupAssetID and lookupServiceID resolve a fixture's own name or code to
// an id by direct query, the same way StageDemoOverride resolves its subject
// -- this phase runs as its own call, disconnected from Load's *Refs, so it
// cannot rely on ids Load already resolved in a different process invocation.
func lookupAssetID(ctx context.Context, s *store.SQLStore, name string) (string, error) {
	var id string
	if err := s.DB().Reader.GetContext(ctx, &id, s.DB().Rebind(
		`SELECT id FROM asset WHERE name = ?`), name); err != nil {
		return "", fmt.Errorf("finding asset %s: %w", name, err)
	}
	return id, nil
}

func lookupServiceID(ctx context.Context, s *store.SQLStore, code string) (string, error) {
	var id string
	if err := s.DB().Reader.GetContext(ctx, &id, s.DB().Rebind(
		`SELECT id FROM service WHERE code = ?`), code); err != nil {
		return "", fmt.Errorf("finding service %s: %w", code, err)
	}
	return id, nil
}

// lookupTeamID resolves a fixture team's own code to an id, the same
// disconnected-from-*Refs reasoning lookupAssetID and lookupServiceID give:
// this phase runs as its own call, after Load's *Refs has already gone out
// of scope. Used to give this phase's own custom fields an owner
// (docs/custom-fields-design.md §4 update): NewCustomField now REQUIRES one,
// with no escape hatch, and this fixture's teams (seed_teams.go) are always
// staged earlier in Load than StageCustomFields runs.
func lookupTeamID(ctx context.Context, s *store.SQLStore, code string) (string, error) {
	var id string
	if err := s.DB().Reader.GetContext(ctx, &id, s.DB().Rebind(
		`SELECT id FROM team WHERE code = ?`), code); err != nil {
		return "", fmt.Errorf("finding team %s: %w", code, err)
	}
	return id, nil
}

// orphanCustomFieldOwner reproduces, deliberately, the ONE state
// CreateCustomField and UpdateCustomField can never produce: a live field
// with no owner at all. That state is real -- it is exactly what migration
// 00054 left the eleven pre-existing fields in, because a migration cannot
// invent an owner nobody chose -- and it is the first thing the coming
// ownership report has to find. A raw UPDATE, outside the store's own
// write path and writing no change_log row, is the only way to reach it
// honestly: the real event this recreates (the migration landing) did not
// go through the application and wrote no change_log entry either.
func orphanCustomFieldOwner(ctx context.Context, s *store.SQLStore, fieldID string) error {
	_, err := s.DB().Writer.ExecContext(ctx, s.DB().Rebind(
		`UPDATE custom_field SET owner_team_id = NULL WHERE id = ?`), fieldID)
	if err != nil {
		return fmt.Errorf("nulling owner_team_id on field %s: %w", fieldID, err)
	}
	return nil
}

// assignAndRetireOwner builds a small, single-purpose team, assigns it as
// fieldID's owner through the real store path (so the reassignment itself
// is properly audited, the same way an administrator's own correction would
// be), and then retires the team -- leaving the field with an owner who is
// still named but can no longer be reached. Its OWN team, not one of the
// six the rest of this estate depends on: retiring "platform" or
// "developers" to manufacture this state would disband a team half the
// company estate still points at, which is a much bigger disturbance than
// this fixture is trying to make.
func assignAndRetireOwner(ctx context.Context, s *store.SQLStore, actor domain.Actor, fieldID, teamCode string) error {
	team, err := domain.NewTeam(store.NewID(), domain.TeamSpec{
		Code: teamCode, Name: "Decommissioned Team",
		Description: str("Existed only long enough to demonstrate a custom field whose owner has since disbanded."),
	}, s.Now())
	if err != nil {
		return fmt.Errorf("building the soon-to-retire team: %w", err)
	}
	if err := s.CreateTeam(ctx, domain.AdministratorPermit(actor), team); err != nil {
		return fmt.Errorf("creating the soon-to-retire team: %w", err)
	}

	field, err := s.GetCustomField(ctx, fieldID)
	if err != nil {
		return fmt.Errorf("reading field %s to reassign its owner: %w", fieldID, err)
	}
	field.OwnerTeamID = &team.ID
	if err := s.UpdateCustomField(ctx, actor, &field.CustomField); err != nil {
		return fmt.Errorf("reassigning field %s to the soon-to-retire team: %w", fieldID, err)
	}

	if err := s.RetireTeam(ctx, domain.AdministratorPermit(actor), team.ID); err != nil {
		return fmt.Errorf("retiring team %s: %w", teamCode, err)
	}
	return nil
}
