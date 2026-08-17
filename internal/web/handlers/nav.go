// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

// The navigation rail.
//
// IT IS DATA HERE RATHER THAN MARKUP IN THE LAYOUT, for one reason: the rail
// had grown to seventeen links under a single heading called "Inventory",
// gaining one per work package as the roadmap was worked through. That is how
// the code is organised and it is not how anybody thinks about an estate --
// somebody opening this at three in the morning wants "the network", not
// "whatever shipped in D4". Grouping in a template means the grouping is
// decided by whoever last edited the template; grouping here means it is
// decided once and the current section can be resolved rather than guessed.
//
// THE GROUPS ARE QUESTIONS, NOT TABLES. Organisation is who owns it, Estate is
// what it physically is, Network is how it connects, Addressing is what it is
// numbered, Services is what runs on it. A page belongs where somebody would
// look for it, which is why the firewalls appear under Network as a FILTERED
// VIEW of the asset list rather than being moved out of Estate -- see NavLink.
//
// WHY A FIREWALL IS NOT MOVED. It sits in a rack, has an end-of-support date,
// costs money, draws power, has an owning team and carries interfaces that hold
// addresses, VLANs, cables and circuit terminations. It participates in six
// domains, so filing it under any one of them is picking one and being wrong
// for the other five -- the rack elevation, the cost rollup, the expiry report
// and the power chain would each point across a section boundary. The
// classification is unstable too: a hypervisor with bridges, a server running
// FRR and a load balancer are all arguable. One place it lives, several places
// it appears.

// NavLink is one entry in the rail.
type NavLink struct {
	Label string
	Href  string
	// Nav is the page slug this link corresponds to, matched against Base.Nav
	// to mark the current page.
	//
	// A FILTERED VIEW GETS ITS OWN SLUG, which is not the obvious answer and is
	// the one that works. The firewall list is /assets with a query, so the
	// tempting reading is that it is the asset page and should share its slug.
	// It cannot: two links then hold one slug, both light up, and -- the bug
	// this cost -- the group that OPENS is whichever holds the plain entry.
	// Clicking Firewalls under Network expanded Estate instead.
	//
	// The query string is what tells the two apart, and it is a fact the
	// request carries. See AssetListNav.
	Nav string
}

// NavGroup is a heading and the links under it.
type NavGroup struct {
	Label string
	Links []NavLink
	// Open is set by NavFor when this group holds the current page, so the
	// rail arrives already showing where you are. Everything after that is the
	// operator's own toggling, remembered client-side.
	Open bool
}

// navGroups is the rail's structure. Order is roughly outside-in: who owns it,
// what it is, how it connects, what runs on it, then what the reports say.
var navGroups = []NavGroup{
	{Label: "Organisation", Links: []NavLink{
		{Label: "Projects", Href: "/projects", Nav: "projects"},
		{Label: "Teams", Href: "/teams", Nav: "teams"},
		{Label: "Environments", Href: "/environments", Nav: "environments"},
	}},
	{Label: "Estate", Links: []NavLink{
		{Label: "Assets", Href: "/assets", Nav: "assets"},
		{Label: "Catalogue", Href: "/catalogue", Nav: "catalogue"},
		{Label: "Power", Href: "/power", Nav: "power"},
		{Label: "Clusters", Href: "/clusters", Nav: "clusters"},
	}},
	{Label: "Network", Links: []NavLink{
		{Label: "Topology", Href: "/network", Nav: "network"},
		// A view, not a home: these are asset lists with a kind filter already
		// applied. The slug is still their own, because it is what decides
		// which group opens -- see NavLink.Nav and AssetListNav.
		{Label: "Firewalls", Href: "/assets?kind=firewall", Nav: "assets-firewall"},
		{Label: "Switches", Href: "/assets?kind=switch", Nav: "assets-switch"},
		{Label: "Paths", Href: "/paths", Nav: "paths"},
		{Label: "Circuits", Href: "/circuits", Nav: "circuits"},
		{Label: "Overlays", Href: "/overlays", Nav: "l2vpn"},
		{Label: "Redundancy", Href: "/redundancy", Nav: "fhrp"},
	}},
	{Label: "Addressing", Links: []NavLink{
		{Label: "Prefixes", Href: "/prefixes", Nav: "prefixes"},
		{Label: "VLANs", Href: "/vlans", Nav: "vlans"},
		{Label: "Allocations", Href: "/allocations", Nav: "registry"},
	}},
	{Label: "Services", Links: []NavLink{
		{Label: "Services", Href: "/services", Nav: "services"},
		{Label: "Certificates", Href: "/certificates", Nav: "certificates"},
	}},
	{Label: "Reports", Links: []NavLink{
		{Label: "What expires", Href: "/reports/expiry", Nav: "expiry"},
		{Label: "Power findings", Href: "/reports/power", Nav: "power-report"},
		{Label: "What it costs", Href: "/reports/cost", Nav: "cost-report"},
		{Label: "Suppliers", Href: "/reports/suppliers", Nav: "supplier-report"},
		{Label: "Environment spans", Href: "/reports/spanning", Nav: "reports"},
		{Label: "Change log", Href: "/changes", Nav: "changes"},
	}},
	// Help is deliberately absent: it is a drawer trigger with its own HTMX
	// attributes, not a page you navigate to, and the layout renders it at the
	// foot of the rail where it already lived.
	{Label: "Settings", Links: []NavLink{
		{Label: "Vocabularies", Href: "/vocabularies", Nav: "vocabularies"},
		{Label: "Inflation", Href: "/inflation", Nav: "inflation"},
	}},
}

// NavFor returns the rail with the group holding the current page opened.
//
// A copy per request rather than mutating the package-level slice: two requests
// on different pages would otherwise race on Open, and the loser would render
// somebody else's section expanded. Cheap -- seven groups of a handful of
// links, rebuilt per page render, and measurably nothing next to the queries
// every page already runs.
func NavFor(current string) []NavGroup {
	out := make([]NavGroup, len(navGroups))
	for i, g := range navGroups {
		out[i] = g
		for _, l := range g.Links {
			if l.Nav != "" && l.Nav == current {
				out[i].Open = true
				break
			}
		}
	}
	return out
}

// AssetListNav returns the rail slug for the asset list under a kind filter.
//
// The rail has entries under Network -- Firewalls, Switches -- that are this
// same page with ?kind= already applied. Which entry a request belongs to is
// therefore decided by the query string and nothing else: the handler is the
// same handler and the page is the same page. Passing the plain slug for all
// of them put the operator in Estate every time they clicked Firewalls.
//
// READ OUT OF THE RAIL RATHER THAN DECLARED BESIDE IT. The entry's Href is
// where the kind-to-entry mapping already lives, and it is the URL a click
// actually sends, so it is the truth rather than a copy of it. A second table
// here would be the thing that drifts the first time somebody adds a kind.
func AssetListNav(kind string) string {
	if kind == "" {
		return "assets"
	}
	href := "/assets?kind=" + kind
	for _, g := range navGroups {
		for _, l := range g.Links {
			if l.Href == href && l.Nav != "" {
				return l.Nav
			}
		}
	}
	// A kind with no rail entry of its own is still the asset list, reached
	// through the filter box on it. Estate is the right group for that.
	return "assets"
}

// IsCurrent reports whether this link is the page being rendered.
func (l NavLink) IsCurrent(current string) bool {
	return l.Nav != "" && l.Nav == current
}
