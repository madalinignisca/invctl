// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package impact

import (
	"fmt"
	"sort"
)

// What else stops being true when these assets go away.
//
// WP-I1, AND THE DEBT IT PAYS DOWN. VLANs, first-hop redundancy groups and
// overlays each arrived as a work package with a model, a page and its own
// findings -- and none of them reached the impact engine. So simulating the
// loss of a switch reported the services on it correctly and said nothing
// about the broadcast domain that switch was the only port of, or the VRRP
// group whose last router it carried. The estate was described and the
// consequences of losing part of it were not.
//
// THESE ARE NOT SERVICE IMPACTS AND ARE NOT PROPAGATED. Nothing here changes a
// service's status: no service in this model declares "my gateway is that VIP"
// or "I need VLAN 30", so inventing a propagation would be asserting a
// dependency nobody wrote down -- the exact laundering the audit rules forbid
// for facts, applied to consequences. They are REPORTED, in the same way
// Isolated and RedundancyLost already are, and for the same reason: an operator
// staring at a rack at three in the morning is served by "and VLAN 30 has no
// ports left" whether or not the model can prove which service minded.
//
// THE THREE KINDS SHARE ONE SHAPE, which is why they share one type: each is a
// declared structure that exists only because ports on assets belong to it.
// Take the assets away and the structure is still declared and no longer real.

// Structure kinds.
const (
	StructureVLAN  = "vlan"
	StructureFHRP  = "fhrp"
	StructureL2VPN = "l2vpn"
)

// Structure is a declared thing that depends on ports living on assets.
//
// AssetIDs may repeat -- a switch with four ports in one VLAN appears four
// times -- and the count that matters is of DISTINCT surviving assets, because
// losing a switch takes all four of its ports at once. Deduplicating at load
// time would be tidier and would lose the ability to say how many ports.
type Structure struct {
	Kind string
	ID   string
	Name string
	// AssetIDs holds the asset behind every member port, one entry per member.
	AssetIDs []string
}

// StructureFinding is a structure the outage empties or reduces to one.
type StructureFinding struct {
	Kind string
	Name string
	// Remaining is how many DISTINCT assets still hold a member; Total is how
	// many held one before the outage.
	Remaining int
	Total     int
	// Detail is the finding in a sentence, assembled here: templates must not
	// decide what is wrong, and "0 of 2" needs a reader to know which is worse.
	Detail string
	Href   string
}

// Emptied reports whether nothing is left.
func (f StructureFinding) Emptied() bool { return f.Remaining == 0 }

// analyseStructures reports what the outage empties or leaves standing alone.
//
// TWO STATES ARE WORTH REPORTING and the rest are noise. Emptied: every asset
// holding a member is down, so the structure is declared and carries nothing.
// Reduced to one: it survived and will not survive the next failure, which is
// the finding RedundancyLost already makes for forwarder groups and is the
// whole reason a VRRP group's member count is worth knowing.
//
// A structure that had one member and still has one is NOT reported. It was
// already a single point of failure before this outage and saying so here would
// answer a question nobody asked -- the redundancy page says it, permanently
// and without needing a simulation.
func analyseStructures(structures []Structure, down map[string]bool) []StructureFinding {
	var out []StructureFinding
	for _, st := range structures {
		total, remaining := map[string]bool{}, map[string]bool{}
		for _, id := range st.AssetIDs {
			total[id] = true
			if !down[id] {
				remaining[id] = true
			}
		}
		if len(total) == 0 {
			continue // declared and empty before the outage; not this report's finding
		}
		switch {
		case len(remaining) == 0:
			out = append(out, StructureFinding{
				Kind: st.Kind, Name: st.Name,
				Remaining: 0, Total: len(total),
				Detail: emptiedDetail(st.Kind, len(total)),
				Href:   structureHref(st.Kind, st.ID),
			})
		case len(remaining) == 1 && len(total) > 1:
			out = append(out, StructureFinding{
				Kind: st.Kind, Name: st.Name,
				Remaining: 1, Total: len(total),
				Detail: reducedDetail(st.Kind, len(total)),
				Href:   structureHref(st.Kind, st.ID),
			})
		}
	}
	// Emptied before reduced, then by name, so the worst is first and the order
	// does not shift between runs on the same estate.
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Remaining, out[j].Remaining; a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func emptiedDetail(kind string, total int) string {
	switch kind {
	case StructureVLAN:
		return fmt.Sprintf("no ports left; the broadcast domain is declared and carries "+
			"nothing (all %d assets holding a port are down)", total)
	case StructureFHRP:
		return fmt.Sprintf("no router left to answer for the virtual address "+
			"(all %d are down)", total)
	case StructureL2VPN:
		return fmt.Sprintf("nothing terminates into it any more (all %d ends are down)", total)
	default:
		return "nothing left"
	}
}

func reducedDetail(kind string, total int) string {
	switch kind {
	case StructureVLAN:
		return fmt.Sprintf("one asset left holding a port, of %d — the broadcast domain "+
			"does not survive the next failure", total)
	case StructureFHRP:
		return fmt.Sprintf("one router left of %d — it survived this and the protocol "+
			"now buys nothing", total)
	case StructureL2VPN:
		return fmt.Sprintf("one end left of %d — the overlay connects nothing to "+
			"anything", total)
	default:
		return "one left"
	}
}

func structureHref(kind, id string) string {
	switch kind {
	case StructureVLAN:
		return "/vlans/" + id
	case StructureFHRP:
		return "/redundancy/" + id
	case StructureL2VPN:
		return "/overlays/" + id
	default:
		return ""
	}
}
