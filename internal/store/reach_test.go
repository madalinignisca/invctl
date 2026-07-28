package store

import (
	"context"
	"errors"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

// mustNetGroup declares a forwarder group and returns its id.
func mustNetGroup(t *testing.T, s *SQLStore, ctx context.Context, code, kind, role, availability string) string {
	t.Helper()
	spec := domain.NetGroupSpec{Code: code, Name: code, Kind: kind, Role: role, Availability: availability}
	if availability == domain.AvailActiveActive {
		one := 1
		spec.MinHealthy = &one
	}
	if availability == domain.AvailActivePassive {
		mode := domain.FailoverManual
		spec.FailoverMode = &mode
	}
	g, err := domain.NewNetGroup(NewID(), spec, s.Now())
	if err != nil {
		t.Fatalf("building net group %s: %v", code, err)
	}
	if err := s.CreateNetGroup(ctx, testActor, g); err != nil {
		t.Fatalf("creating net group %s: %v", code, err)
	}
	return g.ID
}

func mustNetGroupMember(t *testing.T, s *SQLStore, ctx context.Context, groupID, assetID, role string) {
	t.Helper()
	m, err := domain.NewNetGroupMember(groupID, assetID, role, s.Now())
	if err != nil {
		t.Fatalf("building net group member: %v", err)
	}
	if err := s.AddNetGroupMember(ctx, testActor, m); err != nil {
		t.Fatalf("adding net group member: %v", err)
	}
}

// TestNetGroupCRUD covers create, list (with counts) and retire.
func TestNetGroupCRUD(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			groupID := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			assetID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil)
			mustNetGroupMember(t, s, ctx, groupID, assetID, "member")

			t.Run("it appears in the list with its member count", func(t *testing.T) {
				rows, err := s.ListNetGroups(ctx)
				if err != nil {
					t.Fatalf("listing net groups: %v", err)
				}
				if len(rows) != 1 {
					t.Fatalf("got %d groups, want 1", len(rows))
				}
				if rows[0].MemberCount != 1 {
					t.Errorf("member count = %d, want 1", rows[0].MemberCount)
				}
			})

			t.Run("active_active without min_healthy is rejected", func(t *testing.T) {
				_, err := domain.NewNetGroup(NewID(), domain.NetGroupSpec{
					Code: "bad", Name: "bad", Kind: domain.NetGroupMCLAG,
					Role: domain.NetRoleCore, Availability: domain.AvailActiveActive,
				}, s.Now())
				if !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("error = %v, want ErrInvalid", err)
				}
			})

			t.Run("a duplicate code conflicts", func(t *testing.T) {
				dup, err := domain.NewNetGroup(NewID(), domain.NetGroupSpec{
					Code: "sw-core", Name: "dup", Kind: domain.NetGroupStandalone,
					Role: domain.NetRoleCore, Availability: domain.AvailStandalone,
				}, s.Now())
				if err != nil {
					t.Fatalf("building duplicate: %v", err)
				}
				if err := s.CreateNetGroup(ctx, testActor, dup); !errors.Is(err, domain.ErrConflict) {
					t.Errorf("error = %v, want ErrConflict", err)
				}
			})

			t.Run("retiring is reflected and audited", func(t *testing.T) {
				if err := s.RetireNetGroup(ctx, testActor, groupID); err != nil {
					t.Fatalf("retiring: %v", err)
				}
				rows, err := s.ListNetGroups(ctx)
				if err != nil {
					t.Fatalf("listing: %v", err)
				}
				if len(rows) != 0 {
					t.Error("a retired group still appears in the default list")
				}
				changes, err := s.ListChangesForEntity(ctx, "net_group", groupID, 5)
				if err != nil {
					t.Fatalf("listing changes: %v", err)
				}
				if changes[0].Action != domain.ActionRetire {
					t.Errorf("action = %s, want retire", changes[0].Action)
				}
			})
		})
	}
}

// TestNetGroupMemberUniqueness covers idx_net_group_member_one: a device in
// two forwarding groups is a modelling error, and it is enforced by a real
// UNIQUE index, not a check-then-act.
func TestNetGroupMemberUniqueness(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			groupA := mustNetGroup(t, s, ctx, "sw-a", domain.NetGroupStandalone, domain.NetRoleAccess, domain.AvailStandalone)
			groupB := mustNetGroup(t, s, ctx, "sw-b", domain.NetGroupStandalone, domain.NetRoleAccess, domain.AvailStandalone)
			assetID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", nil)

			mustNetGroupMember(t, s, ctx, groupA, assetID, "member")

			m, err := domain.NewNetGroupMember(groupB, assetID, "member", s.Now())
			if err != nil {
				t.Fatalf("building member: %v", err)
			}
			if err := s.AddNetGroupMember(ctx, testActor, m); !errors.Is(err, domain.ErrConflict) {
				t.Errorf("adding a second active membership = %v, want ErrConflict", err)
			}

			t.Run("retiring the first membership frees the asset up", func(t *testing.T) {
				if err := s.RetireNetGroupMember(ctx, testActor, groupA, assetID); err != nil {
					t.Fatalf("retiring membership: %v", err)
				}
				if err := s.AddNetGroupMember(ctx, testActor, m); err != nil {
					t.Errorf("re-joining after the first membership was retired: %v", err)
				}
			})

			t.Run("retiring twice is a no-op", func(t *testing.T) {
				if err := s.RetireNetGroupMember(ctx, testActor, groupA, assetID); err != nil {
					t.Errorf("retiring an already-retired membership: %v", err)
				}
			})

			// The re-entry case an RMA hits: at this point the asset is an
			// ACTIVE member of groupB (from the subtest above). Retire it from
			// groupB, then add it back to groupB -- the SAME group it was just
			// retired from, hitting the (group_id, asset_id) PRIMARY KEY
			// directly rather than the partial index. A plain INSERT reports
			// a permanent, false "already belongs to a group" here; the
			// re-entry must succeed.
			t.Run("re-joining the SAME group after being retired from it succeeds", func(t *testing.T) {
				if err := s.RetireNetGroupMember(ctx, testActor, groupB, assetID); err != nil {
					t.Fatalf("retiring membership in groupB: %v", err)
				}
				again, err := domain.NewNetGroupMember(groupB, assetID, "member", s.Now())
				if err != nil {
					t.Fatalf("building member: %v", err)
				}
				if err := s.AddNetGroupMember(ctx, testActor, again); err != nil {
					t.Errorf("re-joining the same group after retirement = %v, want success", err)
				}
				rows, err := s.ListNetGroupMembers(ctx, groupB)
				if err != nil {
					t.Fatalf("listing members: %v", err)
				}
				if len(rows) != 1 || rows[0].AssetID != assetID {
					t.Errorf("groupB members = %+v, want exactly the re-joined asset active", rows)
				}
			})

			// And joining a DIFFERENT group while still active elsewhere must
			// still be refused -- the upsert must not have quietly widened
			// the invariant it was supposed to preserve.
			t.Run("joining a different group while still active is still refused", func(t *testing.T) {
				elsewhere, err := domain.NewNetGroupMember(groupA, assetID, "member", s.Now())
				if err != nil {
					t.Fatalf("building member: %v", err)
				}
				if err := s.AddNetGroupMember(ctx, testActor, elsewhere); !errors.Is(err, domain.ErrConflict) {
					t.Errorf("joining a second active group = %v, want ErrConflict", err)
				}
			})
		})
	}
}

// TestNetUplinkCRUD covers uplink creation, listing and the "no second active
// edge to the same upstream group" rule.
func TestNetUplinkCRUD(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			edge := mustNetGroup(t, s, ctx, "fw-edge", domain.NetGroupHAPair, domain.NetRoleEdge, domain.AvailActivePassive)

			u, err := domain.NewNetUplink(NewID(), core, edge, domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building uplink: %v", err)
			}
			if err := s.CreateNetUplink(ctx, testActor, u); err != nil {
				t.Fatalf("creating uplink: %v", err)
			}

			t.Run("it lists under the downstream group", func(t *testing.T) {
				rows, err := s.ListNetUplinks(ctx, core)
				if err != nil {
					t.Fatalf("listing uplinks: %v", err)
				}
				if len(rows) != 1 || rows[0].UpstreamCode != "fw-edge" {
					t.Errorf("uplinks = %+v, want one to fw-edge", rows)
				}
			})

			t.Run("a second active edge to the same upstream conflicts", func(t *testing.T) {
				dup, err := domain.NewNetUplink(NewID(), core, edge, domain.PlaneData, s.Now())
				if err != nil {
					t.Fatalf("building duplicate uplink: %v", err)
				}
				if err := s.CreateNetUplink(ctx, testActor, dup); !errors.Is(err, domain.ErrConflict) {
					t.Errorf("error = %v, want ErrConflict", err)
				}
			})

			t.Run("an uplink to a different upstream is an alternate path, not a duplicate", func(t *testing.T) {
				altUpstream := mustNetGroup(t, s, ctx, "fw-edge-2", domain.NetGroupStandalone, domain.NetRoleEdge, domain.AvailStandalone)
				alt, err := domain.NewNetUplink(NewID(), core, altUpstream, domain.PlaneData, s.Now())
				if err != nil {
					t.Fatalf("building alternate uplink: %v", err)
				}
				if err := s.CreateNetUplink(ctx, testActor, alt); err != nil {
					t.Errorf("creating an alternate-path uplink: %v", err)
				}
			})

			// A group pair genuinely can have one edge per plane -- a data
			// uplink and a separate mgmt uplink between the same two groups --
			// so plane must be part of the uniqueness check, not just the
			// group pair.
			t.Run("an uplink on a different plane between the same pair is not a duplicate", func(t *testing.T) {
				mgmt, err := domain.NewNetUplink(NewID(), core, edge, domain.PlaneMgmt, s.Now())
				if err != nil {
					t.Fatalf("building mgmt uplink: %v", err)
				}
				if err := s.CreateNetUplink(ctx, testActor, mgmt); err != nil {
					t.Errorf("creating a second uplink on a different plane: %v", err)
				}
			})

			t.Run("retiring frees the pair up for re-creation", func(t *testing.T) {
				if err := s.RetireNetUplink(ctx, testActor, u.ID); err != nil {
					t.Fatalf("retiring: %v", err)
				}
				again, err := domain.NewNetUplink(NewID(), core, edge, domain.PlaneData, s.Now())
				if err != nil {
					t.Fatalf("building: %v", err)
				}
				if err := s.CreateNetUplink(ctx, testActor, again); err != nil {
					t.Errorf("re-creating after retiring the old edge: %v", err)
				}
			})
		})
	}
}

// TestCreateNetUplinkIsSerialised proves the "no second active edge to the
// same upstream group" invariant holds under concurrent callers on Postgres,
// mirroring TestCreateLinkIsSerialised: the check-then-act has no backing
// UNIQUE constraint, so writeSerializable is what makes it safe.
func TestCreateNetUplinkIsSerialised(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			edge := mustNetGroup(t, s, ctx, "fw-edge", domain.NetGroupHAPair, domain.NetRoleEdge, domain.AvailActivePassive)

			const racers = 8
			done := make(chan error, racers)
			for i := 0; i < racers; i++ {
				go func() {
					u, err := domain.NewNetUplink(NewID(), core, edge, domain.PlaneData, s.Now())
					if err != nil {
						done <- err
						return
					}
					done <- s.CreateNetUplink(ctx, testActor, u)
				}()
			}
			succeeded := 0
			for i := 0; i < racers; i++ {
				if err := <-done; err == nil {
					succeeded++
				}
			}

			var active int
			if err := s.readOne(ctx, &active,
				`SELECT COUNT(*) FROM net_uplink WHERE lifecycle = 'active' AND group_id = ? AND upstream_group_id = ?`,
				core, edge); err != nil {
				t.Fatalf("counting uplinks: %v", err)
			}
			if active != 1 {
				t.Errorf("%d active uplinks between the same pair (%d calls reported success), want exactly 1",
					active, succeeded)
			}
		})
	}
}

// TestNetAttachmentCRUD covers attachment creation with pinned chassis
// members, the non-forwarder-kind restriction, and the one-active-attachment
// rule per (asset, group, plane).
func TestNetAttachmentCRUD(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			chassis1 := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil)
			chassis2 := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-2", nil)
			mustNetGroupMember(t, s, ctx, core, chassis1, "member")
			mustNetGroupMember(t, s, ctx, core, chassis2, "member")

			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil)

			na, err := domain.NewNetAttachment(NewID(), hv, core, domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building attachment: %v", err)
			}
			members := []domain.NetAttachmentMember{{AttachmentID: na.ID, AssetID: chassis1}, {AttachmentID: na.ID, AssetID: chassis2}}
			if err := s.CreateNetAttachment(ctx, testActor, na, members); err != nil {
				t.Fatalf("creating attachment: %v", err)
			}

			t.Run("it lists with both pinned chassis", func(t *testing.T) {
				rows, err := s.ListNetAttachments(ctx, core)
				if err != nil {
					t.Fatalf("listing attachments: %v", err)
				}
				if len(rows) != 1 {
					t.Fatalf("got %d attachments, want 1", len(rows))
				}
				if len(rows[0].Members) != 2 {
					t.Errorf("pinned members = %v, want 2", rows[0].Members)
				}
			})

			t.Run("a rack cannot be attached", func(t *testing.T) {
				rack := mustAsset(t, s, ctx, domain.KindRack, "rack-a", nil)
				bad, err := domain.NewNetAttachment(NewID(), rack, core, domain.PlaneData, s.Now())
				if err != nil {
					t.Fatalf("building: %v", err)
				}
				if err := s.CreateNetAttachment(ctx, testActor, bad, nil); !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("error = %v, want ErrInvalid", err)
				}
			})

			t.Run("a pinned member that is not an active member of the group is rejected", func(t *testing.T) {
				stranger := mustAsset(t, s, ctx, domain.KindSwitch, "sw-elsewhere", nil)
				hv2 := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-02", nil)
				bad, err := domain.NewNetAttachment(NewID(), hv2, core, domain.PlaneData, s.Now())
				if err != nil {
					t.Fatalf("building: %v", err)
				}
				err = s.CreateNetAttachment(ctx, testActor, bad,
					[]domain.NetAttachmentMember{{AttachmentID: bad.ID, AssetID: stranger}})
				if !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("pinning a non-member = %v, want ErrInvalid", err)
				}
				rows, listErr := s.ListNetAttachments(ctx, core)
				if listErr != nil {
					t.Fatalf("listing attachments: %v", listErr)
				}
				for _, r := range rows {
					if r.AssetID == hv2 {
						t.Error("the rejected attachment was written anyway")
					}
				}
			})

			t.Run("a second active attachment on the same plane conflicts", func(t *testing.T) {
				dup, err := domain.NewNetAttachment(NewID(), hv, core, domain.PlaneData, s.Now())
				if err != nil {
					t.Fatalf("building duplicate: %v", err)
				}
				if err := s.CreateNetAttachment(ctx, testActor, dup, nil); !errors.Is(err, domain.ErrConflict) {
					t.Errorf("error = %v, want ErrConflict", err)
				}
			})

			t.Run("the same host may attach on a different plane", func(t *testing.T) {
				mgmt, err := domain.NewNetAttachment(NewID(), hv, core, domain.PlaneMgmt, s.Now())
				if err != nil {
					t.Fatalf("building: %v", err)
				}
				if err := s.CreateNetAttachment(ctx, testActor, mgmt, nil); err != nil {
					t.Errorf("attaching on a second plane: %v", err)
				}
			})

			t.Run("retiring frees the (asset, group, plane) up for re-creation", func(t *testing.T) {
				if err := s.RetireNetAttachment(ctx, testActor, na.ID); err != nil {
					t.Fatalf("retiring: %v", err)
				}
				again, err := domain.NewNetAttachment(NewID(), hv, core, domain.PlaneData, s.Now())
				if err != nil {
					t.Fatalf("building: %v", err)
				}
				if err := s.CreateNetAttachment(ctx, testActor, again, nil); err != nil {
					t.Errorf("re-attaching after retiring the old row: %v", err)
				}
			})
		})
	}
}

// TestCreateNetAttachmentIsSerialised proves the one-active-attachment
// invariant holds under concurrent callers: like CreateLink, the check-then-act
// has no backing UNIQUE constraint.
func TestCreateNetAttachmentIsSerialised(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil)

			const racers = 8
			done := make(chan error, racers)
			for i := 0; i < racers; i++ {
				go func() {
					na, err := domain.NewNetAttachment(NewID(), hv, core, domain.PlaneData, s.Now())
					if err != nil {
						done <- err
						return
					}
					done <- s.CreateNetAttachment(ctx, testActor, na, nil)
				}()
			}
			succeeded := 0
			for i := 0; i < racers; i++ {
				if err := <-done; err == nil {
					succeeded++
				}
			}

			var active int
			if err := s.readOne(ctx, &active,
				`SELECT COUNT(*) FROM net_attachment WHERE lifecycle = 'active' AND asset_id = ? AND group_id = ? AND plane = 'data'`,
				hv, core); err != nil {
				t.Fatalf("counting attachments: %v", err)
			}
			if active != 1 {
				t.Errorf("%d active attachments (%d calls reported success), want exactly 1", active, succeeded)
			}
		})
	}
}

// TestNetAnchorCRUD covers anchor creation and retirement.
func TestNetAnchorCRUD(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			edge := mustNetGroup(t, s, ctx, "fw-edge", domain.NetGroupHAPair, domain.NetRoleEdge, domain.AvailActivePassive)
			a, err := domain.NewNetAnchor(NewID(), "internet", "Internet", "external", edge, s.Now())
			if err != nil {
				t.Fatalf("building anchor: %v", err)
			}
			if err := s.CreateNetAnchor(ctx, testActor, a); err != nil {
				t.Fatalf("creating anchor: %v", err)
			}

			rows, err := s.ListNetAnchors(ctx)
			if err != nil {
				t.Fatalf("listing anchors: %v", err)
			}
			if len(rows) != 1 || rows[0].GroupCode != "fw-edge" {
				t.Errorf("anchors = %+v, want one on fw-edge", rows)
			}

			if err := s.RetireNetAnchor(ctx, testActor, a.ID); err != nil {
				t.Fatalf("retiring anchor: %v", err)
			}
			after, err := s.ListNetAnchors(ctx)
			if err != nil {
				t.Fatalf("listing anchors: %v", err)
			}
			if len(after) != 0 {
				t.Error("a retired anchor still appears in the default list")
			}
		})
	}
}

// TestNetworkCoverage covers the /network page's headline number: an asset
// with an attachment somewhere in its containment ancestry is modelled on
// that plane, absence of topology data is never evidence of disconnection,
// and a non-attachable kind (a rack) never enters the denominator.
func TestNetworkCoverage(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil)
			vm := mustAsset(t, s, ctx, domain.KindVM, "vm-1", &hv)
			mustAsset(t, s, ctx, domain.KindHypervisor, "hv-02", nil) // no attachment at all
			mustAsset(t, s, ctx, domain.KindRack, "rack-a", nil)      // not attachable; must not count

			na, err := domain.NewNetAttachment(NewID(), hv, core, domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building attachment: %v", err)
			}
			if err := s.CreateNetAttachment(ctx, testActor, na, nil); err != nil {
				t.Fatalf("creating attachment: %v", err)
			}

			cov, err := s.NetworkCoverage(ctx)
			if err != nil {
				t.Fatalf("computing coverage: %v", err)
			}
			if cov.TotalAttachable != 3 {
				t.Fatalf("total attachable = %d, want 3 (the rack must not count)", cov.TotalAttachable)
			}
			// hv-01 and vm-1 (through closure) are modelled on data; hv-02 is not.
			if cov.Data.Modelled != 2 {
				t.Errorf("data modelled = %d, want 2 (hv-01 and vm-1 through its own attachment's ancestry)", cov.Data.Modelled)
			}
			if cov.Data.Unmodelled != 1 {
				t.Errorf("data unmodelled = %d, want 1 (hv-02)", cov.Data.Unmodelled)
			}
			// Only a data-plane attachment was declared; mgmt and storage must
			// report zero modelled, not silently borrow the data-plane figure.
			if cov.Mgmt.Modelled != 0 || cov.Mgmt.Unmodelled != cov.TotalAttachable {
				t.Errorf("mgmt coverage = %+v, want nothing modelled", cov.Mgmt)
			}
			if cov.Storage.Modelled != 0 || cov.Storage.Unmodelled != cov.TotalAttachable {
				t.Errorf("storage coverage = %+v, want nothing modelled", cov.Storage)
			}
			_ = vm
		})
	}
}

// TestRetireAssetRetiresTopology is the milestone's other headline guarantee:
// no live topology edge may point at a retired box.
func TestRetireAssetRetiresTopology(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			chassis := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil)
			mustNetGroupMember(t, s, ctx, core, chassis, "member")

			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil)
			na, err := domain.NewNetAttachment(NewID(), hv, core, domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building attachment: %v", err)
			}
			if err := s.CreateNetAttachment(ctx, testActor, na, []domain.NetAttachmentMember{{AttachmentID: na.ID, AssetID: chassis}}); err != nil {
				t.Fatalf("creating attachment: %v", err)
			}

			// Retire the chassis: its group membership must stop being live,
			// and the attachment it was pinned to -- belonging to a DIFFERENT
			// asset, hv-01 -- must be retired outright rather than having its
			// member pin quietly dropped. Dropping the pin would leave an
			// empty member set, which means "attached to the group as a
			// whole" -- promoting a host that just lost its only path into
			// reading as MORE robust than before, the exact inversion this
			// fix exists to prevent.
			if err := s.RetireAsset(ctx, testActor, chassis); err != nil {
				t.Fatalf("retiring chassis: %v", err)
			}

			var activeMembers int
			if err := s.readOne(ctx, &activeMembers,
				`SELECT COUNT(*) FROM net_group_member WHERE asset_id = ? AND lifecycle = 'active'`, chassis); err != nil {
				t.Fatalf("counting memberships: %v", err)
			}
			if activeMembers != 0 {
				t.Errorf("%d active group memberships reference the retired chassis, want 0", activeMembers)
			}

			t.Run("the attachment it was pinned to is retired, not silently emptied", func(t *testing.T) {
				got, err := s.GetNetAttachment(ctx, na.ID)
				if err != nil {
					t.Fatalf("getting attachment: %v", err)
				}
				if got.Lifecycle != domain.LifecycleRetired {
					t.Errorf("attachment lifecycle = %s, want retired -- a single-homed host must not read as "+
						"attached to the whole group once its only chassis is gone", got.Lifecycle)
				}
				rows, err := s.ListNetAttachments(ctx, core)
				if err != nil {
					t.Fatalf("listing attachments: %v", err)
				}
				if len(rows) != 0 {
					t.Errorf("the retired attachment still lists as active: %+v", rows)
				}
			})

			// Retire the host itself: its own attachment must stop being live.
			// (na above is already retired via the chassis cascade, so this
			// covers a second, freshly-declared attachment.)
			hv2 := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-02", nil)
			core2 := mustNetGroup(t, s, ctx, "sw-core-2", domain.NetGroupStandalone, domain.NetRoleCore, domain.AvailStandalone)
			na2, err := domain.NewNetAttachment(NewID(), hv2, core2, domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building attachment: %v", err)
			}
			if err := s.CreateNetAttachment(ctx, testActor, na2, nil); err != nil {
				t.Fatalf("creating attachment: %v", err)
			}
			if err := s.RetireAsset(ctx, testActor, hv2); err != nil {
				t.Fatalf("retiring host: %v", err)
			}
			var activeAttachments int
			if err := s.readOne(ctx, &activeAttachments,
				`SELECT COUNT(*) FROM net_attachment WHERE asset_id = ? AND lifecycle = 'active'`, hv2); err != nil {
				t.Fatalf("counting attachments: %v", err)
			}
			if activeAttachments != 0 {
				t.Errorf("%d active attachments still reference the retired host, want 0", activeAttachments)
			}

			// And each cascade step is audited.
			changes, err := s.ListChangesForEntity(ctx, "net_group_member", core+"/"+chassis, 5)
			if err != nil {
				t.Fatalf("listing membership changes: %v", err)
			}
			if len(changes) == 0 || changes[0].Action != domain.ActionRetire {
				t.Errorf("the cascaded membership retirement was not audited: %+v", changes)
			}
			attachChanges, err := s.ListChangesForEntity(ctx, "net_attachment", na.ID, 5)
			if err != nil {
				t.Fatalf("listing attachment changes: %v", err)
			}
			if len(attachChanges) == 0 || attachChanges[0].Action != domain.ActionRetire {
				t.Errorf("the cascaded attachment retirement was not audited: %+v", attachChanges)
			}
		})
	}
}

// TestRetireAssetRetiresLinkRows: an asset's cables become dead topology the
// moment the asset behind either end is retired, exactly like unpatching a
// port.
func TestRetireAssetRetiresLinkRows(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetA := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", nil)
			assetB := mustAsset(t, s, ctx, domain.KindServer, "srv-1", nil)
			ifaceA := mustInterface(t, s, ctx, assetA, "eth0")
			ifaceB := mustInterface(t, s, ctx, assetB, "eth0")

			link, err := domain.NewLink(NewID(), ifaceA, ifaceB)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, link); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			if err := s.RetireAsset(ctx, testActor, assetB); err != nil {
				t.Fatalf("retiring asset: %v", err)
			}

			got, err := s.GetLink(ctx, link.ID)
			if err != nil {
				t.Fatalf("getting link: %v", err)
			}
			if got.Lifecycle != domain.LifecycleRetired {
				t.Errorf("link lifecycle = %s, want retired once the asset behind one end is gone", got.Lifecycle)
			}

			changes, err := s.ListChangesForEntity(ctx, "link", link.ID, 5)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) == 0 || changes[0].Action != domain.ActionRetire {
				t.Errorf("the cascaded link retirement was not audited: %+v", changes)
			}
		})
	}
}

// TestRetireNetGroupCascades: retiring a group must not strand its members,
// uplinks, attachments or anchors.
func TestRetireNetGroupCascades(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			edge := mustNetGroup(t, s, ctx, "fw-edge", domain.NetGroupHAPair, domain.NetRoleEdge, domain.AvailActivePassive)
			asset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil)
			mustNetGroupMember(t, s, ctx, core, asset, "member")

			// An uplink in each direction, so both halves of the OR in the
			// cascade query are exercised.
			downlink, err := domain.NewNetUplink(NewID(), core, edge, domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building uplink: %v", err)
			}
			if err := s.CreateNetUplink(ctx, testActor, downlink); err != nil {
				t.Fatalf("creating uplink: %v", err)
			}
			access := mustNetGroup(t, s, ctx, "sw-access", domain.NetGroupStandalone, domain.NetRoleAccess, domain.AvailStandalone)
			uplink, err := domain.NewNetUplink(NewID(), access, core, domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building uplink: %v", err)
			}
			if err := s.CreateNetUplink(ctx, testActor, uplink); err != nil {
				t.Fatalf("creating uplink: %v", err)
			}

			host := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil)
			attachment, err := domain.NewNetAttachment(NewID(), host, core, domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building attachment: %v", err)
			}
			if err := s.CreateNetAttachment(ctx, testActor, attachment, nil); err != nil {
				t.Fatalf("creating attachment: %v", err)
			}

			anchor, err := domain.NewNetAnchor(NewID(), "prod-net", "Prod net", "environment", core, s.Now())
			if err != nil {
				t.Fatalf("building anchor: %v", err)
			}
			if err := s.CreateNetAnchor(ctx, testActor, anchor); err != nil {
				t.Fatalf("creating anchor: %v", err)
			}

			if err := s.RetireNetGroup(ctx, testActor, core); err != nil {
				t.Fatalf("retiring group: %v", err)
			}

			t.Run("the member is retired", func(t *testing.T) {
				var n int
				if err := s.readOne(ctx, &n,
					`SELECT COUNT(*) FROM net_group_member WHERE group_id = ? AND lifecycle = 'active'`, core); err != nil {
					t.Fatalf("counting members: %v", err)
				}
				if n != 0 {
					t.Errorf("%d active members remain in the retired group, want 0", n)
				}
			})

			t.Run("uplinks in both directions are retired", func(t *testing.T) {
				down, err := s.GetNetUplink(ctx, downlink.ID)
				if err != nil {
					t.Fatalf("getting downstream uplink: %v", err)
				}
				if down.Lifecycle != domain.LifecycleRetired {
					t.Errorf("downstream uplink lifecycle = %s, want retired", down.Lifecycle)
				}
				up, err := s.GetNetUplink(ctx, uplink.ID)
				if err != nil {
					t.Fatalf("getting upstream uplink: %v", err)
				}
				if up.Lifecycle != domain.LifecycleRetired {
					t.Errorf("upstream uplink lifecycle = %s, want retired", up.Lifecycle)
				}
			})

			t.Run("the attachment is retired", func(t *testing.T) {
				got, err := s.GetNetAttachment(ctx, attachment.ID)
				if err != nil {
					t.Fatalf("getting attachment: %v", err)
				}
				if got.Lifecycle != domain.LifecycleRetired {
					t.Errorf("attachment lifecycle = %s, want retired", got.Lifecycle)
				}
			})

			t.Run("the anchor is retired", func(t *testing.T) {
				rows, err := s.ListNetAnchors(ctx)
				if err != nil {
					t.Fatalf("listing anchors: %v", err)
				}
				for _, r := range rows {
					if r.ID == anchor.ID {
						t.Errorf("the anchor on a retired group still lists as active: %+v", r)
					}
				}
			})

			t.Run("each cascade step is audited", func(t *testing.T) {
				for _, tc := range []struct{ entityType, entityID string }{
					{"net_group_member", core + "/" + asset},
					{"net_uplink", downlink.ID},
					{"net_uplink", uplink.ID},
					{"net_attachment", attachment.ID},
					{"net_anchor", anchor.ID},
				} {
					changes, err := s.ListChangesForEntity(ctx, tc.entityType, tc.entityID, 5)
					if err != nil {
						t.Fatalf("listing changes for %s %s: %v", tc.entityType, tc.entityID, err)
					}
					if len(changes) == 0 || changes[0].Action != domain.ActionRetire {
						t.Errorf("%s %s cascade was not audited: %+v", tc.entityType, tc.entityID, changes)
					}
				}
			})
		})
	}
}

// TestDeriveNetworkProposalProposesAndDoesNotWrite covers the propose-only
// contract: derivation reads `link` and proposes rows, but the tables it
// proposes into are untouched by the call itself, and re-running never
// duplicates or clobbers a row that already exists.
func TestDeriveNetworkProposalProposesAndDoesNotWrite(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			swAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil)
			mustNetGroupMember(t, s, ctx, core, swAsset, "member")

			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil)
			swPort := mustInterface(t, s, ctx, swAsset, "eth0")
			hvPort := mustInterface(t, s, ctx, hv, "mgmt0")
			markMgmt(t, s, ctx, hvPort)

			link, err := domain.NewLink(NewID(), swPort, hvPort)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, link); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			t.Run("it proposes an mgmt attachment from the host-side is_mgmt flag", func(t *testing.T) {
				proposal, err := s.DeriveNetworkProposal(ctx)
				if err != nil {
					t.Fatalf("deriving: %v", err)
				}
				if len(proposal.Attachments) != 1 {
					t.Fatalf("attachments = %+v, want exactly 1", proposal.Attachments)
				}
				got := proposal.Attachments[0]
				if got.AssetID != hv || got.GroupID != core || got.Plane != domain.PlaneMgmt {
					t.Errorf("proposed attachment = %+v, want hv-01/sw-core/mgmt", got)
				}
			})

			t.Run("it never writes anything", func(t *testing.T) {
				rows, err := s.ListNetAttachments(ctx, core)
				if err != nil {
					t.Fatalf("listing attachments: %v", err)
				}
				if len(rows) != 0 {
					t.Errorf("derivation wrote %d attachment rows; it must be propose-only", len(rows))
				}
			})

			t.Run("a declared row is never proposed again", func(t *testing.T) {
				na, err := domain.NewNetAttachment(NewID(), hv, core, domain.PlaneMgmt, s.Now())
				if err != nil {
					t.Fatalf("building: %v", err)
				}
				if err := s.CreateNetAttachment(ctx, testActor, na, nil); err != nil {
					t.Fatalf("declaring the attachment: %v", err)
				}

				proposal, err := s.DeriveNetworkProposal(ctx)
				if err != nil {
					t.Fatalf("deriving again: %v", err)
				}
				if len(proposal.Attachments) != 0 {
					t.Errorf("re-running derivation proposed %d rows over a declared one; "+
						"a declared row must never be shadowed, let alone overwritten: %+v",
						len(proposal.Attachments), proposal.Attachments)
				}

				// And the declared row itself is untouched.
				got, err := s.GetNetAttachment(ctx, na.ID)
				if err != nil {
					t.Fatalf("getting attachment: %v", err)
				}
				if got.Source != domain.SourceDeclared {
					t.Errorf("source = %s, want declared -- derivation must never overwrite it", got.Source)
				}
			})
		})
	}
}

// TestDeriveOrientsForwarderToForwarderByRole covers the direction rule:
// a cable between two declared forwarders is oriented downstream -> upstream
// by net_group.role rank, never guessed from cable order.
func TestDeriveOrientsForwarderToForwarderByRole(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			edge := mustNetGroup(t, s, ctx, "fw-edge", domain.NetGroupHAPair, domain.NetRoleEdge, domain.AvailActivePassive)
			coreAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil)
			edgeAsset := mustAsset(t, s, ctx, domain.KindFirewall, "fw-edge-1", nil)
			mustNetGroupMember(t, s, ctx, core, coreAsset, "member")
			mustNetGroupMember(t, s, ctx, edge, edgeAsset, "primary")

			// Cable declared core-side-first; the proposal must still orient
			// core (downstream, higher rank number) -> edge (upstream).
			corePort := mustInterface(t, s, ctx, coreAsset, "eth0")
			edgePort := mustInterface(t, s, ctx, edgeAsset, "eth0")
			link, err := domain.NewLink(NewID(), corePort, edgePort)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, link); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			proposal, err := s.DeriveNetworkProposal(ctx)
			if err != nil {
				t.Fatalf("deriving: %v", err)
			}
			if len(proposal.Uplinks) != 1 {
				t.Fatalf("uplinks = %+v, want exactly 1", proposal.Uplinks)
			}
			got := proposal.Uplinks[0]
			if got.GroupID != core || got.UpstreamGroupID != edge {
				t.Errorf("uplink = %+v, want sw-core -> fw-edge regardless of cable declaration order", got)
			}
			if got.Plane != domain.PlaneData {
				t.Errorf("plane = %s, want data for a cable between two non-mgmt ports", got.Plane)
			}
		})
	}
}

// TestDeriveSetsUplinkPlaneFromMgmtFlag is the sw-oob-1 trap the design calls
// out: an out-of-band cable between two forwarders must be proposed as a
// mgmt-plane uplink, never hardcoded to data -- joining an out-of-band switch
// into the data-plane graph would be exactly the wrong answer.
func TestDeriveSetsUplinkPlaneFromMgmtFlag(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			oob := mustNetGroup(t, s, ctx, "sw-oob", domain.NetGroupStandalone, domain.NetRoleAccess, domain.AvailStandalone)
			coreAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil)
			oobAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-oob-1", nil)
			mustNetGroupMember(t, s, ctx, core, coreAsset, "member")
			mustNetGroupMember(t, s, ctx, oob, oobAsset, "member")

			corePort := mustInterface(t, s, ctx, coreAsset, "mgmt0")
			oobPort := mustInterface(t, s, ctx, oobAsset, "eth0")
			markMgmt(t, s, ctx, corePort)

			link, err := domain.NewLink(NewID(), corePort, oobPort)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, link); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			proposal, err := s.DeriveNetworkProposal(ctx)
			if err != nil {
				t.Fatalf("deriving: %v", err)
			}
			if len(proposal.Uplinks) != 1 {
				t.Fatalf("uplinks = %+v, want exactly 1", proposal.Uplinks)
			}
			if got := proposal.Uplinks[0].Plane; got != domain.PlaneMgmt {
				t.Errorf("plane = %s, want mgmt -- an out-of-band cable must not be proposed as a data-plane uplink", got)
			}
		})
	}
}

// TestDeriveSkipsDisabledPortsAndRetiredLinks.
func TestDeriveSkipsDisabledPortsAndRetiredLinks(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			core := mustNetGroup(t, s, ctx, "sw-core", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			swAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil)
			mustNetGroupMember(t, s, ctx, core, swAsset, "member")
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil)

			t.Run("a disabled port proposes nothing", func(t *testing.T) {
				swPort := mustInterface(t, s, ctx, swAsset, "eth0")
				hvPort, err := domain.NewInterface(NewID(), hv, "eth0", domain.FFRJ45)
				if err != nil {
					t.Fatalf("building interface: %v", err)
				}
				hvPort.Enabled = false
				if err := s.CreateInterface(ctx, testActor, hvPort); err != nil {
					t.Fatalf("creating disabled interface: %v", err)
				}
				link, err := domain.NewLink(NewID(), swPort, hvPort.ID)
				if err != nil {
					t.Fatalf("building link: %v", err)
				}
				if err := s.CreateLink(ctx, testActor, link); err != nil {
					t.Fatalf("creating link: %v", err)
				}

				proposal, err := s.DeriveNetworkProposal(ctx)
				if err != nil {
					t.Fatalf("deriving: %v", err)
				}
				if len(proposal.Attachments) != 0 {
					t.Errorf("a disabled port produced a proposal: %+v", proposal.Attachments)
				}
			})

			t.Run("a retired link proposes nothing", func(t *testing.T) {
				swPort2 := mustInterface(t, s, ctx, swAsset, "eth1")
				hv2 := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-02", nil)
				hvPort2 := mustInterface(t, s, ctx, hv2, "eth0")
				link, err := domain.NewLink(NewID(), swPort2, hvPort2)
				if err != nil {
					t.Fatalf("building link: %v", err)
				}
				if err := s.CreateLink(ctx, testActor, link); err != nil {
					t.Fatalf("creating link: %v", err)
				}
				if err := s.RetireLink(ctx, testActor, link.ID); err != nil {
					t.Fatalf("retiring link: %v", err)
				}

				proposal, err := s.DeriveNetworkProposal(ctx)
				if err != nil {
					t.Fatalf("deriving: %v", err)
				}
				for _, a := range proposal.Attachments {
					if a.AssetID == hv2 {
						t.Errorf("a retired link produced a proposal for hv-02: %+v", proposal.Attachments)
					}
				}
			})
		})
	}
}

// markMgmt flips an already-created interface's is_mgmt flag directly, since
// CreateInterface has no update method and the fixture only needs this one
// field changed for the derivation test.
func markMgmt(t *testing.T, s *SQLStore, ctx context.Context, interfaceID string) {
	t.Helper()
	if _, err := s.db.Writer.Exec(s.db.Rebind(`UPDATE interface SET is_mgmt = TRUE WHERE id = ?`), interfaceID); err != nil {
		t.Fatalf("marking interface as mgmt: %v", err)
	}
}
