// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// mustDeviceTypeForComponents is a bare model to hang template entries off,
// independent of hardware_test.go's EOL-focused fixtures.
func mustDeviceTypeForComponents(t *testing.T, s *SQLStore) string {
	t.Helper()
	ctx := t.Context()
	dell := mustManufacturer(t, s, ctx, "dell-dtc", "Dell")
	return mustDeviceType(t, s, ctx, dell, "R650-dtc", nil)
}

// TestCreateDeviceTypeComponentsExpandsARange: one call, one range spec, N
// rows -- the shape Task 4's instantiation depends on.
func TestCreateDeviceTypeComponentsExpandsARange(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceType(t, s, ctx, mustManufacturer(t, s, ctx, "dell", "Dell"), "R650", nil)

			ff := "sfp28"
			err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind:       domain.ComponentKindInterface,
				NameSpec:   "Ethernet1/[1-48]",
				FormFactor: &ff,
			})
			if err != nil {
				t.Fatalf("creating components: %v", err)
			}

			rows, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing components: %v", err)
			}
			if len(rows) != 48 {
				t.Fatalf("got %d components, want 48", len(rows))
			}

			// position orders "Ethernet1/2" before "Ethernet1/10", which a
			// lexical sort of the name column would get wrong.
			idx := map[string]int{}
			for i, r := range rows {
				idx[r.Name] = i
			}
			two, twoOK := idx["Ethernet1/2"]
			ten, tenOK := idx["Ethernet1/10"]
			if !twoOK || !tenOK {
				t.Fatalf("expected both Ethernet1/2 and Ethernet1/10 among the created rows: %v", idx)
			}
			if two >= ten {
				t.Errorf("Ethernet1/2 sorted at %d, Ethernet1/10 at %d -- want 2 before 10", two, ten)
			}

			// And the list is genuinely ordered by kind, position, name --
			// not merely "2 happens to be before 10 somewhere in the slice".
			for i := 1; i < len(rows); i++ {
				if rows[i-1].Position > rows[i].Position {
					t.Fatalf("row %d (position %d) sorts after row %d (position %d)",
						i-1, rows[i-1].Position, i, rows[i].Position)
				}
			}
		})
	}
}

// TestCreateDeviceTypeComponentsAppendsPosition: a second batch on the same
// (device_type, kind) continues numbering rather than restarting at 0, so it
// sorts after the first batch instead of interleaving with it.
func TestCreateDeviceTypeComponentsAppendsPosition(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)
			ff := "rj45"

			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth[0-1]", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("first batch: %v", err)
			}
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "mgmt0", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("second batch: %v", err)
			}

			rows, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(rows) != 3 {
				t.Fatalf("got %d rows, want 3", len(rows))
			}
			// mgmt0 was declared second, so it must sort after eth0/eth1.
			if rows[len(rows)-1].Name != "mgmt0" {
				t.Errorf("last row = %s, want mgmt0 -- the second batch did not continue the sequence", rows[len(rows)-1].Name)
			}
		})
	}
}

// TestDeviceTypeComponentLiveScopedUniqueIndex: a retired name's slot is
// free, but a live duplicate is refused -- migration 00067's whole reason for
// scoping the index to lifecycle = 'active'.
func TestDeviceTypeComponentLiveScopedUniqueIndex(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)
			ff := "rj45"

			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth3", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("first declaration: %v", err)
			}

			t.Run("a live duplicate is refused", func(t *testing.T) {
				err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
					Kind: domain.ComponentKindInterface, NameSpec: "eth3", FormFactor: &ff,
				})
				if err == nil {
					t.Fatal("declaring eth3 twice while both are live succeeded")
				}
				if !errors.Is(err, domain.ErrConflict) {
					t.Errorf("error = %v, want ErrConflict", err)
				}
			})

			rows, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d live components after a refused duplicate, want 1", len(rows))
			}
			retiredID := rows[0].ID

			if err := s.RetireDeviceTypeComponent(ctx, testPermit, retiredID); err != nil {
				t.Fatalf("retiring eth3: %v", err)
			}

			t.Run("the name can be redeclared once withdrawn", func(t *testing.T) {
				if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
					Kind: domain.ComponentKindInterface, NameSpec: "eth3", FormFactor: &ff,
				}); err != nil {
					t.Fatalf("redeclaring eth3 after retirement: %v", err)
				}
				live, err := s.ListDeviceTypeComponents(ctx, dtID)
				if err != nil {
					t.Fatalf("listing: %v", err)
				}
				if len(live) != 1 {
					t.Fatalf("got %d live components after redeclaring, want 1", len(live))
				}
				if live[0].ID == retiredID {
					t.Error("the redeclared row reused the retired row's id -- it must be a new row")
				}
			})
		})
	}
}

// TestCreateDeviceTypeComponentsRequiresFormFactorForInterface: the
// constructor rule, exercised through the store path a form actually drives.
func TestCreateDeviceTypeComponentsRequiresFormFactorForInterface(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)

			err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind:     domain.ComponentKindInterface,
				NameSpec: "eth0",
				// No FormFactor.
			})
			if err == nil {
				t.Fatal("an interface component with no form factor was accepted")
			}
			var ve *domain.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want a *domain.ValidationError", err)
			}

			rows, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(rows) != 0 {
				t.Errorf("got %d rows after a refused create, want 0 -- nothing should have been inserted", len(rows))
			}
		})
	}
}

// TestCreateDeviceTypeComponentsIsOneTransaction: a bad name partway through
// an expansion must roll the WHOLE batch back, not leave the earlier names
// committed. ExpandRange itself cannot produce a mix of valid and invalid
// names, so this drives the failure the transaction boundary actually exists
// for -- a later name in the batch colliding with something the earlier
// names in the SAME call already inserted is impossible by construction
// (ExpandRange names are distinct), so instead this collides the batch
// against a component declared before the call, from partway through what
// would otherwise look like a clean range.
func TestCreateDeviceTypeComponentsIsOneTransaction(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)
			ff := "rj45"

			// eth2 already exists, so a batch of eth0..eth3 collides on the
			// third name in expansion order.
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth2", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("seeding eth2: %v", err)
			}

			err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth[0-3]", FormFactor: &ff,
			})
			if err == nil {
				t.Fatal("a batch colliding partway through succeeded")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict", err)
			}

			rows, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			// Only the pre-seeded eth2 must remain -- eth0 and eth1, which sort
			// before the collision in expansion order, must NOT have survived
			// as a partial commit.
			if len(rows) != 1 || rows[0].Name != "eth2" {
				names := make([]string, len(rows))
				for i, r := range rows {
					names[i] = r.Name
				}
				t.Errorf("rows after a failed batch = %v, want only [eth2] -- "+
					"the batch is supposed to be one transaction", names)
			}
		})
	}
}

// TestCreateDeviceTypeComponentsWritesOneChangeLogEntry: the audit rule this
// task exists to prove -- 48 ports are one operator action and one audit
// entry, not 48.
func TestCreateDeviceTypeComponentsWritesOneChangeLogEntry(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)
			ff := "sfp28"

			before, err := s.ListChangesForEntity(ctx, "device_type_component", dtID, 50)
			if err != nil {
				t.Fatalf("listing changes before: %v", err)
			}

			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "Ethernet1/[1-8]", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("creating components: %v", err)
			}

			after, err := s.ListChangesForEntity(ctx, "device_type_component", dtID, 50)
			if err != nil {
				t.Fatalf("listing changes after: %v", err)
			}
			if len(after)-len(before) != 1 {
				t.Fatalf("change_log gained %d entries for one batch of 8, want exactly 1",
					len(after)-len(before))
			}
			if after[0].Action != domain.ActionCreate {
				t.Errorf("action = %s, want create", after[0].Action)
			}
			if !strings.Contains(after[0].Diff, "Ethernet1/1") {
				t.Errorf("the entry does not name what was created: %s", after[0].Diff)
			}
			if !strings.Contains(after[0].Diff, `"count":8`) {
				t.Errorf("the entry does not record the count: %s", after[0].Diff)
			}
		})
	}
}

// TestUpdateDeviceTypeComponentPreservesKindAndType: the manufacturer_id
// guard's counterpart for a component -- device_type_id and kind decide what
// the row IS and which real table Task 4 turns it into, so an edit cannot
// smuggle either through even if the caller submits different ones.
func TestUpdateDeviceTypeComponentPreservesKindAndType(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)
			otherDtID := mustDeviceType(t, s, ctx, mustManufacturer(t, s, ctx, "hp", "HP"), "DL380", nil)
			ff := "rj45"

			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("creating: %v", err)
			}
			rows, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			c := rows[0]

			// Try to smuggle a different device type and kind through the edit.
			c.DeviceTypeID = otherDtID
			c.Kind = domain.ComponentKindPowerInput
			c.Name = "eth0-renamed"
			if err := s.UpdateDeviceTypeComponent(ctx, testPermit, &c); err != nil {
				t.Fatalf("updating: %v", err)
			}

			stored, err := s.getDeviceTypeComponent(ctx, c.ID)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if stored.DeviceTypeID != dtID {
				t.Errorf("device_type_id = %s, want it pinned to %s", stored.DeviceTypeID, dtID)
			}
			if stored.Kind != domain.ComponentKindInterface {
				t.Errorf("kind = %s, want it pinned to interface", stored.Kind)
			}
			if stored.Name != "eth0-renamed" {
				t.Errorf("name = %s, want the edit to have applied", stored.Name)
			}
		})
	}
}

// TestRetireDeviceTypeComponentIsSoftDeleteAndIdempotent.
func TestRetireDeviceTypeComponentIsSoftDeleteAndIdempotent(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)
			dva := 550
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindPowerInput, NameSpec: "psu1", DrawVA: &dva,
			}); err != nil {
				t.Fatalf("creating: %v", err)
			}
			rows, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			id := rows[0].ID

			if err := s.RetireDeviceTypeComponent(ctx, testPermit, id); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			live, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(live) != 0 {
				t.Errorf("retired component still appears in the active list")
			}

			stored, err := s.getDeviceTypeComponent(ctx, id)
			if err != nil {
				t.Fatalf("the row is gone after retirement, want a soft delete: %v", err)
			}
			if stored.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %s, want retired", stored.Lifecycle)
			}

			// Retiring twice is a no-op, not an error or a second audit row.
			before, _ := s.ListChangesForEntity(ctx, "device_type_component", id, 50)
			if err := s.RetireDeviceTypeComponent(ctx, testPermit, id); err != nil {
				t.Errorf("retiring twice: %v", err)
			}
			after, _ := s.ListChangesForEntity(ctx, "device_type_component", id, 50)
			if len(after) != len(before) {
				t.Errorf("retiring an already-retired component wrote %d extra audit entries", len(after)-len(before))
			}
		})
	}
}

// TestUpdateDeviceTypeComponentCannotWithdrawOrReviveIt.
//
// THE SIXTH TIME THIS RULE HAS BEEN WRITTEN IN THIS REPO, and the first where
// the ROW was genuinely at risk rather than only the audit. UpdateProvider,
// UpdateInterface, UpdateNetGroup, UpdateNetAnchor and UpdateLink all pin
// lifecycle from the stored row, and in every one of those the UPDATE never
// named the column -- so the database was safe regardless and only logUpdate's
// struct diff could record a withdrawal that did not happen.
//
// This one named `lifecycle = ?` and wrote the submitted value, which makes it
// a real second withdrawal path: a correction could withdraw a component with
// none of RetireDeviceTypeComponent's meaning. The reverse is worse -- a
// submitted 'active' would REVIVE a withdrawn component, silently putting a
// port back on every asset that model instantiates from then on, with no record
// of anybody deciding it.
//
// Both halves are asserted because both can now fail independently: the row,
// because the SET no longer names the column, and the audit, because the struct
// is pinned.
func TestUpdateDeviceTypeComponentCannotWithdrawOrReviveIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dtID := mustDeviceTypeForComponents(t, s)
			ff := "rj45"
			if err := s.CreateDeviceTypeComponents(ctx, testPermit, dtID, ComponentSpec{
				Kind: domain.ComponentKindInterface, NameSpec: "eth0", FormFactor: &ff,
			}); err != nil {
				t.Fatalf("creating: %v", err)
			}
			rows, err := s.ListDeviceTypeComponents(ctx, dtID)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			c := rows[0]

			// A real change alongside the forged one, so an update is genuinely
			// logged and there is a diff to inspect.
			c.Name = "eth0-renamed"
			c.Lifecycle = domain.LifecycleRetired
			if err := s.UpdateDeviceTypeComponent(ctx, testPermit, &c); err != nil {
				t.Fatalf("correcting with a forged lifecycle: %v", err)
			}

			var life string
			if err := s.DB().Reader.Get(&life, s.DB().Reader.Rebind(
				`SELECT lifecycle FROM device_type_component WHERE id = ?`), c.ID); err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if life != domain.LifecycleActive {
				t.Errorf("the row is %q: a correction withdrew a template component, "+
					"bypassing RetireDeviceTypeComponent entirely", life)
			}

			var diffs []string
			if err := s.DB().Reader.Select(&diffs, s.DB().Reader.Rebind(
				`SELECT COALESCE(diff, '') FROM change_log
				  WHERE entity_id = ? AND action = 'update'`), c.ID); err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("no update was logged at all, so this test is checking nothing")
			}
			for _, d := range diffs {
				if strings.Contains(d, "lifecycle") {
					t.Errorf("change_log records a lifecycle change for a component that "+
						"is still active: %s", d)
				}
			}

			// And the other direction: a withdrawn component must not come back
			// through the correction path.
			if err := s.RetireDeviceTypeComponent(ctx, testPermit, c.ID); err != nil {
				t.Fatalf("withdrawing: %v", err)
			}
			revived, err := s.getDeviceTypeComponent(ctx, c.ID)
			if err != nil {
				t.Fatalf("reloading the withdrawn component: %v", err)
			}
			revived.Name = "eth0-again"
			revived.Lifecycle = domain.LifecycleActive
			if err := s.UpdateDeviceTypeComponent(ctx, testPermit, revived); err != nil {
				t.Fatalf("correcting a withdrawn component: %v", err)
			}
			if err := s.DB().Reader.Get(&life, s.DB().Reader.Rebind(
				`SELECT lifecycle FROM device_type_component WHERE id = ?`), c.ID); err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if life != domain.LifecycleRetired {
				t.Error("a correction revived a withdrawn template component. Every asset " +
					"of this model instantiated from now on gains a port nobody decided " +
					"to put back.")
			}
		})
	}
}
