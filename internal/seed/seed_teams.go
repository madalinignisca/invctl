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
	"github.com/gabriel/invctl/internal/store"
)

// ---------- who looks after what ----------
//
// TEAMS, NEVER PEOPLE. Every contact below is a group address or a queue, which
// is the rule the schema documents and cannot enforce. A demo estate is exactly
// where somebody would otherwise reach for a plausible-looking human name, so
// this fixture is the first place the rule has to hold.
//
// The six here are the distinct values the free-text `owner_team` column carried
// before migration 00014 absorbed it, so the fixture reads the same afterwards
// and now has referential integrity behind it.

type teamSpec struct {
	code, name, contact, description string
}

func (b *builder) teams() {
	if !b.ok() {
		return
	}

	specs := []teamSpec{
		{"platform", "Platform & Core Services", "platform@example.com",
			"Virtualisation, identity, secrets and the shared substrate everything else runs on."},
		{"commerce", "Commerce", "commerce-dev@example.com",
			"The storefront and the order path — everything a customer touches."},
		{"dba", "Database Engineering", "dba@example.com",
			"PostgreSQL and the data on it: replication, backups, restores, retention."},
		{"network", "Network", "neteng@example.com",
			"Switching, routing, firewalls and transit. Owns the cable plant and the edge."},
		{"observability", "Observability", "#obs-oncall",
			"Metrics, logs and alerting for the whole estate. Owns the collectors, not the data sources."},
		{"developers", "Application Developers", "dev-guild@example.com",
			"Non-production environments and the tooling around them."},
	}

	for _, spec := range specs {
		if !b.ok() {
			return
		}
		t, err := domain.NewTeam(store.NewID(), domain.TeamSpec{
			Code: spec.code, Name: spec.name,
			Description: str(spec.description),
			ContactRef:  str(spec.contact),
			Lifecycle:   domain.LifecycleActive,
		}, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building team %s: %w", spec.code, err))
			return
		}
		if err := b.store.CreateTeam(b.ctx, Actor, t); err != nil {
			b.fail(fmt.Errorf("seeding team %s: %w", spec.code, err))
			return
		}
		b.refs.Teams[spec.code] = t.ID
	}
}

// team resolves a code to an id for the rest of the fixture. It returns nil for
// an unknown code rather than failing, because a thing nobody looks after is a
// legitimate state and the seed uses it deliberately -- see the unowned rows.
func (b *builder) team(code string) *string {
	id, ok := b.refs.Teams[code]
	if !ok {
		return nil
	}
	return &id
}
