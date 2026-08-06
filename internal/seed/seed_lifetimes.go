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

	"github.com/gabriel/invctl/internal/domain"
)

// ---------- how long things last ----------
//
// End-of-support dates, in one table so the demo's story is readable in one
// place rather than scattered through the physical and service inventories.
//
// They are set by UPDATE rather than at creation, deliberately. An EOL date is
// declared state and every change to it takes a change_log row; doing it here
// means the seeded change log actually contains one, and a reader can see that
// somebody dating a switch is audited exactly like somebody renaming it. It
// costs one extra write per row in a fixture that already writes thousands.
//
// Dates are RELATIVE to the seeding clock, so the report says something true
// whenever the demo is loaded. A fixture with hard-coded 2027 dates reads as
// urgent this year and as ancient history in three.

type lifetime struct {
	name string // asset name or service code
	days int    // relative to the seed clock; negative is already past
	why  string
}

func (b *builder) lifetimes() {
	if !b.ok() {
		return
	}

	assets := []lifetime{
		// The pair that carries the whole estate, and support lapsed over a
		// year ago. Nothing is placed ON a switch, so the report shows it with
		// nothing riding on it -- which is honest and slightly misleading, and
		// is why the network layer is a different page. It is still the row a
		// CTO stops at.
		{"sw-core-1", -430, "core switch, support lapsed"},
		{"sw-core-2", -430, "core switch, support lapsed"},

		// The finding the report exists for: a hypervisor two months out,
		// carrying a tier-1 database and most of the storefront. Everything
		// needed to act on it -- what rides on it, which project owns it -- is
		// on the same row.
		{"hv-01", 58, "hardware support ends"},

		// The same hardware, bought later. Two boxes of one generation
		// expiring eight months apart is the normal shape and the reason a
		// horizon control exists.
		{"hv-02", 240, "hardware support ends"},

		// hv-03 is deliberately left undated. The report's closing callout
		// counts it, which is the whole point of that callout: an estate where
		// nothing appears to expire is usually one where nobody wrote the dates
		// down.

		{"fw-edge-1", -95, "firewall past vendor support"},

		// Owned by observability, and the box the stranded log shipper sits on.
		{"srv-backup-proxy-1", 21, "out of warranty next month"},
	}

	services := []lifetime{
		// A licence, not hardware -- the other half of what an EOL date means.
		{"vault", 74, "licence term ends"},
		// Already lapsed, on the agent that runs on somebody else's VM. It is
		// the row that lands on two projects at once.
		{"backup-agent", -40, "licence lapsed"},
	}

	for _, l := range assets {
		if !b.ok() {
			return
		}
		id, ok := b.refs.Assets[l.name]
		if !ok {
			b.fail(fmt.Errorf("dating asset %s: unknown asset", l.name))
			return
		}
		row, err := b.store.GetAsset(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("dating asset %s: %w", l.name, err))
			return
		}
		updated := row.Asset
		date := domain.FormatDate(b.now.AddDate(0, 0, l.days))
		updated.EOLDate = &date

		// The environments have to be handed back. UpdateAsset replaces
		// asset_environment wholesale -- that is the documented contract for a
		// set table -- so passing nothing here would silently strip every
		// membership this asset has, and the only symptom would be an estate
		// that quietly emptied its environments.
		envIDs := make([]string, len(row.Environments))
		for i, e := range row.Environments {
			envIDs[i] = e.ID
		}
		if err := b.store.UpdateAsset(b.ctx, Actor, &updated, envIDs); err != nil {
			b.fail(fmt.Errorf("dating asset %s: %w", l.name, err))
			return
		}
	}

	for _, l := range services {
		if !b.ok() {
			return
		}
		id, ok := b.refs.Services[l.name]
		if !ok {
			b.fail(fmt.Errorf("dating service %s: unknown service", l.name))
			return
		}
		row, err := b.store.GetService(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("dating service %s: %w", l.name, err))
			return
		}
		updated := row.Service
		date := domain.FormatDate(b.now.AddDate(0, 0, l.days))
		updated.EOLDate = &date
		if err := b.store.UpdateService(b.ctx, Actor, &updated); err != nil {
			b.fail(fmt.Errorf("dating service %s: %w", l.name, err))
			return
		}
	}
}
