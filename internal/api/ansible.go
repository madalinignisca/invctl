// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The Ansible dynamic inventory view (WP-A2, docs/api-design.md §4): the one
// composed view this work package publishes, assembled from the scoped asset
// list rather than served from its own store query.
//
// THIS HANDLER PAGES INTERNALLY UNTIL THE ESTATE IS EXHAUSTED. It does not
// fetch "with a large limit" -- apiMaxLimit (internal/store/api.go) clamps
// every page to 500 regardless of what is asked for, so a single oversized
// request against an estate bigger than that would come back 200 with a
// silently truncated inventory. A truncated dynamic inventory is the worst
// failure shape this consumer can have: every playbook run against it targets
// a subset of the estate, successfully, with nothing anywhere reporting an
// error. fetchAllAssets instead follows the keyset cursor exactly as an
// external client would, page after page, until a short page says there is
// nothing left.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/middleware"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// fetchAllAssetsWarnAfter is the number of round trips fetchAllAssets will
// make before it starts logging a warning on every subsequent one. This adds
// operational visibility into a runaway estate; it does not cap or truncate
// anything -- a cap here would recreate exactly the silent-truncation bug
// this file's paging exists to forbid.
const fetchAllAssetsWarnAfter = 50

// hostKinds are the kinds Ansible can actually connect to. A rack, a PDU or a
// patch panel is a real asset and not a thing with an SSH daemon; listing one
// produces an inventory that fails on first use.
var hostKinds = map[string]bool{
	domain.KindServer: true, domain.KindVM: true, domain.KindHypervisor: true,
}

// InventoryMeta is Ansible's reserved "_meta" key: a host's variables, keyed
// by host name.
type InventoryMeta struct {
	HostVars map[string]map[string]string `json:"hostvars"`
}

// InventoryGroup is one Ansible group. Hosts is always a non-nil, sorted
// slice so it marshals as "[]" rather than "null" on an empty group -- a
// consumer doing `for host in group.hosts` must never have to special-case
// null.
type InventoryGroup struct {
	Hosts []string `json:"hosts"`
}

// Inventory is the assembled Ansible dynamic inventory. It is NOT the wire
// shape: MarshalJSON flattens Groups to the top level beside "_meta", which
// is what Ansible expects and what no plain struct can express.
type Inventory struct {
	Meta   InventoryMeta
	Groups map[string]InventoryGroup
}

// MarshalJSON assembles a map holding "_meta" plus one key per group, so the
// top level can mix a fixed key with arbitrary group names. A group whose
// sanitised name is literally "_meta" is refused here rather than silently
// overwriting the real one -- buildAnsibleInventory's own collision check
// covers groups against each other, but this is the last line of defence
// against the one name a group must never take.
func (i Inventory) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(i.Groups)+1)
	out["_meta"] = i.Meta
	for name, g := range i.Groups {
		if name == "_meta" {
			return nil, fmt.Errorf("group name %q collides with the reserved _meta key", name)
		}
		out[name] = g
	}
	return json.Marshal(out)
}

// ansibleGroupName lowercases raw, collapses every run of non-alphanumerics
// to a single underscore, and prefixes it with dimension -- so a service
// named "prod" produces "svc_prod" and can never collide with the "prod"
// environment's "env_prod" group.
func ansibleGroupName(dimension, raw string) string {
	var b strings.Builder
	b.Grow(len(dimension) + 1 + len(raw))
	b.WriteString(dimension)
	b.WriteByte('_')

	lastUnderscore := true // suppress a leading underscore after the prefix
	for _, r := range strings.ToLower(raw) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.TrimRight(b.String(), "_")
}

// buildAnsibleInventory is a PURE function over rows, so it is testable
// without a store or a request. rows is assumed to already be narrowed to
// lifecycle = 'active' -- docs/api-design.md §4 -- by the caller's fetch
// filter (fetchAllAssets sets APIAssetFilter.Lifecycle accordingly); this
// function does not re-check it, because the input rows carry no
// lifecycle-vs-"active" boundary the caller has not already applied.
//
// A collision after sanitisation between two different source strings (two
// services, or a service and an environment sharing a sanitised name) is
// refused with an error naming both sources, never merged into one group --
// a merged group would silently widen the target set of every playbook that
// uses it.
func buildAnsibleInventory(rows []store.APIAssetRow) (Inventory, error) {
	inv := Inventory{
		Meta:   InventoryMeta{HostVars: map[string]map[string]string{}},
		Groups: map[string]InventoryGroup{},
	}

	// groupSource maps a sanitised group name to the "dimension:raw" string
	// that first produced it, so a second source producing the same
	// sanitised name -- with a DIFFERENT original -- is detectable as the
	// collision it is.
	groupSource := map[string]string{}
	addHostToGroup := func(dimension, raw, host string) error {
		name := ansibleGroupName(dimension, raw)
		source := dimension + ":" + raw
		if existing, ok := groupSource[name]; ok {
			if existing != source {
				return fmt.Errorf("ansible group name %q collides: %q and %q both sanitise to it",
					name, existing, source)
			}
		} else {
			groupSource[name] = source
		}
		g := inv.Groups[name]
		g.Hosts = append(g.Hosts, host)
		inv.Groups[name] = g
		return nil
	}

	for _, row := range rows {
		if !hostKinds[row.Kind] || len(row.Addresses) == 0 {
			continue
		}
		host := row.Name

		hv := map[string]string{
			"invctl_id":    row.ID,
			"invctl_kind":  row.Kind,
			"ansible_host": row.Addresses[0],
		}
		if row.Site != nil {
			hv["invctl_site"] = *row.Site
		}
		inv.Meta.HostVars[host] = hv

		for _, env := range row.Environments {
			if err := addHostToGroup("env", env, host); err != nil {
				return Inventory{}, err
			}
		}
		if err := addHostToGroup("kind", row.Kind, host); err != nil {
			return Inventory{}, err
		}
		for _, svc := range row.Services {
			if err := addHostToGroup("svc", svc, host); err != nil {
				return Inventory{}, err
			}
		}
	}

	// Sorted before marshalling, so two calls against an unchanged estate
	// produce byte-identical output.
	for name, g := range inv.Groups {
		sort.Strings(g.Hosts)
		inv.Groups[name] = g
	}

	return inv, nil
}

// fetchAllAssets pages through every asset visible to scope, following the
// keyset cursor exactly as an external client would, until a short page says
// there is nothing left. See the file doc comment for why "one call with a
// large limit" is refused.
//
// NO CAP ON THE NUMBER OF ROUND TRIPS, DELIBERATELY. The loop bound is the
// operator's own row count, not anything a client controls, and termination
// is structural: APIListAssets filters on a strict `a.id > ?` against a
// primary key, so every iteration either advances past the last id it saw or
// returns a short page and ends the loop -- it cannot repeat a page or spin
// forever. A cap here would recreate exactly the silent truncation this
// file's paging exists to forbid. Past fetchAllAssetsWarnAfter round trips
// this logs a warning on every further one, purely for operational
// visibility into an unusually large or runaway estate; the warning changes
// nothing about what gets fetched or returned.
func (a *API) fetchAllAssets(ctx context.Context, scope domain.EnvironmentScope) ([]store.APIAssetRow, error) {
	var all []store.APIAssetRow
	after := ""
	for trips := 1; ; trips++ {
		if trips > fetchAllAssetsWarnAfter {
			slog.Warn("api: paging the ansible view is taking an unusually large number of round trips",
				"round_trip", trips, "rows_so_far", len(all))
		}
		rows, err := a.Store.APIListAssets(ctx, store.APIAssetFilter{
			Scope:     scope,
			After:     after,
			Limit:     a.PageSize,
			Lifecycle: domain.LifecycleActive,
		})
		if err != nil {
			return nil, fmt.Errorf("paging assets for the ansible view: %w", err)
		}
		all = append(all, rows...)
		cursor := nextCursor(rows, a.PageSize, func(r store.APIAssetRow) string { return r.ID })
		if cursor == nil {
			return all, nil
		}
		after = *cursor
	}
}

// Ansible serves GET /api/v1/ansible: the composed dynamic-inventory view
// over every active, addressable, in-scope asset. Unpaginated on the wire --
// docs/api-design.md §4 -- because the consumer is Ansible itself, which
// expects one document, not a feed to page through; the pagination happens
// internally against the store instead, in fetchAllAssets.
func (a *API) Ansible(w http.ResponseWriter, r *http.Request) {
	reader, ok := middleware.ReaderFrom(r.Context())
	if !ok {
		writeError(w, errNoReaderInContext)
		return
	}
	if err := checkKnownParams(r.URL.Query()); err != nil {
		writeError(w, err)
		return
	}
	rows, err := a.fetchAllAssets(r.Context(), reader.Environments)
	if err != nil {
		writeError(w, err)
		return
	}
	inv, err := buildAnsibleInventory(rows)
	if err != nil {
		writeError(w, fmt.Errorf("building the ansible view: %w", err))
		return
	}
	render.JSON(w, http.StatusOK, inv)
}
