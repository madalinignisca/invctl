// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

// This file implements the Ansible dynamic inventory view (WP-A2,
// docs/api-design.md §4): the one composed view this work package publishes,
// assembled from the scoped asset list rather than served from its own store
// query.
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
//
// TWO ASSETS SHARING A HOST NAME ARE REFUSED THE SAME WAY, one level down and
// on the dimension that decides which machine gets connected to. asset.name is
// unique only among LIVE SIBLINGS (migration 00021), so `vm-app-1` under
// hv-prod-1 and `vm-app-1` under hv-dev-1 is legal and common. Ansible keys
// hostvars by name, so merging them means last writer wins: a playbook
// targeting env_dev resolves `vm-app-1` to the PRODUCTION box's ansible_host
// and connects to it successfully, with nothing reporting an error. That is
// the group-name failure with a worse blast radius, so it gets the same
// answer -- an error naming BOTH asset ids, a 500 to the client with the
// generic body, and both ids in the server log.
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

	// hostSource maps a published host name to the id of the asset that
	// claimed it, so a second asset claiming the same name is detectable
	// before anything is overwritten. Checked AFTER the host filter above: two
	// assets sharing a name matter here only if both would actually appear in
	// this document, and a switch sharing a VM's name is not a conflict
	// Ansible can ever see.
	hostSource := map[string]string{}

	for _, row := range rows {
		if !hostKinds[row.Kind] || len(row.Addresses) == 0 {
			continue
		}
		host := row.Name
		if existing, ok := hostSource[host]; ok && existing != row.ID {
			return Inventory{}, fmt.Errorf(
				"ansible host name %q is claimed by two assets, %s and %s: "+
					"a name is unique only among live siblings, and merging them would "+
					"connect a playbook to whichever asset was written last",
				host, existing, row.ID)
		}
		hostSource[host] = row.ID

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

// pageSize is the clamped round-trip size fetchAllAssets actually uses. See
// its call site for what each end of the clamp otherwise truncates.
func (a *API) pageSize() int {
	if a.PageSize <= 0 || a.PageSize > MaxLimit {
		return MaxLimit
	}
	return a.PageSize
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
	// CLAMPED HERE, not trusted from the field. API.PageSize is exported and
	// has no floor or ceiling, and both ends of its range truncate this view
	// silently -- the exact failure this file's banner comment forbids:
	//
	//   - The ZERO value. `&api.API{Store: st}` built without New() asks the
	//     store for 0, the store's own apiLimit turns that into 100 rows, and
	//     nextCursor(rows, 0) returns nil on the first call, so the loop ends
	//     after one page with a 200 and a truncated inventory.
	//   - Anything ABOVE MaxLimit. The store clamps to 500 and returns 500,
	//     nextCursor compares 500 against the larger asked-for size, calls it
	//     a short page, and stops. Same silent truncation.
	//
	// The same number must reach the store and nextCursor for the short-page
	// test to mean anything, which is why it is computed once here.
	size := a.pageSize()
	after := ""
	for trips := 1; ; trips++ {
		if trips > fetchAllAssetsWarnAfter {
			slog.Warn("api: paging the ansible view is taking an unusually large number of round trips",
				"round_trip", trips, "rows_so_far", len(all))
		}
		rows, err := a.Store.APIListAssets(ctx, store.APIAssetFilter{
			Scope:     scope,
			After:     after,
			Limit:     size,
			Lifecycle: domain.LifecycleActive,
		})
		if err != nil {
			return nil, fmt.Errorf("paging assets for the ansible view: %w", err)
		}
		all = append(all, rows...)
		cursor := nextCursor(rows, size, func(r store.APIAssetRow) string { return r.ID })
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
	reader, _, err := readerAndQuery(r)
	if err != nil {
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
