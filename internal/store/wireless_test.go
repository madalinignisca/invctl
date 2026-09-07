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
	"github.com/madalinignisca/invctl/internal/impact"
)

func mustRadio(t *testing.T, s *SQLStore, ctx context.Context, assetID, name string) string {
	t.Helper()
	// A RADIO, not an RJ45 -- mustInterface hardcodes FFRJ45. Nothing in the
	// store branches on the form factor, so this would pass with rj45; it is
	// a radio because a fixture that describes an access point with a copper
	// port is a fixture nobody can read.
	iface, err := domain.NewInterface(NewID(), assetID, name, domain.FFRadio5G)
	if err != nil {
		t.Fatalf("building radio %s: %v", name, err)
	}
	if err := s.CreateInterface(ctx, testPermit, iface); err != nil {
		t.Fatalf("creating radio %s: %v", name, err)
	}
	return iface.ID
}

func mustWLAN(t *testing.T, s *SQLStore, ctx context.Context, name, ssid string, scope *string) string {
	t.Helper()
	w, err := domain.NewWirelessLAN(NewID(), name, ssid, "wpa2_personal", scope)
	if err != nil {
		t.Fatalf("building wireless lan %s: %v", ssid, err)
	}
	if err := s.CreateWirelessLAN(ctx, testPermit, w); err != nil {
		t.Fatalf("creating wireless lan %s: %v", ssid, err)
	}
	return w.ID
}

func mustSetWLANs(t *testing.T, s *SQLStore, ctx context.Context, interfaceID string, wlanIDs ...string) {
	t.Helper()
	members := make([]domain.InterfaceWLAN, 0, len(wlanIDs))
	for _, id := range wlanIDs {
		members = append(members, domain.InterfaceWLAN{InterfaceID: interfaceID, WirelessLANID: id})
	}
	if err := s.SetInterfaceWLANs(ctx, testPermit, interfaceID, members); err != nil {
		t.Fatalf("setting wireless membership: %v", err)
	}
}

// ---------- Task 4: CRUD, redaction ----------

// TestSnapshotRedactsPSKRef: psk_ref is absent from every change_log diff,
// and a rotation is still visible as having happened.
//
// docs/AUDIT.md rule 12, applied to the field D4 introduced: "Record THAT
// secret_ref changed, never what to. A path is not a secret, but a complete
// map of secret paths readable by every account is a reconnaissance gift."
//
// BOTH HALVES, because redacting one and not the other leaks on the first
// UPDATE instead of the first CREATE -- a worse failure, because it looks
// fixed. The whole table is read raw for the reason
// TestSnapshotRedactsSecretRef gives.
func TestSnapshotRedactsPSKRef(t *testing.T) {
	const pskPath = "kv/prod/wifi/corp/psk"
	const rotated = "kv/prod/wifi/corp/psk-2026-09"

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			w, err := domain.NewWirelessLAN(NewID(), "Corp", "corp", "wpa2_personal", nil)
			if err != nil {
				t.Fatalf("building wireless lan: %v", err)
			}
			w.PSKRef = strPtr(pskPath)
			if err := s.CreateWirelessLAN(ctx, testPermit, w); err != nil {
				t.Fatalf("creating wireless lan: %v", err)
			}

			var diffs []string
			if err := s.read(ctx, &diffs, `SELECT diff FROM change_log`); err != nil {
				t.Fatalf("reading every audit diff: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("the audit trail is empty; this test proved nothing")
			}
			for _, diff := range diffs {
				if strings.Contains(diff, pskPath) {
					t.Errorf("a PSK path reached the audit trail: %s", diff)
				}
			}

			// The rule asks for the change to be RECORDED, not erased: a
			// reader must be able to tell the field exists and moved.
			var entry string
			err = s.readOne(ctx, &entry,
				`SELECT diff FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"wireless_lan", w.ID)
			if err != nil {
				t.Fatalf("reading the wireless lan's audit entry: %v", err)
			}
			if !strings.Contains(entry, "psk_ref") {
				t.Errorf("the diff does not mention psk_ref at all, so a reader cannot tell "+
					"the SSID has one: %s", entry)
			}
			if !strings.Contains(entry, domain.Redacted) {
				t.Errorf("the diff does not mark the field redacted: %s", entry)
			}

			// The UPDATE path, through the real store method rather than
			// diffJSON directly -- unlike identity, there IS an
			// UpdateWirelessLAN, so the rotation an operator would actually
			// perform is what gets driven here.
			t.Run("a rotation is recorded as having happened and not as what to", func(t *testing.T) {
				before, err := s.ListChangesForEntity(ctx, "wireless_lan", w.ID, 50)
				if err != nil {
					t.Fatalf("reading the change log: %v", err)
				}
				w.PSKRef = strPtr(rotated)
				if err := s.UpdateWirelessLAN(ctx, testPermit, w); err != nil {
					t.Fatalf("rotating the psk reference: %v", err)
				}
				after, err := s.ListChangesForEntity(ctx, "wireless_lan", w.ID, 50)
				if err != nil {
					t.Fatalf("reading the change log: %v", err)
				}
				if len(after) <= len(before) {
					t.Fatalf("rotating the PSK reference wrote no change_log entry "+
						"(%d before, %d after). Redaction must hide the VALUE, not the "+
						"fact that it moved -- an invisible rotation is worse than a "+
						"visible one", len(before), len(after))
				}
				newest := after[0]
				if strings.Contains(newest.Diff, rotated) || strings.Contains(newest.Diff, pskPath) {
					t.Errorf("the rotation leaked a PSK path: %s", newest.Diff)
				}
				if !strings.Contains(newest.Diff, "psk_ref") || !strings.Contains(newest.Diff, domain.Redacted) {
					t.Errorf("the rotation entry does not record that psk_ref changed: %s", newest.Diff)
				}
			})
		})
	}
}

// TestAWirelessLANWithRadiosCannotBeRetired.
func TestAWirelessLANWithRadiosCannotBeRetired(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			ap := mustAsset(t, s, ctx, domain.KindAccessPoint, "ap-1", nil)
			radio := mustRadio(t, s, ctx, ap, "radio0")
			corp := mustWLAN(t, s, ctx, "Corp", "corp", nil)

			mustSetWLANs(t, s, ctx, radio, corp)

			if err := s.RetireWirelessLAN(ctx, testPermit, corp); err == nil {
				t.Fatal("an SSID with a radio still broadcasting it was retired")
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict so the handler returns 409", err)
			}

			mustSetWLANs(t, s, ctx, radio)
			if err := s.RetireWirelessLAN(ctx, testPermit, corp); err != nil {
				t.Errorf("an SSID with no radios could not be retired: %v", err)
			}
		})
	}
}

// ---------- Task 5: the audit fold ----------

// TestMovingARadioBetweenSSIDsAuditsTheInterface. The set replacement
// CLAUDE.md names three times and this codebase has now got wrong four --
// the fourth in SetInterfaceVLANs, the function SetInterfaceWLANs copies.
//
// IT MUST FAIL WHEN THE MEMBERSHIP IS WRITTEN WITHOUT FOLDING INTO THE
// INTERFACE'S AUDITED VALUE, not merely when the write itself breaks. That
// is why it asserts on change_log CONTENT and not only on the count:
// dropping the `db` tag from interfaceWLANAudit.WirelessLANs leaves the
// write working, leaves an entry being written (the interface's row_version
// and updated_at move), and silently empties the diff of everything that
// changed.
func TestMovingARadioBetweenSSIDsAuditsTheInterface(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			ap := mustAsset(t, s, ctx, domain.KindAccessPoint, "ap-oslo-1", nil)
			radio := mustRadio(t, s, ctx, ap, "radio0")
			corp := mustWLAN(t, s, ctx, "Corp", "corp", nil)
			guest := mustWLAN(t, s, ctx, "Guest", "guest", nil)

			set := func(ids ...string) {
				t.Helper()
				mustSetWLANs(t, s, ctx, radio, ids...)
			}

			set(corp)
			before, err := s.ListChangesForEntity(ctx, "interface", radio, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			set(guest)
			after, err := s.ListChangesForEntity(ctx, "interface", radio, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}

			if len(after) <= len(before) {
				t.Fatalf("moving the radio from corp to guest wrote no change_log entry "+
					"(%d before, %d after). A set replacement that produces no diff on "+
					"the parent is the failure CLAUDE.md names three times", len(before), len(after))
			}
			// THE DIFF MUST NAME THE SSIDs, or the entry records that
			// something changed without recording what -- which is what the
			// missing `db` tag produced the first four times.
			newest := after[0]
			if !strings.Contains(newest.Diff, "guest") {
				t.Errorf("the audit entry does not mention the SSID the radio moved TO: %s", newest.Diff)
			}
			if !strings.Contains(newest.Diff, "corp") {
				t.Errorf("the audit entry does not mention the SSID the radio moved FROM, so "+
					"a reader cannot tell what was lost: %s", newest.Diff)
			}

			// AND A SECOND SSID ADDED TO A RADIO THAT KEEPS ITS FIRST, which
			// the move above does not cover. One AP radio serves many SSIDs
			// -- that is the whole reason this is not a `link` row -- and
			// adding one while the others stay is the commonest wireless
			// change there is. Mutation testing on the VLAN twin showed the
			// tagged field's db tag survived without exactly this case.
			warehouse := mustWLAN(t, s, ctx, "Warehouse scanners", "warehouse-scan", nil)
			set(guest, corp)
			beforeAdd, _ := s.ListChangesForEntity(ctx, "interface", radio, 50)
			set(guest, corp, warehouse)
			afterAdd, err := s.ListChangesForEntity(ctx, "interface", radio, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(afterAdd) <= len(beforeAdd) {
				t.Fatalf("adding warehouse-scan wrote no change_log entry (%d before, "+
					"%d after). The other two SSIDs did not change, so only the folded "+
					"set could record this", len(beforeAdd), len(afterAdd))
			}
			if !strings.Contains(afterAdd[0].Diff, "warehouse-scan") {
				t.Errorf("the audit entry does not mention warehouse-scan: %s", afterAdd[0].Diff)
			}
		})
	}
}

// TestARadioBroadcastsManySSIDsAndThatIsNotAConflict. §2.4 as a test:
// CreateLink would have refused the second association with ErrConflict,
// and this is the property that made `link` the wrong table.
func TestARadioBroadcastsManySSIDsAndThatIsNotAConflict(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			ap := mustAsset(t, s, ctx, domain.KindAccessPoint, "ap-1", nil)
			radio := mustRadio(t, s, ctx, ap, "radio0")
			corp := mustWLAN(t, s, ctx, "Corp", "corp", nil)
			guest := mustWLAN(t, s, ctx, "Guest", "guest", nil)
			warehouse := mustWLAN(t, s, ctx, "Warehouse scanners", "warehouse-scan", nil)

			if err := s.SetInterfaceWLANs(ctx, testPermit, radio, []domain.InterfaceWLAN{
				{InterfaceID: radio, WirelessLANID: corp},
				{InterfaceID: radio, WirelessLANID: guest},
				{InterfaceID: radio, WirelessLANID: warehouse},
			}); err != nil {
				t.Fatalf("broadcasting three SSIDs from one radio was refused: %v", err)
			}

			radios, err := s.ListWLANRadios(ctx, corp)
			if err != nil {
				t.Fatalf("listing radios on corp: %v", err)
			}
			if len(radios) != 1 || radios[0].InterfaceID != radio {
				t.Errorf("corp reports %+v, want exactly the one radio", radios)
			}
			for _, wlanID := range []string{corp, guest, warehouse} {
				radios, err := s.ListWLANRadios(ctx, wlanID)
				if err != nil {
					t.Fatalf("listing radios: %v", err)
				}
				if len(radios) != 1 {
					t.Errorf("wlan %s reports %d radios, want 1", wlanID, len(radios))
				}
			}
		})
	}
}

// ---------- Task 6: the fourth structure join ----------

// wirelessEstate builds the smallest estate that can demonstrate all four
// findings, and returns the ids the tests name.
//
// THE SHAPE IS THE POINT and mirrors the seed fixture (internal/seed):
//
//	corp            two APs -> reduced to one by losing one
//	guest           two APs -> emptied by losing both
//	warehouse-scan  one AP  -> a standing single point of failure, NOT reported
func wirelessEstate(t *testing.T, s *SQLStore, ctx context.Context) (ap1, ap2 string) {
	t.Helper()
	ap1 = mustAsset(t, s, ctx, domain.KindAccessPoint, "ap-1", nil)
	ap2 = mustAsset(t, s, ctx, domain.KindAccessPoint, "ap-2", nil)
	radio1 := mustRadio(t, s, ctx, ap1, "radio0")
	radio2 := mustRadio(t, s, ctx, ap2, "radio0")

	corp := mustWLAN(t, s, ctx, "Corp", "corp", nil)
	guest := mustWLAN(t, s, ctx, "Guest", "guest", nil)
	warehouse := mustWLAN(t, s, ctx, "Warehouse scanners", "warehouse-scan", nil)

	mustSetWLANs(t, s, ctx, radio1, corp, guest)
	mustSetWLANs(t, s, ctx, radio2, corp, guest, warehouse)
	return ap1, ap2
}

func wirelessFinding(res impact.Result, ssidSuffix string) (impact.StructureFinding, bool) {
	for _, f := range res.Structures {
		if f.Kind != impact.StructureWLAN {
			continue
		}
		if strings.HasSuffix(f.Name, ssidSuffix) {
			return f, true
		}
	}
	return impact.StructureFinding{}, false
}

// TestAnOutageDowningEveryAPEmptiesTheSSID. §4 item 9 bullet 3.
func TestAnOutageDowningEveryAPEmptiesTheSSID(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			ap1, ap2 := wirelessEstate(t, s, ctx)

			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{ap1, ap2}})
			if err != nil {
				t.Fatalf("simulating: %v", err)
			}
			f, ok := wirelessFinding(res, "guest")
			if !ok {
				t.Fatalf("guest is not reported, but every AP carrying it is down. "+
					"Findings: %+v", res.Structures)
			}
			if !f.Emptied() {
				t.Errorf("guest reports %d remaining, want 0", f.Remaining)
			}
			if f.Total != 2 {
				t.Errorf("guest reports %d assets before the outage, want 2", f.Total)
			}
			if f.Detail == "" || f.Href == "" {
				t.Errorf("the finding has no sentence or does not link anywhere: %+v", f)
			}
		})
	}
}

// TestAnOutageLeavingOneAPReportsReducedToOne. §4 item 9 bullet 4.
func TestAnOutageLeavingOneAPReportsReducedToOne(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			ap1, _ := wirelessEstate(t, s, ctx)

			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{ap1}})
			if err != nil {
				t.Fatalf("simulating: %v", err)
			}
			f, ok := wirelessFinding(res, "corp")
			if !ok {
				t.Fatalf("corp is not reported, but it is down to one AP of two. "+
					"Findings: %+v", res.Structures)
			}
			if f.Remaining != 1 || f.Total != 2 {
				t.Errorf("corp reports %d of %d, want 1 of 2", f.Remaining, f.Total)
			}
		})
	}
}

// TestAnSSIDThatHadOneAPAndStillHasOneIsNotReported. §4 item 9 bullet 5, and
// §2.2's pre-existing answer to the roadmap's "simulate a wireless bridge as
// a single point of failure": a simulation answers "what breaks if this
// fails", and for a thing that is already the only path there is nothing to
// simulate. The redundancy page says it permanently and without needing an
// outage.
func TestAnSSIDThatHadOneAPAndStillHasOneIsNotReported(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			ap1, _ := wirelessEstate(t, s, ctx)

			// ap1 is down; warehouse-scan lives on ap2 alone and ap2 is up.
			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{ap1}})
			if err != nil {
				t.Fatalf("simulating: %v", err)
			}
			if _, ok := wirelessFinding(res, "warehouse-scan"); ok {
				t.Error("warehouse-scan is reported, but it had one AP before this outage " +
					"and still has it -- that is a standing finding, not a consequence, " +
					"and repeating it here answers a question nobody asked")
			}
			// And the test must prove it looked: if corp is missing too, the
			// simulation returned nothing and this assertion is vacuous.
			if _, ok := wirelessFinding(res, "corp"); !ok {
				t.Fatal("corp is not reported either, so this test proved nothing")
			}
		})
	}
}

// TestTheSameSSIDAtTwoSitesIsTwoStructures. §4 item 9 bullet 6, and D6's
// whole point: `ssid` alone is deliberately not unique, so the Oslo `guest`
// and the Frankfurt `guest` are two rows, two structures, and two findings.
func TestTheSameSSIDAtTwoSitesIsTwoStructures(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			oslo := mustAsset(t, s, ctx, domain.KindSite, "dc-oslo", nil)
			fra := mustAsset(t, s, ctx, domain.KindSite, "colo-fra1", nil)
			apO := mustAsset(t, s, ctx, domain.KindAccessPoint, "ap-oslo-1", &oslo)
			apF := mustAsset(t, s, ctx, domain.KindAccessPoint, "ap-fra-1", &fra)

			guestO := mustWLAN(t, s, ctx, "Guest (Oslo)", "guest", &oslo)
			guestF := mustWLAN(t, s, ctx, "Guest (Frankfurt)", "guest", &fra)
			if guestO == guestF {
				t.Fatal("the two SSIDs share an id, so nothing below means anything")
			}

			radioO := mustRadio(t, s, ctx, apO, "radio0")
			radioF := mustRadio(t, s, ctx, apF, "radio0")
			mustSetWLANs(t, s, ctx, radioO, guestO)
			mustSetWLANs(t, s, ctx, radioF, guestF)

			g, err := s.LoadGraph(ctx)
			if err != nil {
				t.Fatalf("loading the graph: %v", err)
			}
			var ids []string
			for _, st := range g.Structures {
				if st.Kind == impact.StructureWLAN {
					ids = append(ids, st.ID)
				}
			}
			if len(ids) != 2 {
				t.Fatalf("the graph holds %d wireless structures, want 2: two estates "+
					"reusing one SSID name are two broadcast domains that have never "+
					"met. Got %v", len(ids), ids)
			}

			// AND THEY MUST NOT COLLAPSE INTO ONE FINDING. Downing the Oslo
			// AP empties the Oslo guest and leaves Frankfurt's alone; a
			// shared structure would report one finding covering both,
			// which is the failure a name-keyed implementation would
			// produce.
			res, err := s.Simulate(ctx, impact.Request{DownAssetIDs: []string{apO}})
			if err != nil {
				t.Fatalf("simulating: %v", err)
			}
			var emptied int
			for _, f := range res.Structures {
				if f.Kind != impact.StructureWLAN {
					continue
				}
				if f.Emptied() {
					emptied++
				}
			}
			if emptied != 1 {
				t.Errorf("losing the Oslo AP emptied %d wireless structures, want exactly "+
					"1 -- the Frankfurt guest is untouched. Findings: %+v",
					emptied, res.Structures)
			}
		})
	}
}

// TestAnSSIDIsUniqueWithinItsScopeAndTheUnscopedPoolIsOne. The index test:
// NULLs are distinct in a unique index on both engines, so a single
// composite over (ssid, scope_asset_id) would constrain nothing at all for
// the estate-wide case -- which is where every SSID starts.
func TestAnSSIDIsUniqueWithinItsScopeAndTheUnscopedPoolIsOne(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			oslo := mustAsset(t, s, ctx, domain.KindSite, "dc-oslo", nil)
			fra := mustAsset(t, s, ctx, domain.KindSite, "colo-fra1", nil)

			mustWLAN(t, s, ctx, "Guest (Oslo)", "guest", &oslo)

			t.Run("the same SSID at another site is allowed", func(t *testing.T) {
				w, _ := domain.NewWirelessLAN(NewID(), "Guest (Frankfurt)", "guest", "wpa2_personal", &fra)
				if err := s.CreateWirelessLAN(ctx, testPermit, w); err != nil {
					t.Errorf("guest at a second site was refused: %v", err)
				}
			})

			t.Run("the same SSID twice at one site is refused", func(t *testing.T) {
				w, _ := domain.NewWirelessLAN(NewID(), "Guest (Oslo) dup", "guest", "wpa2_personal", &oslo)
				if err := s.CreateWirelessLAN(ctx, testPermit, w); err == nil {
					t.Error("guest was declared twice at one site")
				}
			})

			mustWLAN(t, s, ctx, "Corp", "corp", nil)
			t.Run("a second unscoped SSID of the same name is refused", func(t *testing.T) {
				w, _ := domain.NewWirelessLAN(NewID(), "Corp dup", "corp", "wpa2_personal", nil)
				if err := s.CreateWirelessLAN(ctx, testPermit, w); err == nil {
					t.Error("corp was declared twice with no scope. NULLs are distinct in " +
						"SQL, so a composite over (ssid, scope_asset_id) enforces nothing " +
						"here -- and this is where every SSID starts")
				}
			})

			t.Run("a retired row does not block a new one of the same name", func(t *testing.T) {
				retired := mustWLAN(t, s, ctx, "Old guest", "old-guest", nil)
				if err := s.RetireWirelessLAN(ctx, testPermit, retired); err != nil {
					t.Fatalf("retiring: %v", err)
				}
				w, _ := domain.NewWirelessLAN(NewID(), "New guest", "old-guest", "wpa2_personal", nil)
				if err := s.CreateWirelessLAN(ctx, testPermit, w); err != nil {
					t.Errorf("a retired SSID's name could not be reused: %v", err)
				}
			})
		})
	}
}
