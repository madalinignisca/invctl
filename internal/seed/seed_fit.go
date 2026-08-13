// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Measurements, so the physical checks have something to find.
//
// THE FIXTURE HAS TO CONTAIN THE CASE OR THE TEST PROVES NOTHING. Every check
// in domain/fit.go needs an arrangement that triggers it and one that does not,
// or a green suite means only that the code ran. Laid out here rather than
// scattered through the other phases so the shape stays legible:
//
//	rack-a2       600mm wide, 600mm usable -- the shallow comms cabinet. It has
//	              the side-breathing firewall (too narrow) and a full-depth
//	              server (too deep). Two faults with distinct causes.
//	rack-a1       800mm wide, 900mm usable -- the properly specified one, so
//	              the same server in it reports nothing. The NEGATIVE CONTROL:
//	              without it a check that fired on everything would pass.
//	rack-b1       measured not at all, so its contents report as unknown rather
//	              than as fitting.
//	colo-rack-07  rated 600kg and deliberately loaded past it.
//
// FIGURES ARE APPROXIMATE PUBLISHED ONES, ROUNDED, and that is said out loud
// for the same reason the Hetzner storage price is marked approximate: a demo
// that cannot tell a checked number from a remembered one teaches the wrong
// habit. They are the right ORDER for the arithmetic, which is what the checks
// are demonstrating; nobody should quote them at a supplier.

// companyFit measures the cabinets and dimensions the catalogue.
func (b *builder) companyFit() {
	b.measureModels()
	b.addTheStorageStack()
	b.measureRacks()
	b.placeTheSideBreather()
	b.declarePortFaces()
}

// declarePortFaces says which side the cables come out of (WP-C3).
//
// THE ARISTA IS THE CASE, and it is the ordinary one rather than a contrivance.
// A data-centre switch has its ports at the same end as its cold-air intake, so
// the SKU that lets the ports face the cold aisle is the SKU whose ports face
// the REAR when the box is racked front-mounted -- which is exactly what this
// estate did. Every one of its leads crosses the cabinet, forever, and nothing
// in the software could say so until now.
//
// SOME MODELS ARE LEFT UNDECLARED on purpose. An estate where every catalogue
// entry is complete is not one anybody recognises, and the gap count is the
// honesty number that makes the other two findings worth believing.
func (b *builder) declarePortFaces() {
	for _, m := range []struct{ model, face string }{
		// Ports at the rear, mounted at the front. The finding.
		{"DCS-7050SX3-48YC8", domain.PortFaceRear},
		// Servers: management and data at the back, which is normal and is
		// also where they are mounted from, so nothing reports.
		{"PowerEdge R650", domain.PortFaceRear},
		{"PowerEdge R660", domain.PortFaceRear},
		{"ProLiant DL380 Gen11", domain.PortFaceRear},
		// A firewall is a front-panel box, and it is mounted front. The
		// NEGATIVE CONTROL: without something declared and correct, a check
		// that fired on every declared model would pass every assertion.
		{"FortiGate 121G", domain.PortFaceFront},
		// A vertical PDU has outlets down its whole length.
		{"AP8853", domain.PortFaceBoth},
		// The storage shelves are deliberately NOT declared, so the gap
		// finding has something to count.
	} {
		m := m
		if !b.ok() {
			return
		}
		id, ok := b.refs.DeviceTypes[m.model]
		if !ok {
			continue
		}
		dt, err := b.store.GetDeviceType(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("reading device type %s: %w", m.model, err))
			return
		}
		if dt.PortFace != nil && *dt.PortFace == m.face {
			continue // idempotent
		}
		updated := dt.DeviceType
		updated.PortFace = str(m.face)
		if err := b.store.UpdateDeviceType(b.ctx, Actor, &updated); err != nil {
			b.fail(fmt.Errorf("declaring the port face of %s: %w", m.model, err))
			return
		}
	}
}

// addTheStorageStack is where the weight in this estate actually lives.
//
// WRITTEN BECAUSE THE FIXTURE FAILED ITS OWN TEST. The estate had no overload
// case: three 1U servers weigh 58kg and no cabinet is rated anywhere near that,
// so the load check had nothing to find and any test naming it was decorative.
// The fix was NOT to invent a cabinet rated 60kg -- a rating nobody would
// believe teaches people to disbelieve the page. It was to put something heavy
// in the estate, which is what a small company with a DR site actually has.
//
// A populated 84-bay shelf is around 130kg, so three of them and a controller
// take a rack past a typical colo weight cap on their own. That is the real
// finding: the servers were never the problem, and the shelf somebody added
// last year is what put the rack over a contractual limit nobody re-read.
//
// IT ALSO ANSWERS THE QUESTION THAT STARTED THIS -- "will a NAS or a SAN fit"
// -- with something in the demo rather than only in the rules.
func (b *builder) addTheStorageStack() {
	b.manufacturer("netapp", "NetApp", "https://mysupport.netapp.com/")
	b.model("netapp", "DS4486 disk shelf", "DS4486-084", 5, "2029-12-31")
	b.model("netapp", "FAS2820 controller", "FAS2820A", 2, "2032-03-31")

	for _, m := range []struct {
		model        string
		depth, grams int
	}{
		// Deep AND heavy, which is the pair that makes storage the interesting
		// case: it is the one kind of kit that can fail a cabinet on either
		// measurement independently.
		{"DS4486 disk shelf", 610, 130000},
		{"FAS2820 controller", 610, 29000},
	} {
		m := m
		if !b.ok() {
			return
		}
		id, ok := b.refs.DeviceTypes[m.model]
		if !ok {
			continue
		}
		dt, err := b.store.GetDeviceType(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("reading %s: %w", m.model, err))
			return
		}
		updated := dt.DeviceType
		updated.DepthMM, updated.WeightGrams = num(m.depth), num(m.grams)
		updated.Airflow = str(domain.AirflowFrontToRear)
		if err := b.store.UpdateDeviceType(b.ctx, Actor, &updated); err != nil {
			b.fail(fmt.Errorf("measuring %s: %w", m.model, err))
			return
		}
	}

	for _, u := range []struct {
		name, model string
		pos         int
	}{
		{"stor-shelf-1", "DS4486 disk shelf", 1},
		{"stor-shelf-2", "DS4486 disk shelf", 6},
		{"stor-shelf-3", "DS4486 disk shelf", 11},
		{"stor-ctrl-1", "FAS2820 controller", 16},
	} {
		u := u
		b.asset(domain.KindStorage, u.name, "colo-rack-07", []string{"prod"},
			func(a *domain.Asset) {
				a.Vendor, a.Model = str("NetApp"), str(u.model)
				a.DeviceTypeID = b.deviceType(u.model)
				a.TeamID = b.team("platform")
				a.RackPosition, a.RackFace = num(u.pos), str(domain.FaceFront)
			})
	}

	// THE OPPOSING PAIR, and it needed adding too: no rear-to-front box in the
	// estate was adjacent to anything, so that check also had nothing to find.
	//
	// A second top-of-rack switch above the servers it serves, which is where a
	// ToR switch goes and is why the airflow SKU matters -- this one takes air
	// at the port face so the ports can face the cold aisle, and it is directly
	// above a server blowing the other way.
	b.asset(domain.KindSwitch, "sw-colo-2", "colo-rack-07", []string{"prod"},
		func(a *domain.Asset) {
			a.Vendor, a.Model = str("Arista"), str("DCS-7050SX3-48YC8")
			a.DeviceTypeID = b.deviceType("DCS-7050SX3-48YC8")
			a.TeamID = b.team("network")
			a.RackPosition, a.RackFace = num(41), str(domain.FaceFront)
		})

	// Approximate, and said so. Storage is the most expensive thing here and
	// the figures are the right order rather than a quotation.
	b.assetCosts([]costLine{
		{"stor-shelf-1", "acquisition", domain.CostOnce, 12000, -900, "84-bay shelf, populated — approximate"},
		{"stor-shelf-2", "acquisition", domain.CostOnce, 12000, -900, "84-bay shelf, populated — approximate"},
		{"stor-shelf-3", "acquisition", domain.CostOnce, 12000, -300, "84-bay shelf, populated — the one that put the rack over its cap"},
		{"stor-ctrl-1", "acquisition", domain.CostOnce, 18000, -900, "controller pair — approximate"},
		{"stor-ctrl-1", "subscription", domain.CostYearly, 4200, -900, "storage support — approximate"},
		{"sw-colo-2", "acquisition", domain.CostOnce, 9500, -700, "second top-of-rack switch — approximate"},
	})
}

// measureModels puts depth, weight and airflow on the catalogued models.
//
// AIRFLOW IS DECLARED ON SOME AND NOT OTHERS, on purpose. An estate where every
// model has been filled in is not one anybody recognises, and the gap count is
// the honesty number that makes the other findings trustworthy.
func (b *builder) measureModels() {
	for _, m := range []struct {
		model   string
		depth   int
		grams   int
		airflow string
	}{
		// Front-to-rear, the overwhelmingly common case. 772mm is why a 600mm
		// cabinet cannot take one and an 800mm one only just can.
		{"PowerEdge R650", 772, 19500, domain.AirflowFrontToRear},
		{"PowerEdge R660", 772, 20500, domain.AirflowFrontToRear},
		{"ProLiant DL380 Gen11", 750, 25400, domain.AirflowFrontToRear},
		// THE OPPOSING NEIGHBOUR. Data-centre switches ship in two airflow
		// SKUs so the ports can face the cold aisle, and this estate bought the
		// port-side-intake one. Directly above a front-to-rear server it is
		// being fed exhaust, which is the finding position genuinely decides.
		{"DCS-7050SX3-48YC8", 470, 9100, domain.AirflowRearToFront},
		// A vertical PDU moves no air. 'passive' is a DECLARATION and not the
		// same as nobody having said -- it is what stops the rack's own strip
		// counting toward the undeclared gap.
		{"AP8853", 0, 4500, domain.AirflowPassive},
	} {
		m := m
		if !b.ok() {
			return
		}
		id, ok := b.refs.DeviceTypes[m.model]
		if !ok {
			continue // a phase that did not run; nothing to measure
		}
		dt, err := b.store.GetDeviceType(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("reading device type %s: %w", m.model, err))
			return
		}
		updated := dt.DeviceType
		if m.depth > 0 {
			updated.DepthMM = num(m.depth)
		}
		updated.WeightGrams = num(m.grams)
		updated.Airflow = str(m.airflow)
		if err := b.store.UpdateDeviceType(b.ctx, Actor, &updated); err != nil {
			b.fail(fmt.Errorf("measuring device type %s: %w", m.model, err))
			return
		}
		b.refs.DeviceTypes[m.model] = updated.ID
	}
}

// measureRacks records what somebody went round with a tape measure and found.
func (b *builder) measureRacks() {
	for _, r := range []struct {
		name              string
		width, depth, kgs int
	}{
		// The properly specified cabinet. A 772mm server plus its cabling fits
		// in 900mm with room, and 800mm of width leaves the side channels
		// usable. Nothing here reports, which is the point of it being here.
		{"rack-a1", 800, 900, 800},
		// The shallow comms cabinet somebody put servers in. 600mm of usable
		// depth cannot take a 772mm chassis at all, and 600mm of width leaves
		// about 55mm a side -- the same 55mm the cable bundles are using.
		{"rack-a2", 600, 600, 600},
		// Rented and deep, and capped at what the colo contract allows rather
		// than at what the cabinet could bear: 450kg is about the 1000 lb limit
		// these agreements usually carry. Three populated shelves and four
		// servers go past it, which is the finding -- and it is a CONTRACTUAL
		// fault, not a structural one, which is exactly the sort of thing an
		// inventory is for and a floor inspection is not.
		{"colo-rack-07", 800, 1000, 450},
		// rack-b1 is deliberately absent: it records no height either, and an
		// estate always has one cabinet nobody has been round to yet. Its
		// contents report as unknown rather than as fitting.
	} {
		r := r
		if !b.ok() {
			return
		}
		id, ok := b.refs.Assets[r.name]
		if !ok {
			continue
		}
		row, err := b.store.GetAsset(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("reading rack %s: %w", r.name, err))
			return
		}
		updated := row.Asset
		updated.WidthMM = num(r.width)
		updated.UsableDepthMM = num(r.depth)
		updated.MaxLoadGrams = num(r.kgs * 1000)
		if err := b.store.UpdateAsset(b.ctx, Actor, &updated, nil); err != nil {
			b.fail(fmt.Errorf("measuring rack %s: %w", r.name, err))
			return
		}
	}
}

// placeTheSideBreather adds the firewall this whole work package came from.
//
// A FortiGate 121G: short-depth, drawing air from both flanks and exhausting
// behind. It is a perfectly good firewall and it is in the wrong cabinet --
// 600mm wide, with the cable bundles running down the same channels its
// intakes face. Nothing about the placement is invalid, nothing refuses it, and
// an inventory that could not say this is one nobody would ask.
func (b *builder) placeTheSideBreather() {
	b.manufacturer("fortinet", "Fortinet", "https://support.fortinet.com/")
	b.model("fortinet", "FortiGate 121G", "FG-121G", 1, "2032-06-30")
	if !b.ok() {
		return
	}
	if id, ok := b.refs.DeviceTypes["FortiGate 121G"]; ok {
		dt, err := b.store.GetDeviceType(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("reading the FortiGate model: %w", err))
			return
		}
		updated := dt.DeviceType
		// Short-depth, which is the other half of its problem: in a deep
		// cabinet it leaves a gap that lets rear air come back round the front.
		updated.DepthMM = num(260)
		updated.WeightGrams = num(4300)
		updated.Airflow = str(domain.AirflowSideToRear)
		if err := b.store.UpdateDeviceType(b.ctx, Actor, &updated); err != nil {
			b.fail(fmt.Errorf("measuring the FortiGate model: %w", err))
			return
		}
	}

	b.asset(domain.KindFirewall, "fw-branch-1", "rack-a2", []string{"dev", "transit"},
		func(a *domain.Asset) {
			a.Vendor, a.Model = str("Fortinet"), str("FortiGate 121G")
			a.DeviceTypeID = b.deviceType("FortiGate 121G")
			a.TeamID = b.team("network")
			a.ManagerRole = str("operator")
			// U20: not the middle of anything in particular, which is the point.
			// Its problem is the cabinet it is in, not where in the cabinet.
			a.RackPosition, a.RackFace = num(20), str(domain.FaceFront)
			a.Attrs = `{"note":"side-intake, rear-exhaust, short depth — in a 600mm cabinet"}`
		})

	// Priced like everything else, because an asset with no cost line is
	// omitted from every total silently -- the estate guard caught this one
	// within a minute of it being added, which is the guard earning its keep.
	//
	// APPROXIMATE. A mid-range firewall is bought once and then renews forever
	// on a support-and-security bundle that costs a third of the hardware every
	// year; the shape is the point and the figures are the right order rather
	// than a quotation.
	b.assetCosts([]costLine{
		{"fw-branch-1", "acquisition", domain.CostOnce, 3200, -420,
			"FortiGate 121G hardware — approximate, not confirmed against a quotation"},
		{"fw-branch-1", "subscription", domain.CostYearly, 1100, -420,
			"FortiCare and security bundle — approximate; renews annually and outlives the box"},
	})
}
