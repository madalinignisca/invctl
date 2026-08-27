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

// The third tier: rented metal and cloud, for the things nobody buys hardware
// for.
//
// WHY A SMALL COMPANY LOOKS LIKE THIS. It owns the hardware production runs on,
// because that is where the data is and where the auditor looks. It rents a
// rack in a colo for DR, because owning a second building is absurd at this
// size. And it puts everything else -- development, CI, staging, the monitoring
// that must not live inside what it monitors, the offsite backup copy -- on
// whatever is cheapest that month. That last tier is the one CMDBs usually miss,
// and it is where a surprising amount of the estate ends up.
//
// EVERY HOST HERE HAS A REASON, and the reasons are the point of a demo:
//
//	Hetzner        dev and CI. Cheap dedicated metal, and nobody minds an hour
//	               of downtime on a machine that only builds branches.
//	Scaleway       staging and a CI runner. Small, disposable, per-month.
//	OVH            monitoring. DELIBERATELY at a third provider: a monitor
//	               inside the estate it watches goes quiet exactly when it
//	               matters, and a monitor at the SAME provider goes quiet when
//	               that provider does.
//	Hetzner box    the offsite backup copy -- the "1" of 3-2-1, and the reason
//	               it is not at the primary or the DR site.
//
// NO ACQUISITION COST ANYWHERE IN THIS TIER, which is the shape that breaks a
// naive "capital plus support" report: rented capacity is a monthly line and
// nothing else, and an estate whose spend is reported only as depreciation
// misses it entirely.
//
// PRICES ARE REAL LIST PRICES, looked up rather than invented, and rounded to
// the cent as published:
//
//	Hetzner AX52 (Ryzen 7 7700, 64 GB)        EUR 64.00/month
//	Scaleway VPS-PRO-2-L (6 vCPU, 16 GB)      EUR 33.49/month
//	Scaleway VPS-PRO-2-M (4 vCPU, 8 GB)       EUR 17.49/month
//	OVHcloud VPS-2 (4 vCore, 8 GB)            GBP 6.29/month, ~EUR 7.30
//	Hetzner Storage Box BX21 (5 TB)           ~EUR 12/month, approximate
//
// Hetzner restructured its dedicated pricing in June 2026 -- models renamed and
// setup fees rebalanced -- so the AX52 figure is a snapshot of what that
// machine cost, not a quote. The Storage Box figure is the one number here that
// could not be verified from the vendor's page and is marked as approximate on
// its cost line, because a demo that cannot tell a checked figure from a
// remembered one teaches the wrong habit.

// companyRented adds the rented and cloud tier.
func (b *builder) companyRented() {
	b.rentedSites()
	b.rentedHosts()
	b.rentedCluster()
	b.rentedCosts()
	b.retireOnPremDev()
}

// rentedSites: three providers, because the reasons differ.
//
// A provider's region is modelled as a SITE even though nothing here owns a
// building in it. That is the honest reading: a site is where things are, and
// "which failure domain is this in" is the question the model exists to answer.
// Two hosts at Hetzner Helsinki share a failure domain in a way that a host at
// Hetzner and a host at Scaleway do not, and only a site says so.
func (b *builder) rentedSites() {
	for _, s := range []struct{ name, note string }{
		{"hz-hel1", "Hetzner, Helsinki — rented metal for development and CI"},
		{"scw-par1", "Scaleway, Paris — staging and a build runner"},
		{"ovh-gra", "OVHcloud, Gravelines — monitoring only, deliberately a third provider"},
	} {
		b.asset(domain.KindSite, s.name, "", []string{"dev"}, func(a *domain.Asset) {
			a.Attrs = fmt.Sprintf(`{"note":%q}`, s.note)
		})
	}
}

// rentedHosts: what is actually running out there.
func (b *builder) rentedHosts() {
	type host struct {
		name, site, kind, vendor, model, env, note string
	}
	for _, h := range []host{
		// Dev and CI on cheap dedicated metal. Two of them, so the cluster
		// below has something to relocate between.
		{"srv-hz-1", "hz-hel1", domain.KindHypervisor, "Hetzner", "AX52", "dev",
			"Ryzen 7 7700, 64 GB, 2x1TB NVMe — Proxmox"},
		{"srv-hz-2", "hz-hel1", domain.KindHypervisor, "Hetzner", "AX52", "dev",
			"Ryzen 7 7700, 64 GB, 2x1TB NVMe — Proxmox"},

		// Staging and a runner: small, disposable, replaced rather than repaired.
		{"vps-stg-1", "scw-par1", domain.KindVM, "Scaleway", "VPS-PRO-2-L", "staging",
			"6 vCPU, 16 GB — the staging application"},
		{"vps-ci-1", "scw-par1", domain.KindVM, "Scaleway", "VPS-PRO-2-M", "dev",
			"4 vCPU, 8 GB — the build runner"},

		// THE ONE THAT MATTERS ARCHITECTURALLY. Monitoring at a third provider,
		// because a monitor inside the estate it watches goes quiet exactly when
		// it is needed, and one at the same provider goes quiet when that
		// provider does.
		{"vps-mon-1", "ovh-gra", domain.KindVM, "OVHcloud", "VPS-2", "prod",
			"4 vCore, 8 GB — monitoring and the public status page"},

		// The offsite copy. Not a server: a storage service, which is why it is
		// modelled as storage and carries no environment of its own.
		{"bkp-hz-1", "hz-hel1", domain.KindStorage, "Hetzner", "Storage Box BX21", "prod",
			"5 TB — the offsite backup copy, the '1' of 3-2-1"},
	} {
		b.asset(h.kind, h.name, h.site, []string{h.env}, func(a *domain.Asset) {
			a.Vendor, a.Model = str(h.vendor), str(h.model)
			a.TeamID = b.team("platform")
			// NO EOL DATE, and that is not an omission. Rented capacity has no
			// end of support: the contract is monthly and the provider replaces
			// failed hardware without asking. Giving it a date would invent an
			// obligation nobody has.
			a.Attrs = fmt.Sprintf(`{"note":%q}`, h.note)
		})
	}
}

// rentedCluster makes the two Hetzner boxes carry each other.
//
// Two hosts and no capacity floor, so losing one relocates -- the SURVIVABLE
// case, which the estate otherwise only demonstrates by its absence: the
// Windows cluster needs all three of its hosts and the fixture's cluster needs
// all three of its own. An operator should be able to see both answers.
func (b *builder) rentedCluster() {
	if !b.ok() {
		return
	}
	// Already there: leave it alone. Unlike b.asset and assetCosts, which carry
	// their own skip, a cluster is created directly here -- so without this the
	// second run of a top-up dies on the unique name, and the run that dies
	// writes nothing, which is easy to mistake for having been idempotent all
	// along. It was mistaken for exactly that once.
	existing, err := b.store.ListClusters(b.ctx)
	if err != nil {
		b.fail(fmt.Errorf("reading clusters: %w", err))
		return
	}
	for _, e := range existing {
		if e.Name == "dev-hetzner" {
			return
		}
	}

	c, err := domain.NewCluster(store.NewID(), "dev-hetzner", domain.ClusterProxmox)
	if err != nil {
		b.fail(fmt.Errorf("building the Hetzner cluster: %w", err))
		return
	}
	c.HAPolicy = domain.HARestart
	c.Description = str("two rented boxes; losing one is survivable, which is why " +
		"development lives here and production does not")
	if err := b.store.CreateCluster(b.ctx, Actor, c); err != nil {
		b.fail(fmt.Errorf("seeding the Hetzner cluster: %w", err))
		return
	}
	var members []domain.ClusterMember
	for _, name := range []string{"srv-hz-1", "srv-hz-2"} {
		id, ok := b.refs.Assets[name]
		if !ok {
			b.fail(fmt.Errorf("seeding the Hetzner cluster: unknown host %s", name))
			return
		}
		members = append(members, domain.ClusterMember{ClusterID: c.ID, AssetID: id})
	}
	if err := b.store.SetClusterMembers(b.ctx, Actor, c.ID, members); err != nil {
		b.fail(fmt.Errorf("seeding the Hetzner cluster members: %w", err))
	}
}

// rentedCosts: monthly, and only monthly.
func (b *builder) rentedCosts() {
	b.assetCosts([]costLine{
		{"srv-hz-1", "operating", domain.CostMonthly, 64, -540,
			"Hetzner AX52 — Ryzen 7 7700, 64 GB, list price"},
		{"srv-hz-2", "operating", domain.CostMonthly, 64, -540,
			"Hetzner AX52 — Ryzen 7 7700, 64 GB, list price"},
		{"vps-stg-1", "operating", domain.CostMonthly, 33, -300,
			"Scaleway VPS-PRO-2-L — 6 vCPU, 16 GB, list price EUR 33.49"},
		{"vps-ci-1", "operating", domain.CostMonthly, 17, -300,
			"Scaleway VPS-PRO-2-M — 4 vCPU, 8 GB, list price EUR 17.49"},
		{"vps-mon-1", "operating", domain.CostMonthly, 7, -700,
			"OVHcloud VPS-2 — 4 vCore, 8 GB, GBP 6.29 converted"},
		// The one figure here that was not verified against the vendor's own
		// page. Said on the line rather than left to look like the others.
		{"bkp-hz-1", "operating", domain.CostMonthly, 12, -700,
			"Hetzner Storage Box BX21, 5 TB — approximate, not confirmed against " +
				"the price list"},

		// The sites themselves cost nothing: there is no rack rental and no
		// transit bill when you rent a machine. A zero would be wrong -- it
		// would claim somebody checked -- so there is simply no line.
	})
}

// retireOnPremDev records the migration that put development out there.
//
// Retiring rather than deleting, which is the rule everywhere here, and it is
// what makes the estate tell a story instead of showing a snapshot: three
// hypervisors that used to run development sit in the change log with the date
// they were withdrawn, and the machines that replaced them cost a tenth as much
// and are somebody else's to fix.
func (b *builder) retireOnPremDev() {
	for _, name := range []string{"hv-dev-01", "hv-dev-02", "hv-dev-03"} {
		if !b.ok() {
			return
		}
		id, ok := b.refs.Assets[name]
		if !ok {
			continue // the compute layer may not have run; nothing to retire
		}
		if err := b.store.RetireAsset(b.ctx, domain.AdministratorPermit(Actor), id); err != nil {
			b.fail(fmt.Errorf("retiring %s: %w", name, err))
			return
		}
	}
}
