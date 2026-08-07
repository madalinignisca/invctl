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
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

func mustVLAN(t *testing.T, s *SQLStore, ctx context.Context, vid int, name string, group *string) string {
	t.Helper()
	v, err := domain.NewVLAN(NewID(), vid, name, group)
	if err != nil {
		t.Fatalf("building VLAN %d: %v", vid, err)
	}
	if err := s.CreateVLAN(ctx, testActor, v); err != nil {
		t.Fatalf("creating VLAN %d: %v", vid, err)
	}
	return v.ID
}

// TestAVIDIsUniqueWithinItsGroupAndTheUngroupedPoolIsOne.
//
// VLAN 10 in Oslo and VLAN 10 in Frankfurt are different L2 domains that have
// never met, so the number alone cannot be unique. But the ungrouped pool is a
// scope too -- and it is where every VLAN starts, so a composite index over
// (group_id, vid) would leave exactly the common case unconstrained.
func TestAVIDIsUniqueWithinItsGroupAndTheUngroupedPoolIsOne(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			site := mustAsset(t, s, ctx, domain.KindSite, "dc-oslo", nil)
			oslo, err := domain.NewVLANGroup(NewID(), "dc-oslo", &site)
			if err != nil {
				t.Fatalf("building group: %v", err)
			}
			if err := s.CreateVLANGroup(ctx, testActor, oslo); err != nil {
				t.Fatalf("creating group: %v", err)
			}
			site2 := mustAsset(t, s, ctx, domain.KindSite, "colo-fra1", nil)
			fra, _ := domain.NewVLANGroup(NewID(), "colo-fra1", &site2)
			if err := s.CreateVLANGroup(ctx, testActor, fra); err != nil {
				t.Fatalf("creating group: %v", err)
			}

			mustVLAN(t, s, ctx, 10, "management", &oslo.ID)

			t.Run("the same VID in another group is allowed", func(t *testing.T) {
				v, _ := domain.NewVLAN(NewID(), 10, "management", &fra.ID)
				if err := s.CreateVLAN(ctx, testActor, v); err != nil {
					t.Errorf("VLAN 10 in a second site was refused: %v", err)
				}
			})

			t.Run("the same VID twice in one group is refused", func(t *testing.T) {
				v, _ := domain.NewVLAN(NewID(), 10, "duplicate", &oslo.ID)
				if err := s.CreateVLAN(ctx, testActor, v); err == nil {
					t.Error("VLAN 10 was declared twice in one group")
				}
			})

			mustVLAN(t, s, ctx, 99, "transit", nil)
			t.Run("the same VID twice in the ungrouped pool is refused", func(t *testing.T) {
				v, _ := domain.NewVLAN(NewID(), 99, "duplicate", nil)
				if err := s.CreateVLAN(ctx, testActor, v); err == nil {
					t.Error("VLAN 99 was declared twice with no group. NULLs are distinct " +
						"in SQL, so a composite index over (group_id, vid) enforces nothing " +
						"here -- and this is where every VLAN starts")
				}
			})
		})
	}
}

// TestChangingAPortsVLANsIsAuditedOnThePort. The set replacement CLAUDE.md
// names three times: a port moving from VLAN 10 to VLAN 20 must not produce an
// empty diff.
func TestChangingAPortsVLANsIsAuditedOnThePort(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			v10 := mustVLAN(t, s, ctx, 10, "management", nil)
			v20 := mustVLAN(t, s, ctx, 20, "servers", nil)

			set := func(members ...domain.InterfaceVLAN) {
				t.Helper()
				if err := s.SetInterfaceVLANs(ctx, testActor, ifaceID, members); err != nil {
					t.Fatalf("setting membership: %v", err)
				}
			}
			untagged := func(id string) domain.InterfaceVLAN {
				return domain.InterfaceVLAN{InterfaceID: ifaceID, VLANID: id, Mode: domain.VLANModeUntagged}
			}

			set(untagged(v10))
			before, err := s.ListChangesForEntity(ctx, "interface", ifaceID, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			set(untagged(v20))
			after, err := s.ListChangesForEntity(ctx, "interface", ifaceID, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}

			if len(after) <= len(before) {
				t.Fatalf("moving the port from VLAN 10 to VLAN 20 wrote no change_log "+
					"entry (%d before, %d after). A set replacement that produces no "+
					"diff on the parent is the failure CLAUDE.md names three times",
					len(before), len(after))
			}
			// The diff has to name the VLANs, or the entry records that
			// something changed without recording what.
			newest := after[0]
			if !strings.Contains(newest.Diff, "20") {
				t.Errorf("the audit entry does not mention VLAN 20: %s", newest.Diff)
			}

			// AND THE TRUNK, which the untagged case above does not cover.
			// Mutation testing showed the tagged field's db tag survived without
			// this: adding a VLAN to a trunk is at least as common as moving an
			// access port, and it was going unaudited in complete silence.
			v30 := mustVLAN(t, s, ctx, 30, "workloads", nil)
			tagged := func(id string) domain.InterfaceVLAN {
				return domain.InterfaceVLAN{InterfaceID: ifaceID, VLANID: id, Mode: domain.VLANModeTagged}
			}
			set(untagged(v20), tagged(v10))
			beforeTrunk, _ := s.ListChangesForEntity(ctx, "interface", ifaceID, 50)
			set(untagged(v20), tagged(v10), tagged(v30))
			afterTrunk, err := s.ListChangesForEntity(ctx, "interface", ifaceID, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(afterTrunk) <= len(beforeTrunk) {
				t.Fatalf("adding VLAN 30 to the trunk wrote no change_log entry "+
					"(%d before, %d after). The untagged VLAN did not change, so only "+
					"the tagged set could record this", len(beforeTrunk), len(afterTrunk))
			}
			if !strings.Contains(afterTrunk[0].Diff, "30") {
				t.Errorf("the audit entry does not mention VLAN 30: %s", afterTrunk[0].Diff)
			}
		})
	}
}

// TestAPortCannotHaveTwoUntaggedVLANs, at the store as well as the constructor.
func TestAPortCannotHaveTwoUntaggedVLANs(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-2", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			v10 := mustVLAN(t, s, ctx, 10, "management", nil)
			v20 := mustVLAN(t, s, ctx, 20, "servers", nil)

			err := s.SetInterfaceVLANs(ctx, testActor, ifaceID, []domain.InterfaceVLAN{
				{InterfaceID: ifaceID, VLANID: v10, Mode: domain.VLANModeUntagged},
				{InterfaceID: ifaceID, VLANID: v20, Mode: domain.VLANModeUntagged},
			})
			if err == nil {
				t.Fatal("a port with two untagged VLANs was accepted; a frame arriving " +
					"without a tag would have no unambiguous home")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid so the handler returns 422", err)
			}
		})
	}
}

// TestAVLANInUseCannotBeRetired. Soft delete makes the contradiction permanent:
// a retired VLAN addressing live networks says "this does not exist" beside
// several rows saying "and here is what is on it".
func TestAVLANInUseCannotBeRetired(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-3", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			vid := mustVLAN(t, s, ctx, 30, "workloads", nil)

			if err := s.SetInterfaceVLANs(ctx, testActor, ifaceID, []domain.InterfaceVLAN{
				{InterfaceID: ifaceID, VLANID: vid, Mode: domain.VLANModeUntagged},
			}); err != nil {
				t.Fatalf("setting membership: %v", err)
			}

			if err := s.RetireVLAN(ctx, testActor, vid); err == nil {
				t.Fatal("a VLAN with a port on it was retired")
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict so the handler returns 409", err)
			}

			// Empty it, and the withdrawal is allowed.
			if err := s.SetInterfaceVLANs(ctx, testActor, ifaceID, nil); err != nil {
				t.Fatalf("clearing membership: %v", err)
			}
			if err := s.RetireVLAN(ctx, testActor, vid); err != nil {
				t.Errorf("an empty VLAN could not be retired: %v", err)
			}
		})
	}
}

// TestTheBackfillIsIdempotentAndSharesAVLANBetweenFamilies.
//
// The v4 and v6 prefixes of one broadcast domain both carried "30" in the old
// integer column and had no way to say they were the same domain. The backfill
// must put them on ONE VLAN -- that is the fact the model exists to express --
// and must do nothing at all on the second run.
func TestTheBackfillIsIdempotentAndSharesAVLANBetweenFamilies(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			mk := func(cidr string, vid int) string {
				p, err := domain.NewPrefix(NewID(), cidr)
				if err != nil {
					t.Fatalf("building %s: %v", cidr, err)
				}
				p.VLANID = &vid
				if err := s.CreatePrefix(ctx, testActor, p); err != nil {
					t.Fatalf("creating %s: %v", cidr, err)
				}
				return p.ID
			}
			v4 := mk("10.80.30.0/24", 30)
			v6 := mk("2001:db8:80::/64", 30)
			other := mk("10.80.40.0/24", 40)

			created, err := s.BackfillPrefixVLANs(ctx, testActor)
			if err != nil {
				t.Fatalf("backfilling: %v", err)
			}
			if created != 2 {
				t.Errorf("created %d VLANs, want 2 -- 30 and 40, with the v4 and v6 "+
					"prefixes of VLAN 30 sharing one", created)
			}

			got := func(id string) *string {
				p, err := s.GetPrefix(ctx, id)
				if err != nil {
					t.Fatalf("getting prefix: %v", err)
				}
				return p.VLANRefID
			}
			a, b, c := got(v4), got(v6), got(other)
			if a == nil || b == nil || c == nil {
				t.Fatal("a prefix was left unlinked by the backfill")
			}
			if *a != *b {
				t.Error("the v4 and v6 prefixes of VLAN 30 were put on DIFFERENT VLANs. " +
					"That they are one broadcast domain is precisely what the loose " +
					"integer could not say and this model exists to record")
			}
			if *a == *c {
				t.Error("VLAN 30 and VLAN 40 were collapsed into one")
			}

			// The second run must be silent.
			again, err := s.BackfillPrefixVLANs(ctx, testActor)
			if err != nil {
				t.Fatalf("re-running the backfill: %v", err)
			}
			if again != 0 {
				t.Errorf("the second run created %d more VLANs; it runs on every start", again)
			}
		})
	}
}
