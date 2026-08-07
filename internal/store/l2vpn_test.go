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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestATerminationAttachesExactlyOneEnd.
//
// A row naming both a VLAN and a port says the overlay lands in two places at
// once; one naming neither attaches nowhere. Both look like a connection and
// are not, which is worse than a missing row -- a missing row is visibly
// missing. Checked in the constructor AND by the schema, because a constructor
// gives the operator a sentence and a CHECK gives them a stack trace.
func TestATerminationAttachesExactlyOneEnd(t *testing.T) {
	vlan, port := "v-1", "i-1"
	cases := []struct {
		name        string
		vlanID      *string
		interfaceID *string
		ok          bool
	}{
		{"a VLAN", &vlan, nil, true},
		{"a port", nil, &port, true},
		{"both", &vlan, &port, false},
		{"neither", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewL2VPNTermination("t", "vpn", tc.vlanID, tc.interfaceID)
			if tc.ok && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("accepted, want refused")
			}
		})
	}
}

// TestTheSchemaRefusesTwoEndsToo. The constructor is the first line and the
// CHECK is the second: a future caller building the struct directly must not
// be able to write a row the constructor would have refused.
func TestTheSchemaRefusesTwoEndsToo(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			vpn, err := domain.NewL2VPN(NewID(), "ovl-1", domain.L2VPNVXLAN)
			if err != nil {
				t.Fatalf("building overlay: %v", err)
			}
			if err := s.CreateL2VPN(ctx, testActor, vpn); err != nil {
				t.Fatalf("creating overlay: %v", err)
			}
			vlanID := mustVLAN(t, s, ctx, 30, "workloads", nil)
			assetID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-ovl", nil)
			portID := mustInterface(t, s, ctx, assetID, "eth0")

			// Bypassing the constructor deliberately, the way a future caller
			// assembling the struct by hand would.
			bad := &domain.L2VPNTermination{
				ID: NewID(), L2VPNID: vpn.ID, VLANID: &vlanID, InterfaceID: &portID,
				Lifecycle: domain.LifecycleActive,
			}
			at := "2026-08-07T00:00:00Z"
			bad.CreatedAt, bad.UpdatedAt = &at, &at
			q := s.db.Writer.Rebind(`INSERT INTO l2vpn_termination
				(id, l2vpn_id, vlan_id, interface_id, lifecycle, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`)
			_, err = s.db.Writer.ExecContext(ctx, q, bad.ID, bad.L2VPNID, bad.VLANID,
				bad.InterfaceID, bad.Lifecycle, bad.CreatedAt, bad.UpdatedAt)
			if err == nil {
				t.Error("the schema accepted a termination naming both a VLAN and a port")
			}
		})
	}
}

// TestOneTerminationStretchesNothing. The same shape of finding as a
// redundancy group with one member: configured, and connecting nothing to
// anything.
func TestOneTerminationStretchesNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			vpn, _ := domain.NewL2VPN(NewID(), "ovl-2", domain.L2VPNVXLAN)
			vni := int64(10030)
			vpn.Identifier = &vni
			if err := s.CreateL2VPN(ctx, testActor, vpn); err != nil {
				t.Fatalf("creating overlay: %v", err)
			}

			find := func() L2VPNRow {
				t.Helper()
				rows, err := s.ListL2VPNs(ctx)
				if err != nil {
					t.Fatalf("listing: %v", err)
				}
				for _, r := range rows {
					if r.ID == vpn.ID {
						return r
					}
				}
				t.Fatal("the overlay vanished")
				return L2VPNRow{}
			}

			if got := find().Reach(); got != domain.L2VPNUnattached {
				t.Errorf("an overlay with nothing attached reports %q, want %q",
					got, domain.L2VPNUnattached)
			}

			v1 := mustVLAN(t, s, ctx, 30, "site-a", nil)
			t1, _ := domain.NewL2VPNTermination(NewID(), vpn.ID, &v1, nil)
			if err := s.CreateL2VPNTermination(ctx, testActor, t1); err != nil {
				t.Fatalf("attaching: %v", err)
			}
			if got := find().Reach(); got != domain.L2VPNOneEnd {
				t.Errorf("a one-ended overlay reports %q, want %q -- it connects nothing "+
					"to anything", got, domain.L2VPNOneEnd)
			}

			v2 := mustVLAN(t, s, ctx, 31, "site-b", nil)
			t2, _ := domain.NewL2VPNTermination(NewID(), vpn.ID, &v2, nil)
			if err := s.CreateL2VPNTermination(ctx, testActor, t2); err != nil {
				t.Fatalf("attaching: %v", err)
			}
			if got := find().Reach(); got != domain.L2VPNStretched {
				t.Errorf("a two-ended overlay reports %q, want %q", got, domain.L2VPNStretched)
			}

			// And it must refuse to be withdrawn while attached.
			if err := s.RetireL2VPN(ctx, testActor, vpn.ID); err == nil {
				t.Error("an overlay with live attachments was retired")
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict so the handler returns 409", err)
			}

			// Detaching both lets it go, and a detached row stops counting.
			for _, id := range []string{t1.ID, t2.ID} {
				if err := s.RetireL2VPNTermination(ctx, testActor, id); err != nil {
					t.Fatalf("detaching: %v", err)
				}
			}
			if got := find().Reach(); got != domain.L2VPNUnattached {
				t.Errorf("after detaching both, reach = %q, want %q -- a retired "+
					"termination must stop counting", got, domain.L2VPNUnattached)
			}
			if err := s.RetireL2VPN(ctx, testActor, vpn.ID); err != nil {
				t.Errorf("an unattached overlay could not be retired: %v", err)
			}
		})
	}
}
