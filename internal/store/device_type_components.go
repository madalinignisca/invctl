// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The store surface for a device type's component template (migration
// 00067): the ports every instance of a model has. Task 4 reads
// ListDeviceTypeComponents to seed the real rows an asset gets when it is
// created from a device type -- nothing here instantiates anything.

// ComponentSpec is what a form submits to add one or more template entries in
// a single call: a name that may carry a range (domain.ExpandRange), plus
// every kind-specific field NewDeviceTypeComponent needs.
//
// ONE SPEC PRODUCES ROWS OF ONE KIND. An interface batch and a power_input
// batch are two separate calls, because the kind decides which of the
// remaining fields is even meaningful -- domain.DeviceTypeComponent.Validate's
// cross-check would refuse a spec that tried to mix them, so there is no
// value in a shape that could ask for both at once.
type ComponentSpec struct {
	Kind string
	// NameSpec is a literal name ("psu1") or a range ("Ethernet1/[1-48]"),
	// exactly what domain.ExpandRange accepts.
	NameSpec string

	// Interface-shaped. FormFactor is required when Kind is interface --
	// enforced by domain.NewDeviceTypeComponent, not re-checked here.
	FormFactor *string
	SpeedMbps  *int
	IsMgmt     bool
}

// deviceTypeComponentSelect is unqualified because the table carries no join
// this package needs -- the same shape as endpointSelect before it grew one.
const deviceTypeComponentSelect = `SELECT * FROM device_type_component`

// ListDeviceTypeComponents returns a type's active template entries, ordered
// the way an elevation or a form renders them: grouped by kind, then by the
// position ExpandRange assigned, then by name as the final tie-break.
//
// position IS load-bearing here, not merely a display nicety: a lexical sort
// puts "Ethernet1/10" before "Ethernet1/2", and position is what the create
// path assigns from expansion order specifically so this ORDER BY does not
// have to parse names to get that right (migration 00067's header).
func (s *SQLStore) ListDeviceTypeComponents(ctx context.Context, deviceTypeID string) ([]domain.DeviceTypeComponent, error) {
	var rows []domain.DeviceTypeComponent
	err := s.read(ctx, &rows, deviceTypeComponentSelect+`
		WHERE device_type_id = ? AND lifecycle = ?
		ORDER BY kind, position, name`, deviceTypeID, domain.LifecycleActive)
	if err != nil {
		return nil, fmt.Errorf("listing components of device type %s: %w", deviceTypeID, err)
	}
	return rows, nil
}

// getDeviceTypeComponent loads one template entry by id, retired or not --
// the internal counterpart of GetDeviceType, used by Update and Retire below
// to read the row they are about to change.
func (s *SQLStore) getDeviceTypeComponent(ctx context.Context, id string) (*domain.DeviceTypeComponent, error) {
	var c domain.DeviceTypeComponent
	if err := s.readOne(ctx, &c, deviceTypeComponentSelect+` WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting device type component %s: %w", id, err)
	}
	return &c, nil
}

// componentBatchAudit is the audited shape of a CreateDeviceTypeComponents
// call: not one row, but the operator action that produced however many rows
// ExpandRange returned.
//
// ONE change_log ENTRY FOR THE WHOLE BATCH, DELIBERATELY -- the brief's own
// framing: "48 ports are one operator action and one audit story, not 48."
// Forty-eight individual create entries would bury the one thing a reader
// actually wants, which is "an operator declared this range on this model",
// under forty-eight repetitions of it. Names is sorted and joined the way
// auditedDependency sorts data classes, so the entry is stable and readable
// rather than an opaque count.
type componentBatchAudit struct {
	DeviceTypeID string  `db:"device_type_id"`
	Kind         string  `db:"kind"`
	NameSpec     string  `db:"name_spec"`
	Names        string  `db:"names"`
	Count        int     `db:"count"`
	FormFactor   *string `db:"form_factor"`
	SpeedMbps    *int    `db:"speed_mbps"`
	IsMgmt       bool    `db:"is_mgmt"`
}

func auditedComponentBatch(deviceTypeID string, spec ComponentSpec, created []*domain.DeviceTypeComponent) *componentBatchAudit {
	names := make([]string, len(created))
	for i, c := range created {
		names[i] = c.Name
	}
	return &componentBatchAudit{
		DeviceTypeID: deviceTypeID,
		Kind:         spec.Kind,
		NameSpec:     spec.NameSpec,
		Names:        strings.Join(names, ","),
		Count:        len(created),
		FormFactor:   spec.FormFactor,
		SpeedMbps:    spec.SpeedMbps,
		IsMgmt:       spec.IsMgmt,
	}
}

// CreateDeviceTypeComponents expands spec.NameSpec and inserts every
// resulting row for one device type in ONE transaction.
//
// ONE TRANSACTION FOR THE WHOLE EXPANSION, not one per name -- the brief's
// framing again: 48 ports are one operator action, so they either all land or
// none do, and they produce exactly one change_log row (componentBatchAudit
// above), not forty-eight.
//
// position is assigned from the expansion order, continuing after whatever is
// already declared for this (device_type_id, kind) pair rather than
// restarting at 0 -- a second batch of interfaces added later must sort after
// the first, not interleave with it by chance.
func (s *SQLStore) CreateDeviceTypeComponents(ctx context.Context, p domain.Permit, deviceTypeID string, spec ComponentSpec) error {
	names, err := domain.ExpandRange(spec.NameSpec)
	if err != nil {
		return fmt.Errorf("expanding %q: %w", spec.NameSpec, err)
	}
	now := s.now()

	return s.write(ctx, p, func(t *tx) error {
		var nextPosition int
		if err := t.get(ctx, &nextPosition, `
			SELECT COALESCE(MAX(position), -1) + 1 FROM device_type_component
			WHERE device_type_id = ? AND kind = ?`, deviceTypeID, spec.Kind); err != nil {
			return fmt.Errorf("finding the next position for device type %s: %w", deviceTypeID, err)
		}

		created := make([]*domain.DeviceTypeComponent, 0, len(names))
		for i, name := range names {
			c, err := domain.NewDeviceTypeComponent(NewID(), domain.DeviceTypeComponentSpec{
				DeviceTypeID: deviceTypeID,
				Kind:         spec.Kind,
				Name:         name,
				Position:     nextPosition + i,
				FormFactor:   spec.FormFactor,
				SpeedMbps:    spec.SpeedMbps,
				IsMgmt:       spec.IsMgmt,
			}, now)
			if err != nil {
				return err
			}
			_, err = t.exec(ctx, `
				INSERT INTO device_type_component
					(id, device_type_id, kind, name, position, form_factor, speed_mbps,
					 is_mgmt, lifecycle, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.ID, c.DeviceTypeID, c.Kind, c.Name, c.Position, c.FormFactor, c.SpeedMbps,
				c.IsMgmt, c.Lifecycle, c.CreatedAt, c.UpdatedAt)
			if err != nil {
				return translateWriteErr(err, "creating device type component")
			}
			created = append(created, c)
		}

		return t.logCreate(ctx, "device_type_component", deviceTypeID,
			auditedComponentBatch(deviceTypeID, spec, created))
	})
}

// UpdateDeviceTypeComponent persists field changes to one template entry.
//
// DeviceTypeID and Kind are carried over from the stored row, never taken
// from the caller -- the same rule UpdateDeviceType applies to
// manufacturer_id. Kind decides which columns Validate treats as meaningful
// and which real table Task 4 instantiates a row into; flipping it, or moving
// the row to a different device type, is not an edit to this entry, it is
// declaring a different one. Correct a mis-typed kind by retiring the row and
// declaring the right one.
func (s *SQLStore) UpdateDeviceTypeComponent(ctx context.Context, p domain.Permit, c *domain.DeviceTypeComponent) error {
	before, err := s.getDeviceTypeComponent(ctx, c.ID)
	if err != nil {
		return err
	}
	c.DeviceTypeID = before.DeviceTypeID
	c.Kind = before.Kind
	// LIFECYCLE IS CARRIED FROM THE STORED ROW, and the UPDATE below does not
	// name the column either. Both, deliberately.
	//
	// Without the pin this correction path is a SECOND WITHDRAWAL PATH with
	// none of RetireDeviceTypeComponent's meaning -- and worse in the other
	// direction, because a submitted 'active' would reactivate a component
	// somebody withdrew, silently putting a port back on every asset the model
	// instantiates from tomorrow.
	//
	// Dropping it from the SET keeps the ROW safe; pinning the struct keeps the
	// AUDIT safe, because logUpdate diffs the structs and would otherwise
	// record a withdrawal that never happened. Five sibling methods in this
	// repo pin the struct alone, because their UPDATE never named the column;
	// this one did, so it needs both.
	c.Lifecycle = before.Lifecycle
	if err := c.Validate(); err != nil {
		return err
	}
	c.CreatedAt = before.CreatedAt
	c.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE device_type_component
			SET name = ?, position = ?, form_factor = ?, speed_mbps = ?, is_mgmt = ?,
			    updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			c.Name, c.Position, c.FormFactor, c.SpeedMbps, c.IsMgmt,
			c.UpdatedAt, c.ID, c.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating device type component")
		}
		if err := requireVersion(res, "device_type_component", c.ID, &c.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "device_type_component", c.ID, before, c)
	})
}

// RetireDeviceTypeComponent withdraws one template entry.
//
// SOFT DELETE ONLY, matching every other lifecycle in this schema: the row
// stays, its unique slot on (device_type_id, kind, name) frees up because the
// index is scoped to lifecycle = 'active' (migration 00067), and a corrected
// datasheet can redeclare the same name under a fresh row.
func (s *SQLStore) RetireDeviceTypeComponent(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.getDeviceTypeComponent(ctx, id)
	if err != nil {
		return err
	}
	if before.IsRetired() {
		// Already withdrawn: nothing changed, so nothing to log -- a second
		// audit entry would claim a withdrawal that did not happen.
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, p, func(t *tx) error {
		if _, err := t.exec(ctx,
			`UPDATE device_type_component SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			 WHERE id = ?`, domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring device type component")
		}
		after := *before
		after.Lifecycle = domain.LifecycleRetired
		after.UpdatedAt = at
		return t.logUpdate(ctx, "device_type_component", id, before, &after)
	})
}

// ---------- Task 4: bringing a device type's template into existence ----------

// newInterfaceFromTemplate turns one interface-kind template component into
// the live domain.Interface it becomes on a real asset. id and at are
// supplied by the caller, per package convention.
//
// THE ONE PLACE THIS MAPPING IS WRITTEN. instantiateComponents (below) is one
// caller, at asset-creation time; Task 5 ("add a component an asset is
// missing, after its device type's template gained one") is the other,
// reusing this exact function rather than a second copy of the field
// mapping. Two copies drift, and the one that drifts is the rarely-run path
// -- which is also the one that can overwrite an operator's own data on an
// asset that already exists. Do not inline this into either caller.
func newInterfaceFromTemplate(id, assetID string, c domain.DeviceTypeComponent, at string) (*domain.Interface, error) {
	// FormFactor is required on an interface component and enforced twice
	// already -- domain.DeviceTypeComponent.Validate's cross-check, and the
	// dtc_interface_form_factor_check CHECK constraint (migration 00067) --
	// so a nil here would mean one of those two has a bug, not that this
	// function needs a third defence. Dereferencing directly, deliberately.
	iface, err := domain.NewInterface(id, assetID, c.Name, *c.FormFactor)
	if err != nil {
		return nil, fmt.Errorf("instantiating interface %q from device type component %s: %w",
			c.Name, c.ID, err)
	}
	iface.SpeedMbps = c.SpeedMbps
	iface.IsMgmt = c.IsMgmt
	iface.RowVersion = 1
	iface.CreatedAt, iface.UpdatedAt = &at, &at
	return iface, nil
}

// instantiateInterfaceComponents inserts and audits one live interface row
// for each entry in components, on assetID, inside the SAME transaction t --
// the shared path newInterfaceFromTemplate's own doc comment promises:
// instantiateComponents (below) is one caller, handing it every active
// template entry because the asset is brand new and nothing can already
// exist on it; ApplyTemplate is the other, handing it only the entries the
// caller has already determined the asset is missing. Neither caller
// re-implements the mapping or the INSERT.
//
// ids IS PARALLEL TO components, NOT MINTED HERE. Both callers need to know
// the ids they are about to write BEFORE this runs: ApplyTemplate has to
// hand them to applyTemplateSubject to mint a permit scoped to exactly these
// rows before the transaction opens (domain.scopedPermit is immutable for
// the life of a transaction -- see its own doc comment on the mutable-state
// design this replaced). Minting them here instead would make that
// impossible for one caller in order to save the other a three-line loop.
//
// EVERY ENTRY IS ASSUMED INTERFACE-KIND. Both callers filter to
// domain.ComponentKindInterface before calling; a caller that hands this a
// different kind gets a wrapped error rather than a silently skipped row,
// because a mismatch here is a bug in the caller's filter, not a case to
// tolerate. A device type's template carries interfaces only in any case --
// see migration 00067's header for why power_input was removed from the
// kind vocabulary entirely rather than accepted and left uninstantiated.
func (s *SQLStore) instantiateInterfaceComponents(ctx context.Context, t *tx, assetID string, components []domain.DeviceTypeComponent, ids []string, at string) (int, error) {
	added := 0
	for i, c := range components {
		if c.Kind != domain.ComponentKindInterface {
			return added, fmt.Errorf(
				"instantiating device type component %s onto asset %s: kind %q is not %q",
				c.ID, assetID, c.Kind, domain.ComponentKindInterface)
		}
		iface, err := newInterfaceFromTemplate(ids[i], assetID, c, at)
		if err != nil {
			return added, err
		}
		_, err = t.exec(ctx, `
			INSERT INTO interface (id, asset_id, name, form_factor, speed_mbps, mac, mtu,
			                       lag_parent_id, is_mgmt, enabled, lifecycle,
			                       created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			iface.ID, iface.AssetID, iface.Name, iface.FormFactor, iface.SpeedMbps, iface.MAC, iface.MTU,
			iface.LagParentID, iface.IsMgmt, iface.Enabled, iface.Lifecycle, iface.CreatedAt, iface.UpdatedAt)
		if err != nil {
			return added, translateWriteErr(err, "instantiating interface from device type template")
		}
		if err := t.logCreate(ctx, "interface", iface.ID, iface); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

// instantiateComponents seeds an asset's real component rows from its device
// type's active template entries (Task 4), inside the SAME transaction t as
// the asset's own INSERT -- see insertAsset, the only caller. Every row this
// writes is declared state, exactly as if an operator had added it by hand
// through CreateInterface, and gets its own change_log entry: the audit rule
// has no exception for a row a template merely suggested.
//
// NOTHING SPECIAL HAPPENS FOR A TYPE WITH NO TEMPLATE, OR NO TYPE AT ALL --
// ListDeviceTypeComponents-equivalent read returns zero rows and the loop
// below does nothing, so every existing caller (CreateAsset,
// CreateAssetInProject, both importers) is unaffected unless it names a
// device_type_id that actually carries a template. That is the regression
// this task's brief calls out by name.
//
// REACTIVATION AND VOCABULARY-EXISTENCE ARE NOT RE-CHECKED HERE, unlike
// CreateInterface. Reactivation exists because a NAME can collide with a
// RETIRED port on the SAME asset; a.ID was minted moments ago for this one
// INSERT and nothing has ever written against it, so there is no retired
// port to collide with. Vocabulary existence is guaranteed a different way:
// device_type_component.form_factor already carries a REFERENCES
// interface_form_factor(code) (migration 00067), so a value that passed
// CreateDeviceTypeComponents cannot later name a code that does not exist --
// the vocabulary row cannot be deleted out from under a template that still
// references it.
//
// A DEVICE TYPE'S TEMPLATE CARRIES INTERFACES ONLY -- see migration 00067's
// header for why power inputs were excluded from the kind vocabulary
// entirely, rather than accepted and left uninstantiated here.
//
// plannedIDs IS NIL FOR EVERY CALLER EXCEPT CreateAssetInProject. nil means
// "mint one id per interface component right here"; a non-nil slice means
// the caller already minted these (with NewID(), before this transaction
// opened) and folded them into the permit this transaction runs under --
// CreateAssetInProject has to, because domain.scopedPermit.Covers can only
// authorize an id that is already in its scope, and "interface" classifies
// ScopeSubjectDerived (domain/role.go). See that method's own comment for
// the regression this parameter fixes.
//
// A LENGTH MISMATCH IS A CONFLICT, NOT A PANIC. plannedIDs is positional
// against the interface-kind components read here a moment ago, but that
// read is a SECOND read of the same table -- CreateAssetInProject read it
// once, before the transaction, to mint ids and build the permit; this reads
// it again, inside the transaction, the same way every other caller of this
// function always has. Nothing serializes the two, so a template edited
// between them is a genuine (if narrow) race, and indexing ids[i] blind
// would either panic or silently mismatch a minted id to the wrong
// component. Erroring here instead turns a data race into a transaction
// rollback, which is the property this whole package treats as the correct
// failure shape for a lost race.
func (s *SQLStore) instantiateComponents(ctx context.Context, t *tx, a *domain.Asset, plannedIDs []string) error {
	if a.DeviceTypeID == nil {
		return nil
	}
	var components []domain.DeviceTypeComponent
	if err := t.selectAll(ctx, &components, deviceTypeComponentSelect+`
		WHERE device_type_id = ? AND lifecycle = ?
		ORDER BY kind, position, name`, *a.DeviceTypeID, domain.LifecycleActive); err != nil {
		return fmt.Errorf("reading device type %s's component template: %w", *a.DeviceTypeID, err)
	}
	// FILTERED TO INTERFACE-KIND BEFORE THE SHARED PATH SEES IT --
	// instantiateInterfaceComponents errors on anything else rather than
	// skipping it; see that function's own doc comment.
	ifaces := make([]domain.DeviceTypeComponent, 0, len(components))
	for _, c := range components {
		if c.Kind == domain.ComponentKindInterface {
			ifaces = append(ifaces, c)
		}
	}
	var ids []string
	if plannedIDs != nil {
		if len(plannedIDs) != len(ifaces) {
			return fmt.Errorf(
				"instantiating device type %s's component template onto asset %s: "+
					"planned %d interface id(s) but the template now has %d active interface "+
					"component(s), which is a lost race between reading it and writing it: %w",
				*a.DeviceTypeID, a.ID, len(plannedIDs), len(ifaces), domain.ErrConflict)
		}
		ids = plannedIDs
	} else {
		ids = make([]string, len(ifaces))
		for i := range ids {
			ids[i] = NewID()
		}
	}
	at := domain.FormatTime(s.now())
	_, err := s.instantiateInterfaceComponents(ctx, t, a.ID, ifaces, ids, at)
	return err
}

// ---------- Task 5: backfilling a template onto an asset that already exists ----------

// applyTemplateSubject is ApplyTemplate's counterpart to
// authorizeInterfaceSubject: the caller's own permit must already cover the
// asset, checked as ApplyTemplate's first statement, before anything is read, and the permit ApplyTemplate's
// transaction actually runs under is scoped narrowly to the specific
// interface ids this call is about to mint -- ScopeSubjectDerived, so
// Covers only ever admits an id the store put there itself, never one a
// caller could have supplied.
func applyTemplateSubject(p domain.Permit, assetID string, newInterfaceIDs []string) (domain.Permit, error) {
	if !p.Covers("asset", assetID) {
		return nil, fmt.Errorf("applying device type template to asset %s: %w", assetID, domain.ErrForbidden)
	}
	ids := make(map[string]bool, len(newInterfaceIDs))
	for _, id := range newInterfaceIDs {
		ids[id] = true
	}
	return domain.ScopedPermit(p.Actor(), nil, domain.ScopedEntities{
		"interface": ids,
	}), nil
}

// ApplyTemplate backfills assetID with whatever interface its device type's
// active template names that the asset does not already carry an interface
// of that name for -- added and audited exactly as instantiateComponents
// would have done at create time, through the same
// instantiateInterfaceComponents this shares with it. It never removes or
// overwrites anything; added counts only the rows this call actually
// inserted, for a caller (Task 8's control) that wants to say "added 6
// ports" rather than just "done".
//
// A COMPONENT SOMEBODY ALREADY RECORDED IS THEIRS, WHATEVER THE TEMPLATE
// SAYS ABOUT IT NOW. Matching is by name against every interface row the
// asset already has -- ACTIVE OR RETIRED, deliberately, not just the active
// ones ListInterfaces would show. Two reasons, and either alone would be
// enough:
//
//  1. The database will not let a plain INSERT collide with a retired name
//     anyway -- interface's UNIQUE (asset_id, name) (migration shared/00002)
//     is a TABLE constraint, not a partial index scoped to lifecycle, the
//     same fact CreateInterface's own reactivation comment leans on. Trying
//     to insert eth0 here while a retired eth0 already exists fails the
//     constraint, full stop.
//
//  2. Even if it did not: a retired port is a person's decision, made
//     through RetireInterface, that this physical port is gone. Reactivating
//     it is CreateInterface's job, triggered by a person re-adding it BY
//     NAME -- a deliberate act, not a side effect of backfilling a template
//     onto an estate that predates the feature. This call never reactivates
//     anything; a retired name is treated as "already accounted for" and
//     left exactly as it is, same as a live one. The person who withdrew the
//     port is the one who gets to put it back.
//
// A NAME'S FORM FACTOR, SPEED OR MGMT FLAG IS NEVER COMPARED OR CORRECTED
// against what the template currently says -- only NAME decides "already
// there". eth0 recorded as sfp28 while the template still says rj45 stays
// sfp28, untouched, no change_log row, exactly the brief's own worked
// example. Silently overwriting an operator's own record is worse than the
// feature not existing at all.
func (s *SQLStore) ApplyTemplate(ctx context.Context, p domain.Permit, assetID string) (int, error) {
	// THE CHECK IS THE FIRST STATEMENT, on the CALLER'S OWN permit, before a
	// single row is read. CreateAssetInProject documents the same requirement
	// and for the same reason.
	//
	// It used to sit further down, after the asset read, the template read, the
	// interface read and three `return 0, nil` exits -- which made a foreign
	// asset answer THREE ways instead of one: forbidden when something was
	// missing, but a silent success when the asset had no device type, or no
	// template, or was already complete. An out-of-scope caller could tell
	// those apart and learn, one asset at a time, which assets carry a
	// templated model and which are missing ports from it. That distinguishable
	// third answer is exactly what
	// TestRetireCostAuthorizationRunsBeforeTheAlreadyRetiredCheck was written
	// to forbid elsewhere in this package.
	//
	// applyTemplateSubject checks it again before minting. That is not
	// redundant: the census in permit_source_test.go can only see the minter,
	// so the minter has to carry the check for the guard to mean anything.
	if !p.Covers("asset", assetID) {
		return 0, domain.ErrForbidden
	}

	var row struct {
		DeviceTypeID *string `db:"device_type_id"`
	}
	if err := s.readOne(ctx, &row, `SELECT device_type_id FROM asset WHERE id = ?`, assetID); err != nil {
		return 0, fmt.Errorf("reading asset %s's device type: %w", assetID, err)
	}
	if row.DeviceTypeID == nil {
		return 0, nil
	}

	components, err := s.ListDeviceTypeComponents(ctx, *row.DeviceTypeID)
	if err != nil {
		return 0, err
	}
	if len(components) == 0 {
		return 0, nil
	}

	// EVERY EXISTING NAME, ANY LIFECYCLE -- see ApplyTemplate's own doc
	// comment for why retired counts as "already there".
	var existingNames []string
	if err := s.read(ctx, &existingNames,
		`SELECT name FROM interface WHERE asset_id = ?`, assetID); err != nil {
		return 0, fmt.Errorf("reading asset %s's existing interfaces: %w", assetID, err)
	}
	have := make(map[string]bool, len(existingNames))
	for _, name := range existingNames {
		have[name] = true
	}

	missing := make([]domain.DeviceTypeComponent, 0, len(components))
	for _, c := range components {
		if c.Kind == domain.ComponentKindInterface && !have[c.Name] {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}

	// The ids this call will write have to be known BEFORE the permit is
	// minted -- see applyTemplateSubject's own comment, and
	// domain.scopedPermit's rejected-earlier-design comment on why a
	// permit cannot be told "trust whatever this transaction mints" instead.
	newIDs := make([]string, len(missing))
	for i := range missing {
		newIDs[i] = NewID()
	}
	txPermit, err := applyTemplateSubject(p, assetID, newIDs)
	if err != nil {
		return 0, err
	}

	at := domain.FormatTime(s.now())
	added := 0
	err = s.write(ctx, txPermit, func(t *tx) error {
		n, ierr := s.instantiateInterfaceComponents(ctx, t, assetID, missing, newIDs, at)
		added = n
		return ierr
	})
	if err != nil {
		// ZERO ON FAILURE. `added` is a partial count from inside a transaction
		// that has just rolled back, so returning it alongside the error tempts
		// a caller into saying "added 3 ports" next to a failure, about three
		// ports that do not exist.
		return 0, err
	}
	return added, nil
}
