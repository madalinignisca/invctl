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

// ---------- reachability topology (docs/reachability-design.md, M5) ----------
//
// The forwarder graph over the physical estate: which chassis form one
// forwarding unit, which unit uplinks to which, where each host actually lands,
// and where each reachability scope enters the estate.
//
// Before this existed the seed declared zero topology rows, so the impact page
// answered "no network topology declared" for every outage and losing the edge
// firewall reported nothing at all -- the engine was correct and had nothing to
// answer with. Every value below is chosen to make one of the design's numbered
// scenarios reproducible from the demo rather than from a test fixture:
//
//   - sw-core is active_active with min_healthy = 1, so losing one core chassis
//     leaves the group OK. The host-level answer then comes entirely from
//     net_attachment_member: hv-01 and hv-02 are pinned to both chassis and
//     survive, hv-03 is pinned to sw-core-2 alone and does not.
//   - fw-edge is active_passive with failover_mode = MANUAL. That single value
//     is what makes losing fw-edge-1 come out DEGRADED rather than OK -- a
//     passive firewall that needs a human to promote it is the operator's
//     stated real-world behaviour. Concretely, and asserted by scenarios 1 and
//     2: manual reports haproxy-edge and partner-gateway degraded with
//     Cause=exposure plus two Unreachable rows blocked by fw-edge, because the
//     group sits in the optimistic union-find pass and not the pessimistic
//     one; flipping it to auto puts the group in both passes and the same run
//     reports no service impact at all and one redundancy note.
//   - sw-oob is referenced by nothing on the data plane, so losing it isolates
//     three hypervisors on the management plane and moves no service's status.
//
// The 13 VMs and k8s nodes get no rows at all. They inherit their hypervisor's
// attachment through asset_closure, which is the whole point of resolving
// attachments by nearest ancestor; adding a row per VM would make the fixture
// stop demonstrating it.

// topology declares the forwarder groups, their members, the uplink between
// them, every host attachment and every anchor.
//
// Order is not arbitrary: CreateNetAttachment validates each pinned chassis as
// an active member of the target group inside its own transaction, so members
// must exist before any attachment names them.
func (b *builder) topology() {
	if !b.ok() {
		return
	}
	b.netGroups()
	b.netGroupMembers()
	b.netUplinks()
	b.netAttachments()
	b.netAnchors()
}

func (b *builder) netGroups() {
	minHealthy := 1
	manual := domain.FailoverManual

	specs := []domain.NetGroupSpec{
		// 1-of-2 survives, so losing a core chassis is not a core outage. Set
		// min_healthy to 2 instead and half the fabric reads as degraded -- a
		// data decision, not a code branch.
		{
			Code: "sw-core", Name: "Core switch pair", Kind: domain.NetGroupMCLAG,
			Role: domain.NetRoleCore, Availability: domain.AvailActiveActive, MinHealthy: &minHealthy,
		},
		// MANUAL, deliberately. This is the value the whole fw-edge-1 scenario
		// turns on.
		{
			Code: "fw-edge", Name: "Edge firewall pair", Kind: domain.NetGroupHAPair,
			Role: domain.NetRoleEdge, Availability: domain.AvailActivePassive, FailoverMode: &manual,
		},
		// A lone switch is a group of one.
		{
			Code: "sw-oob", Name: "Out-of-band management switch", Kind: domain.NetGroupStandalone,
			Role: domain.NetRoleAccess, Availability: domain.AvailStandalone,
		},
	}
	for _, spec := range specs {
		if !b.ok() {
			return
		}
		g, err := domain.NewNetGroup(store.NewID(), spec, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building net group %s: %w", spec.Code, err))
			return
		}
		if err := b.store.CreateNetGroup(b.ctx, Actor, g); err != nil {
			b.fail(fmt.Errorf("seeding net group %s: %w", spec.Code, err))
			return
		}
		b.refs.NetGroups[spec.Code] = g.ID
	}
}

func (b *builder) netGroupMembers() {
	// primary/standby on fw-edge is read by the same AvailabilityPolicy a
	// service uses: the evaluator needs to know which chassis is the one that
	// has to be promoted before the pair serves again.
	members := []struct{ group, asset, role string }{
		{"sw-core", "sw-core-1", "member"},
		{"sw-core", "sw-core-2", "member"},
		{"fw-edge", "fw-edge-1", domain.RolePrimary},
		{"fw-edge", "fw-edge-2", domain.RoleStandby},
		{"sw-oob", "sw-oob-1", "member"},
	}
	for _, m := range members {
		if !b.ok() {
			return
		}
		groupID, ok := b.refs.NetGroups[m.group]
		if !ok {
			b.fail(fmt.Errorf("seeding net group member %s: unknown group %s", m.asset, m.group))
			return
		}
		assetID, ok := b.refs.Assets[m.asset]
		if !ok {
			b.fail(fmt.Errorf("seeding net group member %s: unknown asset %s", m.asset, m.asset))
			return
		}
		member, err := domain.NewNetGroupMember(groupID, assetID, m.role, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building net group member %s/%s: %w", m.group, m.asset, err))
			return
		}
		if err := b.store.AddNetGroupMember(b.ctx, Actor, member); err != nil {
			b.fail(fmt.Errorf("seeding net group member %s/%s: %w", m.group, m.asset, err))
			return
		}
	}
}

func (b *builder) netUplinks() {
	// One row, not two, even though two physical cables back it
	// (sw-core-1->fw-edge-1 and sw-core-2->fw-edge-2). Uplinks are
	// group-to-group by design; member-level uplink diversity is the known
	// limit the design records rather than a gap in the fixture.
	//
	// sw-oob has no uplink and no anchor on purpose. That is a real thing an
	// out-of-band switch is, and it makes the /network page's
	// "groups without an uplink or anchor" count non-zero for an honest reason.
	uplinks := []struct{ from, to, plane string }{
		{"sw-core", "fw-edge", domain.PlaneData},
	}
	for _, u := range uplinks {
		if !b.ok() {
			return
		}
		fromID, ok := b.refs.NetGroups[u.from]
		if !ok {
			b.fail(fmt.Errorf("seeding uplink %s->%s: unknown group %s", u.from, u.to, u.from))
			return
		}
		toID, ok := b.refs.NetGroups[u.to]
		if !ok {
			b.fail(fmt.Errorf("seeding uplink %s->%s: unknown group %s", u.from, u.to, u.to))
			return
		}
		link, err := domain.NewNetUplink(store.NewID(), fromID, toID, u.plane, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building uplink %s->%s: %w", u.from, u.to, err))
			return
		}
		if err := b.store.CreateNetUplink(b.ctx, Actor, link); err != nil {
			b.fail(fmt.Errorf("seeding uplink %s->%s: %w", u.from, u.to, err))
			return
		}
	}
}

func (b *builder) netAttachments() {
	// pin names one chassis an attachment actually lands on, and the host-side
	// port it lands from. The interface is provenance -- nothing reads it yet,
	// and it is what lets a later reconciler check the pin against the cable
	// plant, which is the stated mitigation for this table rotting.
	type pin struct{ chassis, iface string }

	specs := []struct {
		host, group, plane string
		pins               []pin
	}{
		// Dual-homed: one cable to each core chassis, so either loss is
		// survivable at the host level as well as at the group level.
		{"hv-01", "sw-core", domain.PlaneData, []pin{
			{"sw-core-1", "hv-01/eno2"}, {"sw-core-2", "hv-01/eno3"},
		}},
		{"hv-02", "sw-core", domain.PlaneData, []pin{
			{"sw-core-1", "hv-02/eno2"}, {"sw-core-2", "hv-02/eno3"},
		}},
		// Single-homed, and this is the row the whole model exists for: the
		// group survives losing sw-core-2 at 1-of-2, and hv-03 does not.
		{"hv-03", "sw-core", domain.PlaneData, []pin{
			{"sw-core-2", "hv-03/eno2"},
		}},

		// No pins on the management attachments. sw-oob is a group of one, so
		// "attached to the group as a whole" says exactly as much as a pin
		// would, without adding three rows that can rot out of step with the
		// cable plant.
		{"hv-01", "sw-oob", domain.PlaneMgmt, nil},
		{"hv-02", "sw-oob", domain.PlaneMgmt, nil},
		{"hv-03", "sw-oob", domain.PlaneMgmt, nil},

		// srv-backup-proxy-1 gets a management attachment and NO data-plane
		// one, which is the whole point of it: the console cable is patched
		// and the data uplink is not. The impact engine therefore makes no
		// data-plane claim about it at all (it is Unmodelled), and the path
		// diagram draws its log shipper connected to nothing -- two views,
		// both honest, agreeing that the box has no production network.
		{"srv-backup-proxy-1", "sw-oob", domain.PlaneMgmt, nil},
	}

	for _, spec := range specs {
		if !b.ok() {
			return
		}
		hostID, ok := b.refs.Assets[spec.host]
		if !ok {
			b.fail(fmt.Errorf("seeding attachment of %s: unknown asset %s", spec.host, spec.host))
			return
		}
		groupID, ok := b.refs.NetGroups[spec.group]
		if !ok {
			b.fail(fmt.Errorf("seeding attachment of %s: unknown group %s", spec.host, spec.group))
			return
		}
		att, err := domain.NewNetAttachment(store.NewID(), hostID, groupID, spec.plane, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building attachment %s->%s: %w", spec.host, spec.group, err))
			return
		}

		members := make([]domain.NetAttachmentMember, 0, len(spec.pins))
		for _, p := range spec.pins {
			chassisID, ok := b.refs.Assets[p.chassis]
			if !ok {
				b.fail(fmt.Errorf("seeding attachment of %s: unknown chassis %s", spec.host, p.chassis))
				return
			}
			ifaceID, ok := b.interfaceIDs[p.iface]
			if !ok {
				b.fail(fmt.Errorf("seeding attachment of %s: unknown interface %s", spec.host, p.iface))
				return
			}
			m, err := domain.NewNetAttachmentMember(att.ID, chassisID, &ifaceID)
			if err != nil {
				b.fail(fmt.Errorf("building attachment member %s/%s: %w", spec.host, p.chassis, err))
				return
			}
			members = append(members, *m)
		}

		if err := b.store.CreateNetAttachment(b.ctx, Actor, att, members); err != nil {
			b.fail(fmt.Errorf("seeding attachment %s->%s: %w", spec.host, spec.group, err))
			return
		}
	}
}

func (b *builder) netAnchors() {
	// An anchor is where a reachability scope enters the estate, and its scope
	// is 1:1 with endpoint.exposure -- no parallel taxonomy. A misplaced anchor
	// is the single highest-leverage wrong row in this model, which is why
	// there are four of them and not forty.
	anchors := []struct{ code, name, scope, group, env string }{
		{"internet", "The public internet", "external", "fw-edge", ""},
		{"partner-transit", "Partner transit zone", "cross_env", "fw-edge", ""},
		{"prod-net", "Production network", "environment", "sw-core", "prod"},
		{"dev-net", "Development network", "environment", "sw-core", "dev"},
	}
	for _, a := range anchors {
		if !b.ok() {
			return
		}
		groupID, ok := b.refs.NetGroups[a.group]
		if !ok {
			b.fail(fmt.Errorf("seeding anchor %s: unknown group %s", a.code, a.group))
			return
		}
		anchor, err := domain.NewNetAnchor(store.NewID(), a.code, a.name, a.scope, groupID, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building anchor %s: %w", a.code, err))
			return
		}
		// NewNetAnchor does not take an environment: an environment-scoped
		// anchor names which environment it serves, and CreateNetAnchor
		// re-validates, so setting it here is checked rather than trusted.
		if a.env != "" {
			envID, ok := b.refs.Environments[a.env]
			if !ok {
				b.fail(fmt.Errorf("seeding anchor %s: unknown environment %s", a.code, a.env))
				return
			}
			anchor.EnvironmentID = &envID
		}
		if err := b.store.CreateNetAnchor(b.ctx, Actor, anchor); err != nil {
			b.fail(fmt.Errorf("seeding anchor %s: %w", a.code, err))
			return
		}
	}
}
