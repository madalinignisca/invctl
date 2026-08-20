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

// selectOptions builds a select field's option list from value/label pairs,
// positioned in the order given.
func selectOptions(pairs ...string) []domain.CustomFieldOption {
	if len(pairs)%2 != 0 {
		panic("selectOptions: arguments must be value, label pairs")
	}
	out := make([]domain.CustomFieldOption, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		//nolint:gosec // G602: the even-length check above panics first, so i+1 is always in range
		out = append(out, domain.CustomFieldOption{Value: pairs[i], Label: pairs[i+1], Position: i / 2})
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
func StageCustomFields(ctx context.Context, s *store.SQLStore, adminUsername string) error {
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

	assetFields, err := defineCustomFields(ctx, s, actor, admin.ID, now, domain.CustomFieldEntityAsset, []customFieldSpec{
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
			selectOptions("low", "Low", "medium", "Medium", "high", "High")},
	})
	if err != nil {
		return err
	}

	serviceFields, err := defineCustomFields(ctx, s, actor, admin.ID, now, domain.CustomFieldEntityService, []customFieldSpec{
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
			selectOptions("bronze", "Bronze", "silver", "Silver", "gold", "Gold")},
	})
	if err != nil {
		return err
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
func defineCustomFields(ctx context.Context, s *store.SQLStore, actor domain.Actor, createdBy string,
	now time.Time, entityType string, specs []customFieldSpec) (map[string]string, error) {
	ids := make(map[string]string, len(specs))
	for _, spec := range specs {
		f, err := domain.NewCustomField(store.NewID(), entityType, spec.code, spec.label,
			spec.kind, spec.description, createdBy, now)
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
