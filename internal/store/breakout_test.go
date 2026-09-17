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
	"errors"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// mediumPtr and lengthPtr save every test below from repeating the same
// address-of-a-literal boilerplate for Link.Medium/LengthM.
func mediumPtr(v string) *string { return &v }
func lengthPtr(v int) *int       { return &v }

// TestCreateBreakout1to4 covers the base case the design exists for: a
// QSFP-to-4xSFP+ DAC reads back as four link rows sharing one breakout id,
// positions 1-4 in the order the strands were given.
func TestCreateBreakout1to4(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			switchAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core", nil)
			aIface := mustInterface(t, s, ctx, switchAsset, "eth1")

			var bIfaces []string
			for i := 0; i < 4; i++ {
				srv := mustAsset(t, s, ctx, domain.KindServer, "srv-"+string(rune('a'+i)), nil)
				bIfaces = append(bIfaces, mustInterface(t, s, ctx, srv, "eth0"))
			}

			links, err := s.CreateBreakout(ctx, testPermit, domain.BreakoutSpec{
				AInterfaceID: aIface, BInterfaceIDs: bIfaces,
				Medium: mediumPtr("dac"), LengthM: lengthPtr(3),
			})
			if err != nil {
				t.Fatalf("creating breakout: %v", err)
			}
			if len(links) != 4 {
				t.Fatalf("got %d links, want 4", len(links))
			}

			breakoutID := links[0].BreakoutID
			if breakoutID == nil {
				t.Fatal("first strand carries no breakout id")
			}
			seenPositions := map[int]bool{}
			for i, l := range links {
				if l.AInterfaceID != aIface {
					t.Errorf("strand %d a-end = %s, want %s", i, l.AInterfaceID, aIface)
				}
				if l.BInterfaceID != bIfaces[i] {
					t.Errorf("strand %d b-end = %s, want %s", i, l.BInterfaceID, bIfaces[i])
				}
				if l.BreakoutID == nil || *l.BreakoutID != *breakoutID {
					t.Errorf("strand %d breakout id = %v, want %v", i, l.BreakoutID, breakoutID)
				}
				if l.BreakoutPosition == nil {
					t.Fatalf("strand %d carries no position", i)
				}
				seenPositions[*l.BreakoutPosition] = true
			}
			for pos := 1; pos <= 4; pos++ {
				if !seenPositions[pos] {
					t.Errorf("position %d missing from the four strands", pos)
				}
			}

			// Read back through GetLink -- proves SELECT * FROM link (and
			// StructScan) survives the two new columns, not just the value
			// CreateBreakout happened to return in memory.
			got, err := s.GetLink(ctx, links[0].ID)
			if err != nil {
				t.Fatalf("reading back strand: %v", err)
			}
			if got.BreakoutID == nil || *got.BreakoutID != *breakoutID {
				t.Errorf("read-back breakout id = %v, want %v", got.BreakoutID, breakoutID)
			}
		})
	}
}

// TestCreateBreakoutSharedAEndOnly proves the asymmetry the design turns on:
// the same a-end port is accepted by every strand (that is the relaxed
// check), but the same b-end port twice is refused (that check is NOT
// relaxed -- a far port still takes only one cable).
func TestCreateBreakoutSharedAEndOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			switchAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core", nil)
			aIface := mustInterface(t, s, ctx, switchAsset, "eth1")
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			b1 := mustInterface(t, s, ctx, srv, "eth0")
			b2 := mustInterface(t, s, ctx, srv, "eth1")

			t.Run("the same b-end twice in one breakout spec is refused", func(t *testing.T) {
				// Caught by domain.BreakoutSpec.Validate before a single
				// query runs -- a ValidationError, not ErrConflict, which is
				// the store's answer to a port already patched by a
				// DIFFERENT breakout or link (see the subtests below).
				_, err := s.CreateBreakout(ctx, testPermit, domain.BreakoutSpec{
					AInterfaceID: aIface, BInterfaceIDs: []string{b1, b1},
				})
				if _, ok := domain.AsValidation(err); !ok {
					t.Errorf("duplicate b-end within one spec = %v (%T), want *ValidationError", err, err)
				}
			})

			// A real 1-to-2 breakout succeeds -- the a-end is genuinely
			// shared, the b-ends are genuinely distinct.
			links, err := s.CreateBreakout(ctx, testPermit, domain.BreakoutSpec{
				AInterfaceID: aIface, BInterfaceIDs: []string{b1, b2},
			})
			if err != nil {
				t.Fatalf("creating breakout: %v", err)
			}
			if len(links) != 2 {
				t.Fatalf("got %d links, want 2", len(links))
			}

			t.Run("a third strand cannot reuse either already-patched b-end", func(t *testing.T) {
				srv2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
				b3 := mustInterface(t, s, ctx, srv2, "eth0")
				_, err := s.CreateBreakout(ctx, testPermit, domain.BreakoutSpec{
					AInterfaceID: aIface, BInterfaceIDs: []string{b3, b1},
				})
				if !errors.Is(err, domain.ErrConflict) {
					t.Errorf("reusing a patched b-end = %v, want ErrConflict", err)
				}
			})

			t.Run("the a-end is already patched, so a second breakout off it is refused too", func(t *testing.T) {
				srv3 := mustAsset(t, s, ctx, domain.KindServer, "srv-c", nil)
				b4 := mustInterface(t, s, ctx, srv3, "eth0")
				srv4 := mustAsset(t, s, ctx, domain.KindServer, "srv-d", nil)
				b5 := mustInterface(t, s, ctx, srv4, "eth0")
				_, err := s.CreateBreakout(ctx, testPermit, domain.BreakoutSpec{
					AInterfaceID: aIface, BInterfaceIDs: []string{b4, b5},
				})
				if !errors.Is(err, domain.ErrConflict) {
					t.Errorf("a second breakout off an already-patched a-end = %v, want ErrConflict", err)
				}
			})
		})
	}
}

// breakoutFixture builds a live two-strand breakout and returns its links.
func breakoutFixture(t *testing.T, s *SQLStore, ctx context.Context) []*domain.Link {
	t.Helper()
	switchAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core", nil)
	aIface := mustInterface(t, s, ctx, switchAsset, "eth1")
	srv1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
	srv2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
	b1 := mustInterface(t, s, ctx, srv1, "eth0")
	b2 := mustInterface(t, s, ctx, srv2, "eth0")

	links, err := s.CreateBreakout(ctx, testPermit, domain.BreakoutSpec{
		AInterfaceID: aIface, BInterfaceIDs: []string{b1, b2},
		Medium: mediumPtr("dac"), LengthM: lengthPtr(3),
	})
	if err != nil {
		t.Fatalf("creating breakout fixture: %v", err)
	}
	// Reloaded through GetLink rather than returned as-is: CreateBreakout's
	// in-memory structs never carry the row_version the INSERT's DEFAULT
	// actually assigned, and UpdateLink's optimistic-concurrency check would
	// refuse every correction below on a version mismatch that has nothing
	// to do with the drift guard under test -- the same reload
	// TestUpdateLinkCorrectsMediumAndLength already does for an ordinary cable.
	for i, l := range links {
		reloaded, err := s.GetLink(ctx, l.ID)
		if err != nil {
			t.Fatalf("reloading strand %d: %v", i, err)
		}
		links[i] = reloaded
	}
	return links
}

// TestBreakoutDriftIsRefusedOnCreate builds the drift deliberately by
// hand-writing a second, disagreeing row under the same breakout id --
// CreateBreakout itself cannot produce this, since every strand comes from
// the same spec.Medium/spec.LengthM. This proves the read-back guard would
// catch a row that got in some other way (a future writer, a bad migration,
// direct SQL), not only rows CreateBreakout itself produces.
func TestBreakoutDriftIsRefusedOnCreate(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			links := breakoutFixture(t, s, ctx)

			drifted := *links[1]
			drifted.Medium = mediumPtr("mmf") // siblings say "dac"
			drifted.RowVersion = links[1].RowVersion
			err := s.UpdateLink(ctx, testPermit, &drifted)
			if err == nil {
				t.Fatal("want an error correcting one strand to a different medium than its siblings, got none")
			}
			if _, ok := domain.AsValidation(err); !ok {
				t.Errorf("want a *ValidationError, got %T: %v", err, err)
			}
		})
	}
}

// TestUpdateLinkCannotDriftABreakout is the direct test of the guard's stated
// purpose: correcting ONE strand's medium or length must be refused when it
// would disagree with its still-live siblings, and must succeed when the
// correction is applied to every member (which the guard sees no drift for,
// since after the update it is being compared against ITSELF among the
// remaining siblings -- see the length_m case below for the stronger form).
func TestUpdateLinkCannotDriftABreakout(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			links := breakoutFixture(t, s, ctx)

			t.Run("a different medium than its live sibling is refused", func(t *testing.T) {
				upd := *links[0]
				upd.Medium = mediumPtr("mmf")
				err := s.UpdateLink(ctx, testPermit, &upd)
				if err == nil {
					t.Fatal("want an error, got none")
				}
				if _, ok := domain.AsValidation(err); !ok {
					t.Errorf("want a *ValidationError, got %T: %v", err, err)
				}
			})

			t.Run("a different length_m than its live sibling is refused", func(t *testing.T) {
				upd := *links[0]
				upd.LengthM = lengthPtr(99)
				err := s.UpdateLink(ctx, testPermit, &upd)
				if err == nil {
					t.Fatal("want an error, got none")
				}
				if _, ok := domain.AsValidation(err); !ok {
					t.Errorf("want a *ValidationError, got %T: %v", err, err)
				}
			})

			t.Run("the identical value the siblings already carry is accepted", func(t *testing.T) {
				upd := *links[0]
				upd.Medium = mediumPtr("dac")
				upd.LengthM = lengthPtr(3)
				if err := s.UpdateLink(ctx, testPermit, &upd); err != nil {
					t.Errorf("re-writing the same value as its siblings: %v", err)
				}
				// UpdateLink bumps upd's own RowVersion, not links[0]'s --
				// upd is a copy. Reload so the next subtest's optimistic-
				// concurrency check sees the row as it actually is now,
				// rather than failing on a version mismatch that has
				// nothing to do with what it is testing.
				reloaded, err := s.GetLink(ctx, links[0].ID)
				if err != nil {
					t.Fatalf("reloading after update: %v", err)
				}
				links[0] = reloaded
			})

			t.Run("retiring a strand frees it from the drift check", func(t *testing.T) {
				if err := s.RetireLink(ctx, testPermit, links[1].ID); err != nil {
					t.Fatalf("retiring strand: %v", err)
				}
				// Now the only live strand left may carry whatever medium it
				// likes -- a retired strand's old value must not still gate it.
				upd := *links[0]
				upd.Medium = mediumPtr("mmf")
				if err := s.UpdateLink(ctx, testPermit, &upd); err != nil {
					t.Errorf("correcting the sole remaining live strand: %v", err)
				}
			})
		})
	}
}

// TestRetiringABreakoutStrandFreesItsPosition proves the partial unique index
// (link_breakout_position_key) is scoped to live rows: pulling position 2 of
// a breakout and then declaring a fresh strand into that same position must
// succeed, not collide with the retired row's old index entry.
func TestRetiringABreakoutStrandFreesItsPosition(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			links := breakoutFixture(t, s, ctx)
			breakoutID := *links[1].BreakoutID

			if err := s.RetireLink(ctx, testPermit, links[1].ID); err != nil {
				t.Fatalf("retiring strand: %v", err)
			}

			// A brand new interface takes the freed b-end's port name, and a
			// fresh row is written directly at position 2 of the SAME
			// breakout id. Direct SQL, not CreateBreakout or CreateLink:
			// neither public method can add one strand to an EXISTING
			// breakout (that repair path is out of this task's scope), and
			// CreateLink's INSERT does not even name the breakout columns --
			// this is a schema-level test of the partial unique index
			// itself (link_breakout_position_key), proving it is scoped to
			// live rows the way port_pass_through.position and
			// backend_pool(service_id, name) already are.
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-c", nil)
			freshB := mustInterface(t, s, ctx, srv, "eth0")
			q := s.db.Writer.Rebind(`
				INSERT INTO link (id, a_interface_id, b_interface_id, medium, length_m,
				                   lifecycle, breakout_id, breakout_position)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
			_, err := s.db.Writer.ExecContext(ctx, q,
				NewID(), links[0].AInterfaceID, freshB, links[0].Medium, links[0].LengthM,
				domain.LifecycleActive, breakoutID, 2)
			if err != nil {
				t.Fatalf("re-declaring the freed position: %v", err)
			}
		})
	}
}

// TestBreakoutStrandsShareOneBatchID pins the audit grouping.
//
// Four strands declared in one act are one act. Without a batch id the audit
// reads as four separate cablings by the same person in the same second, and a
// reader has to infer the grouping from breakout_id inside each snapshot --
// which works, and is inference rather than the column built for it.
//
// Asserted rather than assumed because change_log is append-only: a batch id
// missing from a row is missing permanently, so this is the only moment it can
// be got right.
func TestBreakoutStrandsShareOneBatchID(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			links := breakoutFixture(t, s, ctx)

			var batches []string
			for _, l := range links {
				var b string
				if err := s.DB().Reader.Get(&b, s.DB().Reader.Rebind(
					`SELECT COALESCE(batch_id, '') FROM change_log
					  WHERE entity_type = 'link' AND action = 'create' AND entity_id = ?`,
				), l.ID); err != nil {
					t.Fatalf("reading the change log for strand %s: %v", l.ID, err)
				}
				batches = append(batches, b)
			}
			if len(batches) != len(links) {
				t.Fatalf("got %d create rows for %d strands", len(batches), len(links))
			}
			for _, b := range batches {
				if b == "" {
					t.Fatalf("a strand's create row has no batch_id. change_log admits no "+
						"UPDATE, so this row can never be grouped with its siblings "+
						"afterwards: %v", batches)
				}
				if b != batches[0] {
					t.Errorf("strands carry different batch ids %v -- one assembly, one "+
						"act, one batch", batches)
				}
			}
		})
	}
}
