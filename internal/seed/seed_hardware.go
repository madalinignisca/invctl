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

// The hardware catalogue and the power chain, seeded so a fresh install shows
// what they are for rather than two empty screens.
//
// EVERY FINDING IS REPRESENTED ON PURPOSE. A fixture that merely contains rows
// proves the tables exist; this one is arranged so the expiry report shows an
// inherited date beside an overridden one, and the power report shows a false
// redundancy, an expected convergence and a single-fed host. If somebody
// removes the rule, the fixture stops demonstrating it and a test says so.

// catalogue seeds manufacturers and the models this estate actually runs.
//
// Runs BEFORE physical(), because an asset names its model at creation.
func (b *builder) catalogue() {
	makers := []struct{ code, name, support string }{
		{"arista", "Arista Networks", "https://www.arista.com/en/support"},
		{"dell", "Dell Technologies", "CONTRACT-4417"},
		{"apc", "Schneider Electric APC", "https://www.apc.com/support"},
	}
	for _, m := range makers {
		if !b.ok() {
			return
		}
		src, err := domain.NewManufacturer(store.NewID(), domain.ManufacturerSpec{
			Code: m.code, Name: m.name, SupportRef: str(m.support),
		}, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building manufacturer %s: %w", m.code, err))
			return
		}
		if err := b.store.CreateManufacturer(b.ctx, Permit, src); err != nil {
			b.fail(fmt.Errorf("seeding manufacturer %s: %w", m.code, err))
			return
		}
		b.refs.Manufacturers[m.code] = src.ID
	}

	models := []struct {
		maker, model, part string
		u                  int
		eol                string
	}{
		// The switches are out of support NEXT YEAR and neither switch states a
		// date of its own -- so both inherit, and the expiry report gains two
		// rows nobody typed a date for. That is the catalogue earning its keep.
		{"arista", "DCS-7050SX3-48YC8", "DCS-7050SX3-48YC8-F", 1, "2027-04-30"},
		{"dell", "PowerEdge R650", "P30721-B21", 1, "2029-03-31"},
		{"apc", "AP8853", "AP8853", 0, "2028-06-30"},
	}
	for _, m := range models {
		if !b.ok() {
			return
		}
		spec := domain.DeviceTypeSpec{
			ManufacturerID: b.refs.Manufacturers[m.maker],
			Model:          m.model, PartNumber: str(m.part),
			EOLDate: str(m.eol), FullDepth: m.u > 0,
		}
		if m.u > 0 {
			spec.UHeight = num(m.u)
		}
		d, err := domain.NewDeviceType(store.NewID(), spec, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building device type %s: %w", m.model, err))
			return
		}
		if err := b.store.CreateDeviceType(b.ctx, Permit, d); err != nil {
			b.fail(fmt.Errorf("seeding device type %s: %w", m.model, err))
			return
		}
		b.refs.DeviceTypes[m.model] = d.ID
	}
}

// deviceType is the id of a catalogued model, for an asset's field callback.
func (b *builder) deviceType(model string) *string {
	id, ok := b.refs.DeviceTypes[model]
	if !ok {
		return nil
	}
	return &id
}

// power seeds the supply chain, the boards and what plugs into them.
//
// Runs after physical(), because an input names an asset.
//
// THE SHAPE IS THE ORDINARY 2N BUILD: a generator behind two UPS groups, boards
// under each. It is arranged so the findings page shows all three kinds at
// once, including the one that only exists because the supply layer does --
// hv-01 dual-fed across two boards that share UPS-A.
func (b *builder) power() {
	site := "dc-oslo"

	sources := []struct{ name, kind, parent string }{
		{"GEN-1", domain.SourceGenerator, ""},
		{"UPS-A", domain.SourceUPS, "GEN-1"},
		{"UPS-B", domain.SourceUPS, "GEN-1"},
	}
	for _, s := range sources {
		if !b.ok() {
			return
		}
		spec := domain.PowerSourceSpec{
			SiteID: b.refs.Assets[site], Name: s.name, Kind: s.kind,
		}
		if s.parent != "" {
			id := b.refs.PowerSources[s.parent]
			spec.ParentID = &id
		}
		src, err := domain.NewPowerSource(store.NewID(), spec, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building supply %s: %w", s.name, err))
			return
		}
		if err := b.store.CreatePowerSource(b.ctx, Permit, src); err != nil {
			b.fail(fmt.Errorf("seeding supply %s: %w", s.name, err))
			return
		}
		b.refs.PowerSources[s.name] = src.ID
	}

	// DB-A and DB-A2 are both behind UPS-A. That is the trap the supply layer
	// exists to see: two boards is not two sides.
	boards := []struct{ name, source string }{
		{"DB-A", "UPS-A"},
		{"DB-A2", "UPS-A"},
		{"DB-B", "UPS-B"},
	}
	for _, p := range boards {
		if !b.ok() {
			return
		}
		src := b.refs.PowerSources[p.source]
		panel, err := domain.NewPowerPanel(store.NewID(), domain.PowerPanelSpec{
			SiteID: b.refs.Assets[site], SourceID: &src, Name: p.name,
			Rating: domain.Rating{Voltage: num(400), Amperage: num(63), Phase: str(domain.PhaseThree)},
		}, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building panel %s: %w", p.name, err))
			return
		}
		if err := b.store.CreatePowerPanel(b.ctx, Permit, panel); err != nil {
			b.fail(fmt.Errorf("seeding panel %s: %w", p.name, err))
			return
		}
		b.refs.PowerPanels[p.name] = panel.ID
	}

	feeds := []struct {
		panel, name string
		amps        int
	}{
		{"DB-A", "A1", 16},
		{"DB-A", "A2", 32},
		{"DB-A2", "A3", 32},
		{"DB-B", "B1", 32},
	}
	for _, f := range feeds {
		if !b.ok() {
			return
		}
		feed, err := domain.NewPowerFeed(store.NewID(), domain.PowerFeedSpec{
			PanelID: b.refs.PowerPanels[f.panel], Name: f.name,
			Rating:         domain.Rating{Voltage: num(230), Amperage: num(f.amps), Phase: str(domain.PhaseSingle)},
			MaxUtilisation: domain.DefaultMaxUtilisation,
		}, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building feed %s: %w", f.name, err))
			return
		}
		if err := b.store.CreatePowerFeed(b.ctx, Permit, feed); err != nil {
			b.fail(fmt.Errorf("seeding feed %s: %w", f.name, err))
			return
		}
		b.refs.PowerFeeds[f.panel+"/"+f.name] = feed.ID
	}

	// Each row is a deliberate case, and the comment says which.
	inputs := []struct {
		asset, feed, name string
		draw              int
	}{
		// FALSE REDUNDANCY, the subtle one: two boards, one UPS. Invisible to
		// anything that stops at the panel.
		{"hv-01", "DB-A/A1", "A", 900},
		{"hv-01", "DB-A2/A3", "B", 900},
		// FALSE REDUNDANCY, the obvious one: both leads on the same board.
		{"sw-core-1", "DB-A/A1", "A", 350},
		{"sw-core-1", "DB-A/A2", "B", 350},
		// PROPERLY 2N: one lead per side. Converges only at the generator, which
		// is the design and must not read as a fault.
		{"hv-02", "DB-A/A2", "A", 900},
		{"hv-02", "DB-B/B1", "B", 900},
		// SINGLE-FED, and it carries services -- which is what makes it worth
		// reporting at all.
		{"hv-03", "DB-B/B1", "A", 900},
		// Single-fed and carries nothing: correctly silent, and the control that
		// stops "single-fed" meaning "has one lead".
		{"pdu-a1", "DB-A/A2", "A", 120},
	}
	for _, i := range inputs {
		if !b.ok() {
			return
		}
		in, err := domain.NewPowerInput(store.NewID(), domain.PowerInputSpec{
			AssetID: b.refs.Assets[i.asset], FeedID: b.refs.PowerFeeds[i.feed],
			Name: i.name, DrawVA: num(i.draw),
		}, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building input %s on %s: %w", i.name, i.asset, err))
			return
		}
		if err := b.store.CreatePowerInput(b.ctx, Permit, in); err != nil {
			b.fail(fmt.Errorf("seeding input %s on %s: %w", i.name, i.asset, err))
			return
		}
	}
}
