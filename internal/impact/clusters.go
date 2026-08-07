// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package impact

import (
	"fmt"
	"sort"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Cluster HA, and the first thing here that changes what the engine CONCLUDES.
//
// Everything shipped in groups D and E reports beside the simulation. This one
// alters it: a guest whose host failed inside a cluster that can absorb the
// loss is not gone, it restarted somewhere else, and reporting it as an outage
// is wrong for every estate that runs Proxmox, vSphere or Hyper-V properly.
//
// WHAT IT DOES NOT CLAIM. The guest was not serving during the restart. The
// engine answers the steady-state question -- what is broken once the dust
// settles -- so a relocated guest is correctly not in the down set, and the
// restart is a real event that would be dishonest to leave unsaid. Hence the
// finding: a report that silently turns an outage into nothing teaches people
// the tool is optimistic, which is worse than the pessimism it replaced.
//
// THE PLACEMENT IS STILL A FACT. A guest's parent stays the host it is actually
// on; the cluster says that placement is mobile. Nothing here reparents
// anything, and the rack view, the power chain and the containment walk are
// untouched.

// Cluster is a set of hosts that can carry each other's guests.
type Cluster struct {
	ID       string
	Name     string
	HAPolicy string
	MinHosts *int
	// MemberAssetIDs are the hosts. GuestsByHost maps each member to the guest
	// assets running on it, resolved from containment at load time.
	MemberAssetIDs []string
	GuestsByHost   map[string][]string
}

// RelocationFinding is what a cluster did, or failed to do, for its guests.
type RelocationFinding struct {
	ClusterName string
	// Outcome is domain.RelocateOK or domain.RelocateNoCapacity. A cluster with
	// no HA configured produces no finding at all -- it behaved exactly as an
	// estate without this table, and saying so on every simulation is noise.
	Outcome domain.Relocation
	Guests  int
	// Surviving and Needed are the arithmetic, so the sentence can be checked.
	Surviving int
	Needed    int
	Detail    string
}

// Relocated reports whether the guests came back.
func (f RelocationFinding) Relocated() bool { return f.Outcome == domain.RelocateOK }

// applyClusterHA removes relocated guests from the down set and says what it did.
//
// It runs BEFORE the fixed point, on the asset set, so everything downstream --
// instances, capacity, propagation, shutdown order -- sees an estate in which
// those guests are up. That is the only honest place for it: patching the
// result afterwards would leave the dependency walk having reasoned about an
// outage that did not happen.
//
// Returns the guests to revive, so the caller can rebuild the instance set
// rather than this function reaching into it.
func applyClusterHA(clusters []Cluster, down map[string]bool) (revive map[string]bool, findings []RelocationFinding) {
	revive = map[string]bool{}
	for _, c := range clusters {
		if len(c.MemberAssetIDs) == 0 {
			continue
		}
		surviving, failed := 0, []string{}
		for _, host := range c.MemberAssetIDs {
			if down[host] {
				failed = append(failed, host)
				continue
			}
			surviving++
		}
		if len(failed) == 0 {
			continue // nothing to relocate
		}

		var guests []string
		for _, host := range failed {
			guests = append(guests, c.GuestsByHost[host]...)
		}
		if len(guests) == 0 {
			continue // members failed and carried nothing
		}

		outcome := domain.CanRelocate(c.HAPolicy, surviving, c.MinHosts)
		if outcome == domain.RelocateNotConfigured {
			// Identical to an estate without this table, so it is not a finding.
			continue
		}
		needed := 1
		if c.MinHosts != nil && *c.MinHosts > 1 {
			needed = *c.MinHosts
		}
		f := RelocationFinding{
			ClusterName: c.Name, Outcome: outcome, Guests: len(guests),
			Surviving: surviving, Needed: needed,
		}
		if outcome == domain.RelocateOK {
			for _, g := range guests {
				revive[g] = true
			}
			f.Detail = fmt.Sprintf("%d guest(s) restarted on the %d surviving host(s); "+
				"they were not serving during the restart", len(guests), surviving)
		} else {
			f.Detail = fmt.Sprintf("%d guest(s) have nowhere to go: %d host(s) left and "+
				"%d needed. HA is configured and cannot help here", len(guests), surviving, needed)
		}
		findings = append(findings, f)
	}

	// Failures before successes: a cluster that could not absorb the loss is
	// the finding somebody needs, and it must not sit under three that did.
	sort.SliceStable(findings, func(i, j int) bool {
		if a, b := findings[i].Relocated(), findings[j].Relocated(); a != b {
			return !a
		}
		return findings[i].ClusterName < findings[j].ClusterName
	})
	return revive, findings
}
