// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"context"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// Adding to an estate that is already running.
//
// Load only ever populates an EMPTY database -- it has to, because it creates
// every row unconditionally and a second run would collide on the first unique
// code it reached. That is the right behaviour for a fixture, and it is useless
// for a demo that is live and must not be reset.
//
// So this is the other half: hydrate the reference maps from what is already
// there, then run a phase with the creators in skip-what-exists mode. The phase
// itself is the SAME code the fresh seed runs, which is the point -- a growing
// estate described in two places would drift the first time one of them was
// edited, and the drift would only be visible on a redeploy nobody was watching.
//
// EVERY WRITE STILL GOES THROUGH THE STORE, so every declared row it adds gets
// its change_log entry in the same transaction. A top-up that reached for the
// database directly would be faster and would silently produce an estate whose
// history began mid-sentence.

// AssetHydrationLimit bounds the hydration read. ListAssets defaults to a page
// size meant for a screen; a top-up needs every asset, because a name it cannot
// see is a name it will recreate.
const AssetHydrationLimit = 100000

// TopUp adds the phases below to a database that already holds an estate, and
// is safe to run repeatedly: anything already present is left untouched.
func TopUp(ctx context.Context, s *store.SQLStore) (*Refs, error) {
	b := &builder{
		ctx:   ctx,
		store: s,
		now:   s.Now(),
		topUp: true,

		interfaceIDs: map[string]string{},
		identityIDs:  map[string]string{},
		poolIDs:      map[string]string{},

		refs: &Refs{
			Environments:  map[string]string{},
			Assets:        map[string]string{},
			Services:      map[string]string{},
			Teams:         map[string]string{},
			Projects:      map[string]string{},
			Endpoints:     map[string]string{},
			Routes:        map[string]string{},
			NetGroups:     map[string]string{},
			Manufacturers: map[string]string{},
			DeviceTypes:   map[string]string{},
			PowerSources:  map[string]string{},
			PowerPanels:   map[string]string{},
			PowerFeeds:    map[string]string{},
		},
	}
	if err := b.hydrate(); err != nil {
		return nil, fmt.Errorf("reading the existing estate: %w", err)
	}

	// The compute build-out and the rented tier. Only these: the rest of
	// company() creates interfaces and certificates whose uniqueness is not
	// name-scoped, so re-running it would not skip, it would collide. A phase
	// joins this list once it is idempotent, not merely because it is new.
	//
	// companyRented is idempotent in both halves -- b.asset skips a name that
	// exists, assetCosts skips a matching line, and RetireAsset returns without
	// writing when the asset is already retired.
	b.companyCompute()
	b.companyRented()
	// Idempotent: it UPDATES models and racks to the same values on a second
	// run, and b.asset skips the firewall once it exists.
	b.companyFit()
	// Idempotent: the group, the interfaces and the circuit each skip when
	// already present.
	b.companyDRLink()

	if b.err != nil {
		return nil, b.err
	}
	return b.refs, nil
}

// hydrate fills the reference maps from the live database, so a phase written
// against a fresh seed can resolve the parents, teams and models it names.
func (b *builder) hydrate() error {
	envs, err := b.store.ListEnvironments(b.ctx)
	if err != nil {
		return fmt.Errorf("environments: %w", err)
	}
	for _, e := range envs {
		b.refs.Environments[e.Code] = e.ID
	}

	// RETIRED ASSETS INCLUDED, and this is not a detail. b.asset skips a name
	// already in these refs, so a hydration that omitted retired rows would not
	// see what a previous run had withdrawn -- and would helpfully recreate it.
	// The partial unique index permits that (a name is unique among LIVE rows),
	// so nothing would object: the second run would quietly resurrect three
	// retired hypervisors and re-price them.
	//
	// Found by running the top-up twice and counting, which is the only way this
	// surfaces. It appeared the moment a phase retired something, and would have
	// been invisible for as long as every phase only created.
	assets, err := b.store.ListAssets(b.ctx, store.AssetFilter{IncludeRetired: true, Limit: AssetHydrationLimit})
	if err != nil {
		return fmt.Errorf("assets: %w", err)
	}
	for _, a := range assets {
		b.refs.Assets[a.Name] = a.ID
	}

	teams, err := b.store.TeamOptions(b.ctx)
	if err != nil {
		return fmt.Errorf("teams: %w", err)
	}
	for _, t := range teams {
		b.refs.Teams[t.Code] = t.ID
	}

	makers, err := b.store.ListManufacturers(b.ctx, true)
	if err != nil {
		return fmt.Errorf("manufacturers: %w", err)
	}
	for _, m := range makers {
		b.refs.Manufacturers[m.Code] = m.ID
	}

	types, err := b.store.ListDeviceTypes(b.ctx, store.DeviceTypeFilter{})
	if err != nil {
		return fmt.Errorf("device types: %w", err)
	}
	for _, d := range types {
		b.refs.DeviceTypes[d.Model] = d.ID
	}

	// NET GROUPS, and this one was added after the top-up died on a duplicate.
	// A phase that checks b.refs before creating is only idempotent if the refs
	// were hydrated -- an empty map means "not there" for everything, so the
	// check passes and the insert conflicts. That is the same failure the
	// retired-asset hydration had, in a different map: the guard is not the
	// check, it is the check PLUS the hydration that makes it true.
	groups, err := b.store.ListNetGroups(b.ctx)
	if err != nil {
		return fmt.Errorf("net groups: %w", err)
	}
	for _, g := range groups {
		b.refs.NetGroups[g.Code] = g.ID
	}
	return nil
}

// ---------- idempotent creators, for phases that both paths run ----------

// environment adds one if the code is new.
func (b *builder) environment(code, name, role string, inScope bool, criticality int) {
	if !b.ok() {
		return
	}
	if _, exists := b.refs.Environments[code]; exists {
		return
	}
	env, err := domain.NewEnvironment(store.NewID(), code, name, role, inScope, criticality, b.now)
	if err != nil {
		b.fail(fmt.Errorf("building environment %s: %w", code, err))
		return
	}
	if err := b.store.CreateEnvironment(b.ctx, Actor, env); err != nil {
		b.fail(fmt.Errorf("seeding environment %s: %w", code, err))
		return
	}
	b.refs.Environments[code] = env.ID
}

// manufacturer adds one if the code is new.
func (b *builder) manufacturer(code, name, support string) {
	if !b.ok() {
		return
	}
	if _, exists := b.refs.Manufacturers[code]; exists {
		return
	}
	m, err := domain.NewManufacturer(store.NewID(), domain.ManufacturerSpec{
		Code: code, Name: name, SupportRef: str(support),
	}, b.now)
	if err != nil {
		b.fail(fmt.Errorf("building manufacturer %s: %w", code, err))
		return
	}
	if err := b.store.CreateManufacturer(b.ctx, Actor, m); err != nil {
		b.fail(fmt.Errorf("seeding manufacturer %s: %w", code, err))
		return
	}
	b.refs.Manufacturers[code] = m.ID
}

// model adds a catalogue entry if the model name is new.
func (b *builder) model(maker, name, part string, u int, eol string) {
	if !b.ok() {
		return
	}
	if _, exists := b.refs.DeviceTypes[name]; exists {
		return
	}
	makerID, ok := b.refs.Manufacturers[maker]
	if !ok {
		b.fail(fmt.Errorf("cataloguing %s: unknown manufacturer %s", name, maker))
		return
	}
	spec := domain.DeviceTypeSpec{
		ManufacturerID: makerID,
		Model:          name, PartNumber: str(part),
		EOLDate: str(eol), FullDepth: u > 0,
	}
	if u > 0 {
		spec.UHeight = num(u)
	}
	d, err := domain.NewDeviceType(store.NewID(), spec, b.now)
	if err != nil {
		b.fail(fmt.Errorf("building device type %s: %w", name, err))
		return
	}
	if err := b.store.CreateDeviceType(b.ctx, Actor, d); err != nil {
		b.fail(fmt.Errorf("seeding device type %s: %w", name, err))
		return
	}
	b.refs.DeviceTypes[name] = d.ID
}

// assetCosts attaches price lines, skipping any an asset already carries.
//
// The skip compares kind, period and amount rather than the note: a line is the
// same line when it bills the same money on the same cycle, and letting a reworded
// note create a second EUR 500 subscription is exactly the duplicate this guards.
func (b *builder) assetCosts(lines []costLine) {
	for _, line := range lines {
		if !b.ok() {
			return
		}
		id, ok := b.refs.Assets[line.target]
		if !ok {
			b.fail(fmt.Errorf("pricing asset %s: unknown", line.target))
			return
		}
		if b.topUp {
			existing, err := b.store.ListAssetCosts(b.ctx, id)
			if err != nil {
				b.fail(fmt.Errorf("reading the costs on %s: %w", line.target, err))
				return
			}
			if hasCost(existing, line) {
				continue
			}
		}
		spec := domain.CostSpec{
			Kind:        line.kind,
			Period:      line.period,
			AmountMinor: major(line.amount),
		}
		if line.fromDays != 0 {
			from := domain.FormatDate(b.now.AddDate(0, 0, line.fromDays))
			spec.ValidFrom = &from
		}
		if line.note != "" {
			spec.Note = str(line.note)
		}
		c, err := domain.NewCost(store.NewID(), spec, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building the %s cost for %s: %w", line.kind, line.target, err))
			return
		}
		if err := b.store.AddAssetCost(b.ctx, Actor, id, c); err != nil {
			b.fail(fmt.Errorf("pricing asset %s: %w", line.target, err))
			return
		}
	}
}

func hasCost(existing []store.CostRow, line costLine) bool {
	for _, c := range existing {
		if c.Kind == line.kind && c.Period == line.period && c.AmountMinor == major(line.amount) {
			return true
		}
	}
	return false
}
