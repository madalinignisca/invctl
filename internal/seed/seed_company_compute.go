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

// The compute build-out: five clusters on three hypervisor platforms.
//
// WHY THREE PLATFORMS RATHER THAN ONE. A single-platform estate makes the cost
// report a multiplication table -- every host the same line, every total a
// count times a constant. Nobody needs a report for that. The reason this
// feature exists is that the same rack of identical metal costs wildly
// different money depending on what is installed on it, and that the licence is
// usually the larger half. Three platforms put four cost SHAPES side by side:
//
//   Proxmox production    EUR 500/yr    recurring, cheap, per host
//   Proxmox non-production EUR 100/yr   the same product, a fifth of the price
//   VMware                EUR 3,000/yr  recurring, six times the Proxmox line
//   Hyper-V               EUR 8,500 ONCE perpetual -- huge capital, no renewal
//
// The last one is the interesting row. A yearly figure and a one-off figure
// cannot be compared by looking at them, which is why costs are reported
// amortised over the asset's life rather than summed: Hyper-V looks ruinous
// next to Proxmox until it is spread over six years, and then it does not.
// An estate with one platform never asks that question.
//
// PRICES ARE PUBLIC LIST, APPROXIMATED, and deliberately round. Windows Server
// Datacenter is licensed per core with a 16-core floor in 2-core packs, so a
// 24-core host is twelve packs; at roughly EUR 710 a pack that is about EUR
// 8,500. vSphere is per core per year, so a 24-core host is around EUR 3,000.
// Proxmox is per socket per year and the two figures used here sit on its
// Standard and Community tiers. Real invoices are none of these numbers --
// channel, term and volume move all of them -- and the note on each line says
// what the figure is FOR so a reader can substitute their own. It is a demo,
// not a quote.

// companyCompute adds the clusters, the DR site and the money.
//
// Written to be re-runnable: everything it creates is name-keyed and skipped if
// present, so it is the one phase TopUp can apply to a live estate.
func (b *builder) companyCompute() {
	b.computeEnvironments()
	b.computeCatalogue()
	b.drSite()
	b.clusters()
	b.computeCosts()
	b.estateCosts()
}

// computeEnvironments adds the two the build-out needs.
func (b *builder) computeEnvironments() {
	// Staging is NOT in audit scope and DR is, which is the pair worth having in
	// a fixture: people assume scope follows importance, and it follows DATA.
	// A staging box holds nothing real; a DR box holds a copy of production and
	// is therefore every bit as in-scope as the thing it stands behind.
	b.environment("staging", "Staging", domain.EnvRoleStaging, false, 3)
	b.environment("dr", "Disaster Recovery", domain.EnvRoleDR, true, 1)
}

// computeCatalogue adds the models the new hosts are, so their support dates
// are inherited rather than typed onto twelve assets by hand.
func (b *builder) computeCatalogue() {
	b.manufacturer("hpe", "Hewlett Packard Enterprise", "https://support.hpe.com")
	b.model("hpe", "ProLiant DL380 Gen11", "P52560-B21", 2, "2031-10-31")
	b.model("dell", "PowerEdge R660", "P54321-B21", 1, "2031-03-31")
}

// drSite is the second rented location: a warm standby an hour up the coast.
func (b *builder) drSite() {
	// RENTED, so no power model -- the same honest gap as the colo. You do not
	// own the panels in somebody else's building, you cannot trace them, and a
	// diagram drawn from a guess is worse than an admitted blank.
	b.asset(domain.KindSite, "dr-bergen", "", []string{"dr"}, nil)
	b.asset(domain.KindRack, "dr-rack-01", "dr-bergen", []string{"dr"}, func(a *domain.Asset) {
		a.UHeight = num(42)
	})
	b.asset(domain.KindSwitch, "sw-dr-1", "dr-rack-01", []string{"dr"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Arista"), str("DCS-7050SX3-48YC8")
		a.Serial = str("JPE20341188")
		a.DeviceTypeID = b.deviceType("DCS-7050SX3-48YC8")
		a.TeamID = b.team("network")
		a.RackPosition, a.RackFace = num(42), str(domain.FaceFront)
	})
	b.asset(domain.KindFirewall, "fw-dr-1", "dr-rack-01", []string{"dr"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Palo Alto"), str("PA-460")
		a.Serial = str("PA46-DR-00713")
		a.TeamID = b.team("network")
		a.RackPosition, a.RackFace = num(40), str(domain.FaceFront)
	})

	// Staging gets its own too, which is the rule the company adopted after the
	// audit: an environment without its own policy engine is an environment
	// sharing production's.
	b.asset(domain.KindFirewall, "fw-stg-1", "rack-a2", []string{"staging"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Palo Alto"), str("PA-450")
		a.Serial = str("PA45-STG-02219")
		a.TeamID = b.team("network")
		a.RackPosition, a.RackFace = num(38), str(domain.FaceFront)
	})
}

// cluster is one group of identical hosts.
type cluster struct {
	prefix   string // host names are prefix-01, -02, -03
	rack     string
	envs     []string
	platform string // recorded on the asset, and what the licence line prices
	vendor   string
	model    string // catalogue entry, so EOL is inherited
	serial   string // prefix; the host number is appended
	firstU   int
	stride   int // how many U each host takes
	team     string
}

// clusters builds the four new ones. hv-01..03 already exist and are Proxmox;
// they gain a subscription line below rather than being rebuilt.
func (b *builder) clusters() {
	for _, c := range []cluster{
		// The Windows estate: directory, file services and the .NET application
		// nobody has rewritten. On HPE because that is what the Windows team
		// has always bought, which is the honest reason most estates are mixed.
		{"hv-win", "rack-b1", []string{"prod"}, "Hyper-V on Windows Server 2022 Datacenter",
			"HPE", "ProLiant DL380 Gen11", "CZ23410", 10, 2, "platform"},

		// The VMware cluster is the one they are trying to get OFF, which is why
		// it runs staging rather than production: the licence renewal is the
		// forcing function and the report is where that argument gets made.
		{"hv-esx", "rack-a2", []string{"staging"}, "VMware vSphere 8",
			"Dell", "PowerEdge R660", "JX881M33", 10, 1, "platform"},

		// Development, on the previous generation of production hardware. It was
		// not bought for this, it was inherited -- so the acquisition line is a
		// book value rather than an invoice, and it is marked as such.
		{"hv-dev", "rack-a2", []string{"dev"}, "Proxmox VE 8",
			"Dell", "PowerEdge R650", "JX774K21", 20, 1, "platform"},

		// DR: the same platform as production, because a standby on a different
		// hypervisor is a standby nobody has ever actually failed over to.
		{"hv-dr", "dr-rack-01", []string{"dr"}, "Proxmox VE 8",
			"Dell", "PowerEdge R660", "JX881M55", 10, 1, "platform"},
	} {
		c := c
		for i := 1; i <= 3; i++ {
			i := i
			name := fmt.Sprintf("%s-%02d", c.prefix, i)
			b.asset(domain.KindHypervisor, name, c.rack, c.envs, func(a *domain.Asset) {
				a.Vendor, a.Model = str(c.vendor), str(c.model)
				a.Serial = str(fmt.Sprintf("%s%02d", c.serial, i))
				a.DeviceTypeID = b.deviceType(c.model)
				a.TeamID = b.team(c.team)
				a.RackPosition = num(c.firstU + (i-1)*c.stride)
				a.RackFace = str(domain.FaceFront)
				// Opaque by design -- nothing filters on it, and the moment
				// something needs to, it stops being an attribute and becomes a
				// column. Recorded because the licence on the line below is only
				// explicable next to it.
				a.Attrs = fmt.Sprintf(`{"hypervisor_platform":%q}`, c.platform)
			})
		}
	}
}

// computeCosts prices the platforms. This is the phase the whole build-out is
// arranged to make legible.
func (b *builder) computeCosts() {
	lines := []costLine{
		// Proxmox on the EXISTING production cluster. The hosts were seeded with
		// an acquisition line and no subscription, which is the state of an
		// estate that recorded what it bought and never recorded what it renews
		// -- the more expensive of the two mistakes over a six-year life.
		{"hv-01", "subscription", domain.CostYearly, 500, -1035, "Proxmox VE Standard, per socket, production"},
		{"hv-02", "subscription", domain.CostYearly, 500, -700, "Proxmox VE Standard, per socket, production"},
		{"hv-03", "subscription", domain.CostYearly, 500, -320, "Proxmox VE Standard, per socket, production"},
	}

	// The four new clusters: metal, then licence. Two lines per host, because
	// they are separately cancellable and separately negotiable -- and because
	// summing them into one number is what makes a renewal invisible until it
	// arrives.
	type priced struct {
		prefix              string
		hardware            int64
		hardwareNote        string
		licence             int64
		licencePeriod, note string
		boughtDays          int
	}
	for _, p := range []priced{
		{"hv-win", 11400, "2U, dual 12-core, 512 GB", 8500, domain.CostOnce,
			"Windows Server 2022 Datacenter, 24 cores, 12x 2-core packs", -640},
		{"hv-esx", 10200, "1U, dual 12-core, 384 GB", 3000, domain.CostYearly,
			"VMware vSphere Foundation, 24 cores, per year", -880},
		{"hv-dev", 3200, "1U, repurposed from the 2023 production refresh -- book value, not an invoice",
			100, domain.CostYearly, "Proxmox VE Community, per socket, non-production", -1400},
		{"hv-dr", 9600, "1U, dual 12-core, 384 GB", 500, domain.CostYearly,
			"Proxmox VE Standard, per socket -- DR carries production data", -420},
	} {
		p := p
		for i := 1; i <= 3; i++ {
			name := fmt.Sprintf("%s-%02d", p.prefix, i)
			lines = append(lines,
				costLine{name, "acquisition", domain.CostOnce, p.hardware, p.boughtDays, p.hardwareNote},
				costLine{name, "licence", p.licencePeriod, p.licence, p.boughtDays, p.note},
			)
		}
	}

	// The DR site itself. Rented space and a second carrier -- the running cost
	// of an insurance policy, which is the number people forget when they ask
	// why DR is expensive.
	lines = append(lines,
		costLine{"dr-rack-01", "operating", domain.CostMonthly, 410, -430, "full rack, 4 kW committed"},
		costLine{"dr-bergen", "connectivity", domain.CostMonthly, 190, -430, "500 Mbit, single carrier"},
		costLine{"sw-dr-1", "acquisition", domain.CostOnce, 5400, -430, "48x 25G, 8x 100G uplink"},
		costLine{"fw-dr-1", "acquisition", domain.CostOnce, 4200, -430, ""},
		costLine{"fw-dr-1", "support", domain.CostYearly, 890, -430, "threat prevention and URL filtering"},
		costLine{"fw-stg-1", "acquisition", domain.CostOnce, 3100, -300, ""},
		costLine{"fw-stg-1", "support", domain.CostYearly, 640, -300, "threat prevention, no URL filtering"},
	)

	b.assetCosts(lines)
}

// estateCosts prices what was already there and carried no figure.
//
// Not decoration. A rollup that silently omits half the estate is worse than no
// rollup, because it is confidently wrong -- and an unpriced asset is invisible
// to every total on the costs page while looking perfectly present on every
// other one.
func (b *builder) estateCosts() {
	b.assetCosts([]costLine{
		// Space and transit at the two rented sites, and the third on-prem rack.
		{"rack-a2", "operating", domain.CostMonthly, 380, -700, "half rack, 2 kW committed"},
		{"colo-rack-07", "operating", domain.CostMonthly, 640, -560, "full rack, 4 kW committed"},
		{"colo-fra1", "connectivity", domain.CostMonthly, 240, -560, "1 Gbit, single carrier"},

		// The colo machines are RENTED, so they have no acquisition line at all
		// -- a monthly fee that includes the hardware. It is the shape that
		// breaks a naive "capital plus support" report, which is why it is here.
		{"srv-colo-1", "operating", domain.CostMonthly, 310, -560, "rented, hardware replacement included"},
		{"srv-colo-2", "operating", domain.CostMonthly, 310, -560, "rented, hardware replacement included"},
		{"srv-colo-3", "operating", domain.CostMonthly, 310, -560, "rented, hardware replacement included"},

		// Firewalls: the box, then the subscription that makes it a firewall
		// rather than a router. Cancelling the second leaves the first running
		// and useless, which is why they are two lines.
		{"fw-prod-1", "acquisition", domain.CostOnce, 5600, -900, ""},
		{"fw-prod-1", "support", domain.CostYearly, 1450, -900, "threat prevention, URL filtering, 24x7"},
		{"fw-dev-1", "acquisition", domain.CostOnce, 2900, -900, ""},
		{"fw-dev-1", "support", domain.CostYearly, 610, -900, "threat prevention, business hours"},
		{"fw-colo-1", "acquisition", domain.CostOnce, 2900, -560, ""},
		{"fw-colo-1", "support", domain.CostYearly, 610, -560, "threat prevention, business hours"},

		// Switching and the PDU.
		{"sw-tor-a1", "acquisition", domain.CostOnce, 4700, -1035, "48x 25G top-of-rack"},
		{"sw-tor-a2", "acquisition", domain.CostOnce, 4700, -700, "48x 25G top-of-rack"},
		{"sw-colo-1", "acquisition", domain.CostOnce, 3900, -560, "48x 1G, 4x 10G uplink"},
		{"sw-oob-1", "acquisition", domain.CostOnce, 890, -1035, "out-of-band management, 24x 1G"},
		{"pdu-a1", "acquisition", domain.CostOnce, 1180, -1400, "switched, metered per outlet"},
	})
}
