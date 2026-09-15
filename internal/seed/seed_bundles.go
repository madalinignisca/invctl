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
	"github.com/madalinignisca/invctl/internal/store"
)

// The duct the inter-rack fibre runs through (WP-B4).
//
// WHAT IT IS FOR, AND IT IS NOT THE CUT PAGE'S HEADLINE. The design doc's
// first stated consumer is that GET /links/{id}/impact "is usually wrong by
// omission for a cable in a duct" -- a backhoe does not pick one strand. Four
// of this estate's cables now answer that: open any one of them and the page
// names the duct and the three others that share its fate. Before this phase
// no cable anywhere in the demo could.
//
// THE CUT ANSWER IS "JOINS NOTHING", AND THAT IS CORRECT RATHER THAN A GAP.
// An earlier draft of this comment claimed the duct was a hidden single point
// of failure the port-level view could not see. The engine says otherwise, and
// the engine is right: cutting all four severs the two MC-LAG peer links --
// both ends of which are in the sw-core group, so they cross no boundary --
// and hv-01's and hv-02's second homes, each of which still has its first. The
// simulation reports nothing lost because nothing is lost. Shipping the claim
// would have put a sentence in the seed that the software on the next screen
// contradicts.
//
// So this is deliberately the template's OWN common case, the one
// bundle_impact.html has a whole panel for: "this bundle joins nothing that is
// modelled". That panel exists because joining nothing and cutting something
// harmlessly look identical in a Result and need opposite sentences
// (store.CircuitCut's doc comment), and until now the demo could not show it.
//
// WHAT THE DEMO STILL CANNOT SHOW is a bundle that SEPARATES the estate. This
// cable plant has exactly two cables crossing a forwarder-group boundary --
// sw-core-1 to fw-edge-1 and sw-core-2 to fw-edge-2 -- and they are 2m DACs in
// different racks, so no honest duct holds both. Manufacturing one would mean
// either lying about where cables run or inventing topology, and what the
// estate IS is a bigger decision than what it demonstrates. Outcome three is
// demonstrated for a CIRCUIT instead, by the Oslo-Bergen dark fibre
// (seed_drlink.go), which is the real backhoe case this company has.

// ductBundle declares the A1-B1 tray and pulls the fibre into it.
//
// THE FOUR MEMBERS ARE EVERY OM4 RUN IN THE ESTATE, which is not a coincidence
// and not a selection: hv-01 and hv-02 sit in rack-a1 and sw-core-2 sits in
// rack-b1, so their second homes leave the rack, and the MC-LAG peer bond
// joins the two cores across the same gap. Fibre at 20m and 25m is fibre that
// left the cabinet. In a real room four such runs between two racks go through
// one tray.
//
// THE DAC RUNS ARE DELIBERATELY NOT MEMBERS. hv-01/eno2, hv-02/eno2 and
// hv-03/eno2 are three-metre direct-attach inside their own rack; they never
// enter the tray. Adding them would make this "every cable near the core"
// rather than a fact about a duct, and a bundle whose membership is a gesture
// teaches a reader to distrust the ones that are not. The recorded lengths are
// what says which is which.
func (b *builder) ductBundle() {
	if !b.ok() {
		return
	}
	// Keyed the way networking() records a cable: by the two interfaces it
	// joins, in the order the cable table declares them. A cable has no name
	// of its own, so this is the only handle that survives the phase boundary.
	members := []string{
		"hv-01/eno3->sw-core-2/Ethernet1",
		"hv-02/eno3->sw-core-2/Ethernet2",
		"sw-core-1/Ethernet46->sw-core-2/Ethernet46",
		"sw-core-1/Ethernet47->sw-core-2/Ethernet47",
	}
	ids := make([]string, 0, len(members))
	for _, key := range members {
		id, ok := b.linkIDs[key]
		if !ok {
			// ALL OR NOTHING, AND SAID OUT LOUD. A bundle that quietly lost
			// half its cables still renders, still answers the cut question,
			// and answers it wrongly -- an operator reading "these two go
			// dark" would take the missing two for safe. Declaring nothing is
			// honest; declaring a subset is worse than declaring none.
			b.skip("cable %s is not in this estate, so the A1-B1 duct was not declared", key)
			return
		}
		ids = append(ids, id)
	}

	bundle, err := domain.NewCableBundle(store.NewID(), domain.CableBundleSpec{
		Code: "duct-a1-b1",
		Name: "Duct A1→B1",
		Description: str("Fibre tray between rack-a1 and rack-b1. Every OM4 run " +
			"between the core racks is pulled through it, so one backhoe, one " +
			"careless contractor or one flooded riser takes all four at once."),
	}, b.now)
	if err != nil {
		b.fail(fmt.Errorf("building the A1-B1 duct: %w", err))
		return
	}
	if err := b.store.CreateBundle(b.ctx, Permit, bundle); err != nil {
		b.fail(fmt.Errorf("seeding the A1-B1 duct: %w", err))
		return
	}
	// Membership is a set replaced wholesale, folded into the bundle's own
	// audited value (docs/cable-bundles-design.md, "Rules"), so this is one
	// more audited write on the bundle rather than four member creations.
	if err := b.store.SetBundleMembers(b.ctx, Permit, bundle.ID, ids); err != nil {
		b.fail(fmt.Errorf("pulling the cables into the A1-B1 duct: %w", err))
		return
	}
}
