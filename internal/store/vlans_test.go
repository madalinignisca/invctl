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

// TestTwoPrefixesCanShareOneVLAN.
//
// This replaces the backfill test, which went with the backfill in 00036. The
// property it was really protecting is this one: the v4 and v6 halves of a
// broadcast domain are ONE place, which the loose integer had no way to say --
// two prefixes each carrying "30" were two unrelated numbers. Now they point at
// the same row, and the VLAN's own page counts both.
func TestTwoPrefixesCanShareOneVLAN(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			vlanID := mustVLAN(t, s, ctx, 30, "workloads", nil)
			for _, cidr := range []string{"10.80.30.0/24", "2001:db8:80::/64"} {
				p, err := domain.NewPrefix(NewID(), cidr)
				if err != nil {
					t.Fatalf("building %s: %v", cidr, err)
				}
				p.VLANRefID = &vlanID
				if err := s.CreatePrefix(ctx, testActor, p); err != nil {
					t.Fatalf("creating %s: %v", cidr, err)
				}
			}

			rows, err := s.ListVLANs(ctx)
			if err != nil {
				t.Fatalf("listing vlans: %v", err)
			}
			for _, r := range rows {
				if r.ID != vlanID {
					continue
				}
				if r.PrefixCount != 2 {
					t.Errorf("VLAN 30 holds %d networks, want 2 -- the v4 and v6 halves "+
						"of one broadcast domain", r.PrefixCount)
				}
				return
			}
			t.Fatal("the VLAN is not in the list")
		})
	}
}

// TestAPrefixHasExactlyOnePlaceToSayItsVLAN.
//
// THE REGRESSION THIS EXISTS FOR IS DRIFT, and it shipped. 00031 added
// vlan_ref_id beside a loose vlan_id integer and left every writer of the
// integer in place, so editing a prefix's VLAN through the UI moved one and not
// the other: /prefixes said 41 while /vlans still counted the network under 40.
// Neither page was wrong about its own column.
//
// A test cannot easily assert "these two columns agree" once one of them is
// gone, and that is the point -- the fix was to remove the second place, not to
// synchronise it. So this asserts the structural property instead: the live
// schema has exactly one column carrying a prefix's VLAN. If somebody adds a
// convenience integer back, this fails.
func TestAPrefixHasExactlyOnePlaceToSayItsVLAN(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := migrated(t, e)

			var vlanish []string
			for _, col := range liveColumns(t, db, "prefix") {
				if strings.Contains(col, "vlan") {
					vlanish = append(vlanish, col)
				}
			}
			if len(vlanish) != 1 || vlanish[0] != "vlan_ref_id" {
				t.Errorf("prefix carries %v as its VLAN column(s), want exactly "+
					"[vlan_ref_id]. Two columns for one fact drift the first time "+
					"different code paths write each, which is what happened between "+
					"migrations 00031 and 00036", vlanish)
			}
		})
	}
}
