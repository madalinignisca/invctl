// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"errors"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// The estate of a small company, layered on the base fixture.
//
// GATED, LIKE THE OBSERVATIONS, and for the same reason. The base fixture is
// shaped for the test suite: every row in it is asserted on somewhere, and the
// false-redundancy pair, the undated asset and the uncatalogued box are all
// deliberate. Adding a colo and four more firewalls to it would make several
// hundred tests describe an estate nobody chose. So this is a second layer that
// only a deployment asks for.
//
// WHAT IT IS MEANT TO LOOK LIKE: a company of maybe forty people that outgrew a
// cupboard. Three racks in its own building, a handful of rented machines in a
// colo an hour away, a firewall per environment because somebody sensible
// insisted, one upstream firewall carrying three ISP handoffs, and a certificate
// on everything because the auditor asked once and nobody wanted to be asked
// twice.
//
// It is written to exercise every feature rather than to be pretty: the colo is
// single-fed on one provider, one rack's A and B feeds trace to the same UPS,
// two certificates are days from expiring and one has already lapsed.

// CompanyEstate makes Load add the layer below. Off by default: a fresh
// deployment gets the honest small fixture, not somebody else's company.
var CompanyEstate bool

func (b *builder) company() {
	b.companySites()
	b.companyFirewalls()
	b.companyUplinks()
	b.companyCertificates()
	// The compute build-out: it prices assets the phases above create.
	b.companyCompute()
	// The rented and cloud tier last, because it RETIRES the on-prem
	// development hosts the compute phase created -- the migration that put
	// development onto somebody else's metal.
	b.companyRented()
	// Measurements go after everything that creates a rack or a model, since
	// this phase reads them back and fills them in.
	b.companyFit()
	// The DR fibre last: it needs the compute layer's Bergen switch.
	b.companyDRLink()
	// The patch panel and its leads: needs the cabinet measured and the port
	// faces declared, so it runs after companyFit.
	b.companyCabling()
	b.companyMoney()
}

// companySites adds the third on-prem rack and the rented colo.
func (b *builder) companySites() {
	// The rack that was added when the third hypervisor arrived. It has no power
	// recorded, which is deliberate: the findings page counts what is NOT
	// modelled, and an estate where somebody added a rack and never came back to
	// the power model is the ordinary state of the world rather than a
	// hypothetical.
	b.asset(domain.KindRack, "rack-a2", "dc-oslo", []string{"prod"}, func(a *domain.Asset) {
		a.UHeight = num(42)
	})

	// The colo: three rented machines and the switch that came with them. A
	// different site, so anything dual-homed across the two is genuinely
	// redundant -- and anything that is not, is not.
	//
	// NO POWER MODEL, and that is the honest answer rather than a gap. You do
	// not own a colo's panels, you cannot trace them, and inventing feeds for
	// them would put a confident diagram behind a fact nobody here can check.
	// The power report counts the site as unmodelled and says so, which is the
	// true statement.
	b.asset(domain.KindSite, "colo-fra1", "", []string{"prod"}, nil)
	b.asset(domain.KindRack, "colo-rack-07", "colo-fra1", []string{"prod"}, func(a *domain.Asset) {
		a.UHeight = num(47) // the colo's racks are taller than ours
	})
	b.asset(domain.KindSwitch, "sw-colo-1", "colo-rack-07", []string{"prod"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("MikroTik"), str("CRS354-48G-4S+2Q+")
		a.Serial = str("HGT89LKM0021")
		a.TeamID = b.team("network")
		a.RackPosition, a.RackFace = num(47), str(domain.FaceFront)
	})

	rented := []struct {
		name, serial string
		u            int
	}{
		{"srv-colo-1", "JX774K2200A1", 44},
		{"srv-colo-2", "JX774K2200A2", 42},
		{"srv-colo-3", "JX774K2200A3", 40},
	}
	for _, r := range rented {
		b.asset(domain.KindServer, r.name, "colo-rack-07", []string{"prod"}, func(a *domain.Asset) {
			a.Vendor, a.Model = str("Dell"), str("PowerEdge R650")
			a.Serial = str(r.serial)
			a.DeviceTypeID = b.deviceType("PowerEdge R650")
			a.TeamID = b.team("platform")
			a.RackPosition, a.RackFace = num(r.u), str(domain.FaceFront)
			// Rented, so the support date is the CONTRACT's, not the vendor's --
			// and it is the case the override exists for.
			a.EOLDate = str(domain.FormatDate(b.now.AddDate(1, 2, 0)))
		})
	}
}

// companyFirewalls gives each environment its own, under one upstream.
func (b *builder) companyFirewalls() {
	// A firewall per environment is what a small company does once somebody has
	// been through an audit: production and development do not share a policy
	// engine, and the transit box in the base fixture is the one that fronts
	// both. fw-edge-1 stays the upstream.
	firewalls := []struct{ name, rack, env, vendor, model string }{
		{"fw-prod-1", "rack-a1", "prod", "Palo Alto", "PA-460"},
		{"fw-dev-1", "rack-a2", "dev", "Palo Alto", "PA-410"},
		{"fw-colo-1", "colo-rack-07", "prod", "MikroTik", "CCR2004-1G-12S+2XS"},
	}
	for _, f := range firewalls {
		b.asset(domain.KindFirewall, f.name, f.rack, []string{f.env, "transit"}, func(a *domain.Asset) {
			a.Vendor, a.Model = str(f.vendor), str(f.model)
			a.TeamID = b.team("network")
			a.ManagerRole = str("operator")
		})
	}

	// The access switches between the servers and their firewall. sw-core-1 and
	// sw-core-2 in the base fixture are the pair that carries everything; these
	// are the cheap ones at the top of each rack.
	tors := []struct{ name, rack, env string }{
		{"sw-tor-a1", "rack-a1", "prod"},
		{"sw-tor-a2", "rack-a2", "dev"},
	}
	for _, s := range tors {
		b.asset(domain.KindSwitch, s.name, s.rack, []string{s.env}, func(a *domain.Asset) {
			a.Vendor, a.Model = str("MikroTik"), str("CRS326-24G-2S+")
			a.TeamID = b.team("network")
			a.RackPosition, a.RackFace = num(42), str(domain.FaceFront)
		})
	}
}

// companyUplinks records who the company buys transit from.
//
// AS INTERFACES, NOT AS A PROVIDER MODEL, and the distinction is worth being
// straight about. Circuits -- a provider, a contract, a commit rate, a monthly
// cost, a contract end date -- are WP-E1 and are not built. What exists today is
// the port the handoff lands on, so that is what this records: three named WAN
// interfaces on the upstream firewall. It is honest about the estate and honest
// about the model, and when E1 arrives these are the ports its terminations
// attach to.
func (b *builder) companyUplinks() {
	edge, ok := b.refs.Assets["fw-edge-1"]
	if !ok {
		b.fail(errors.New("seeding uplinks: fw-edge-1 is missing"))
		return
	}
	uplinks := []struct{ name, note string }{
		{"wan1-nordvind", "primary transit, 1 Gbit"},
		{"wan2-telenor", "secondary transit, 500 Mbit"},
		{"wan3-lyse", "backup, business fibre"},
	}
	for _, u := range uplinks {
		if !b.ok() {
			return
		}
		i, err := domain.NewInterface(store.NewID(), edge, u.name, "sfp+")
		if err != nil {
			b.fail(fmt.Errorf("building uplink %s: %w", u.name, err))
			return
		}
		if err := b.store.CreateInterface(b.ctx, domain.AdministratorPermit(Actor), i); err != nil {
			b.fail(fmt.Errorf("seeding uplink %s: %w", u.name, err))
			return
		}
	}

	// The colo has ONE uplink, which is the point: it is single-homed, and the
	// estate should say so rather than imply parity with the office.
	if colo, ok := b.refs.Assets["sw-colo-1"]; ok {
		i, err := domain.NewInterface(store.NewID(), colo, "wan1-hetzner", "sfp+")
		if err != nil {
			b.fail(fmt.Errorf("building the colo uplink: %w", err))
			return
		}
		if err := b.store.CreateInterface(b.ctx, domain.AdministratorPermit(Actor), i); err != nil {
			b.fail(fmt.Errorf("seeding the colo uplink: %w", err))
		}
	}
}

// companyCertificates gives every internal service its own, and the public
// estate the two it actually needs.
func (b *builder) companyCertificates() {
	// FEW PUBLIC, MANY INTERNAL, which is what a company with an internal CA
	// looks like. The public ones are the expensive, watched, ninety-day sort;
	// the internal ones are issued for a year by something nobody thinks about
	// until it stops.
	certs := []struct {
		cn, issuer string
		days       int
		services   []string
	}{
		{"*.internal.example.no", "Example Internal CA", 210,
			[]string{"orders-api", "billing", "search"}},
		{"vault.internal.example.no", "Example Internal CA", 96, []string{"vault"}},
		{"sso.internal.example.no", "Example Internal CA", -12, []string{"sso"}},
	}
	for _, c := range certs {
		if !b.ok() {
			return
		}
		notAfter := domain.FormatDate(b.now.AddDate(0, 0, c.days))
		cert, err := domain.NewCertificate(store.NewID(), domain.CertificateSpec{
			SubjectCN: c.cn, Issuer: str(c.issuer),
			NotBefore: str(domain.FormatDate(b.now.AddDate(-1, 0, 0))),
			NotAfter:  str(notAfter),
		}, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building certificate %s: %w", c.cn, err))
			return
		}
		cert.TeamID = b.team("platform")
		if err := b.store.CreateCertificate(b.ctx, Actor, cert); err != nil {
			b.fail(fmt.Errorf("seeding certificate %s: %w", c.cn, err))
			return
		}
		for _, code := range c.services {
			id, ok := b.refs.Services[code]
			if !ok {
				continue // a service the base fixture does not have
			}
			if err := b.store.DeployCertificateToService(b.ctx, Actor, cert.ID, id, nil); err != nil {
				b.fail(fmt.Errorf("deploying %s to %s: %w", c.cn, code, err))
				return
			}
		}
	}
}
