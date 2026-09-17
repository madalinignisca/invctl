// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"net/http"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/store"
)

// "The backhoe went through the duct. What goes dark?"
//
// A bundle's own cut view, Task 3 of docs/cable-bundles-design.md: cutting a
// bundle is cutting every LIVE cable pulled through it at once, not one
// strand at a time -- which is the whole reason /links/{id}/impact is
// wrong by omission for a cable somebody bundled. See store.BundleCutEffect
// and store.SQLStore.cutEffect's doc comment for why this rides the same
// walker as a single cable's cut rather than a second implementation.

type bundleImpactPage struct {
	Base
	Bundle *domain.CableBundle
	// Groups is Members folded by breakout_id (docs/breakout-cables-design.md
	// D5) -- what the PAGE counts as "one cable". Membership itself is
	// unchanged: cable_bundle_member still holds one row per strand, and a
	// breakout DAC pulled through this duct is still n member rows in the
	// store. Reporting each of those as its own "cable" would overstate a
	// backhoe's real blast radius by the strand count, so the template
	// renders Groups, never the raw member list.
	Groups []BreakoutMemberGroup
	Result impact.Result
	// Cut is the connectivity answer for the WHOLE bundle -- see
	// store.CircuitCut's doc comment on its three outcomes, and
	// store.BundleCutEffect on why only live members count towards it.
	Cut store.CircuitCut
	// HasImpact and HasNetworkFinding are the shared impact_result partial's
	// contract -- see linkImpactPage's doc comment on why these are not
	// optional.
	HasImpact         bool
	HasNetworkFinding bool
}

// BreakoutMemberGroup is one physical cable's worth of a bundle's membership
// -- an ordinary link's single row on its own, or every membership row of one
// breakout DAC folded into one entry.
//
// docs/breakout-cables-design.md D5, decided rather than left to the reader:
// "The bundle and impact pages group by breakout_id... The store keeps four
// rows." This type is the view-model half of that ruling. It carries no new
// fact -- Strands is exactly the subset of BundleMemberRow already loaded --
// it only changes what COUNTS as one entry on the page.
type BreakoutMemberGroup struct {
	// BreakoutID is "" for an ordinary cable, which is also how a group with
	// exactly one strand is told apart from a breakout that happens to have
	// only one of its strands pulled through this particular duct (see
	// IsBreakout).
	BreakoutID string
	Strands    []store.BundleMemberRow
}

// IsBreakout reports whether this group is a folded breakout, not an
// ordinary cable. A group with a BreakoutID but only ONE strand in it --
// an operator bundled a single leg of a breakout DAC and left the rest out,
// or the rest are simply not in this particular bundle -- displays exactly
// like an ordinary cable: "1 breakout cable (1 strand)" would say something
// the data does not actually show, since this page only knows about the
// strand that IS a member here.
func (g BreakoutMemberGroup) IsBreakout() bool {
	return g.BreakoutID != "" && len(g.Strands) > 1
}

// groupBundleMembers folds a bundle's membership rows by breakout_id --
// docs/breakout-cables-design.md D5. Order-preserving and single-pass:
// bundleMemberQuery already orders by the a-end, and every strand of one
// breakout shares the SAME a-end by construction (CreateBreakout), so a
// breakout's rows always arrive adjacent and the first one seen decides
// where the group lands.
func groupBundleMembers(members []store.BundleMemberRow) []BreakoutMemberGroup {
	groups := make([]BreakoutMemberGroup, 0, len(members))
	index := make(map[string]int, len(members))
	for _, m := range members {
		if m.BreakoutID == nil {
			groups = append(groups, BreakoutMemberGroup{Strands: []store.BundleMemberRow{m}})
			continue
		}
		if i, ok := index[*m.BreakoutID]; ok {
			groups[i].Strands = append(groups[i].Strands, m)
			continue
		}
		index[*m.BreakoutID] = len(groups)
		groups = append(groups, BreakoutMemberGroup{
			BreakoutID: *m.BreakoutID,
			Strands:    []store.BundleMemberRow{m},
		})
	}
	return groups
}

// BundleImpact simulates cutting a whole bundle: every live cable in it, at
// once.
func (a *App) BundleImpact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bundle, err := a.Store.GetBundle(r.Context(), id)
	if err != nil {
		// 404 rather than an empty simulation, for AssetImpact's reason:
		// "nothing breaks" about a scenario nobody ran is the most dangerous
		// answer this tool can produce.
		a.handleStoreError(w, r, err)
		return
	}

	members, err := a.Store.ListBundleMembers(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// Only LIVE members are cut. A retired cable stays in the bundle as
	// history (Task 2's rule) but a backhoe through the duct today does not
	// re-sever a cable somebody already unplugged.
	var liveLinkIDs []string
	for _, m := range members {
		if m.Lifecycle == domain.LifecycleActive {
			liveLinkIDs = append(liveLinkIDs, m.LinkID)
		}
	}

	result, err := a.Store.Simulate(r.Context(), impact.Request{
		CutLinkIDs:    liveLinkIDs,
		WindowSeconds: queryInt(r, "window", 180, 1, 3650),
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	cut, err := a.Store.BundleCutEffect(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := bundleImpactPage{
		Base:      a.base(r, "If "+bundle.Name+" is cut", "bundles"),
		Bundle:    bundle,
		Groups:    groupBundleMembers(members),
		Result:    result,
		Cut:       cut,
		HasImpact: len(result.Services) > 0 || len(result.WontRestart) > 0,
		HasNetworkFinding: len(result.Isolated) > 0 || len(result.Partitions) > 0 ||
			len(result.Unreachable) > 0 || len(result.RedundancyLost) > 0,
	}
	a.Render.Respond(w, r, http.StatusOK, "bundle_impact", "impact_result", data)
}
