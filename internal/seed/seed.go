// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package seed builds the demo estate.
//
// This is deliberately not a toy. Every case HANDOVER §10 asks for is present,
// because the impact engine's tests assert against this fixture: if the
// fixture does not contain a case, the engine is not tested for it.
//
// The estate is a small segmented site with a production zone, an
// out-of-scope development zone, and a transit zone brokering between them.
package seed

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// Actor attributes seeded rows in the change log, so demo data is instantly
// distinguishable from anything an operator typed.
var Actor = domain.Actor{ID: "seed", Name: "seed", Kind: "system"}

// Refs holds the ids of the fixture rows the tests need to name. Building it
// as a return value keeps the tests from hard-coding ids or looking rows up by
// display name.
type Refs struct {
	Environments map[string]string // code -> id
	Assets       map[string]string // name -> id
	Services     map[string]string // code -> id
	Teams        map[string]string // code -> id
	Projects     map[string]string // code -> id
	Endpoints    map[string]string // "service/endpoint" -> id
	Routes       map[string]string // match value -> id
	NetGroups    map[string]string // code -> id
	// VLANs by name, so the phases that put ports in them and terminate
	// overlays into them need no second lookup.
	VLANs map[string]string // name -> id
	// The hardware catalogue and the power chain.
	Manufacturers map[string]string // code -> id
	DeviceTypes   map[string]string // model -> id
	PowerSources  map[string]string // name -> id
	PowerPanels   map[string]string // name -> id
	PowerFeeds    map[string]string // "panel/feed" -> id
}

// builder threads the store, context and error state through the many small
// inserts below, so the fixture reads as a description of the estate rather
// than as several hundred lines of error checking.
type builder struct {
	ctx   context.Context
	store *store.SQLStore
	now   time.Time
	refs  *Refs
	err   error
	// skipped are phases that found nothing of theirs in the estate. See skip.
	skipped []string

	// topUp makes the creators skip what a hydrated database already holds, so
	// a phase can be re-run against a live estate instead of only an empty one.
	// See topup.go for why that is a mode rather than the default.
	topUp bool

	interfaceIDs map[string]string // "asset/interface" -> id
	identityIDs  map[string]string // name -> id
	poolIDs      map[string]string // name -> id
}

// ObserveDemo makes Load stage a set of demo observations after the inventory,
// through the same write path a monitoring credential uses. Set from
// INV_SEED_OBSERVATIONS by the server; off everywhere else, including every
// test, so the fixture the suite asserts against is unchanged.
var ObserveDemo bool

// Load populates an empty database with the demo estate.
func Load(ctx context.Context, s *store.SQLStore) (*Refs, error) {
	b := &builder{
		ctx:   ctx,
		store: s,
		now:   s.Now(),

		interfaceIDs: map[string]string{},
		identityIDs:  map[string]string{},
		poolIDs:      map[string]string{},

		refs: &Refs{
			Environments: map[string]string{},
			Assets:       map[string]string{},
			Services:     map[string]string{},
			Teams:        map[string]string{},
			Projects:     map[string]string{},
			Endpoints:    map[string]string{},
			Routes:       map[string]string{},
			NetGroups:    map[string]string{},
			VLANs:        map[string]string{},

			Manufacturers: map[string]string{},
			DeviceTypes:   map[string]string{},
			PowerSources:  map[string]string{},
			PowerPanels:   map[string]string{},
			PowerFeeds:    map[string]string{},
		},
	}

	b.environments()
	// Teams first: assets, services, projects and identities all point at one,
	// and a foreign key does not care that this is a fixture.
	b.teams()
	// The catalogue before the estate: an asset names its model at creation, so
	// the models have to exist for the link to be made rather than backfilled.
	b.catalogue()
	b.physical()
	b.networking()
	// The bridges hang off the hypervisors physical() built and land on the
	// bonds networking() declared, so it follows both. It sits ahead of
	// topology() only for readability -- a bridge takes no topology row and
	// inherits its host's attachment through asset_closure, which is the same
	// thing every guest already does.
	b.virtual()
	// Power after the estate: a board names its site and an input names an
	// asset. It reads both and nothing reads it, so it can sit here.
	b.power()
	// topology reads asset ids and interface ids, so it has to follow both of
	// the phases above. It sits here rather than at the end because the groups
	// it declares are a reading of the cable plant networking() just laid.
	b.topology()
	b.identities()
	b.services()
	b.endpoints()
	b.routing()
	b.dependencies()
	// Last of the declared phases: a project links to assets and services, so
	// it needs both to exist. It reads them and writes nothing they depend on,
	// which is why it can sit at the end rather than being threaded through.
	b.projects()
	// Dates last: it reads and updates assets and services, so both must exist,
	// and it must not run before projects() or the report would have nobody to
	// attribute an expiring box to.
	b.lifetimes()
	// The edge types WP-I1 and WP-E2 taught the engine about: a cluster, VLAN
	// membership, first-hop redundancy, an overlay and a circuit. After the
	// estate and the topology, because every one of them names an interface or
	// a host that must already exist.
	b.engineEdges()
	// Prices last of all: they attach to assets, services AND projects, so
	// every one of those has to exist, and nothing else reads them.
	b.costs()
	// Certificates last: they deploy to assets and services and are renewed by
	// teams, so all three must exist.
	b.certificates()

	// Last, and only when asked. Observations are telemetry rather than
	// inventory, so a deployment gets the honest empty state unless somebody is
	// presenting and wants the panels to have something to say. Staged through
	// the real recorder, never by writing asset_health directly.
	// The small-company layer, when a deployment asks for it. It sits after
	// every declared phase because it references teams, services, device types
	// and the base estate, and before the observations so telemetry can land on
	// anything it adds.
	if CompanyEstate {
		b.company()
	}

	if ObserveDemo {
		b.observations()
	}

	if b.err != nil {
		return nil, b.err
	}
	// Phases that found nothing of theirs. Logged rather than returned: they are
	// not failures, and a caller that had to handle them would mostly ignore
	// them. What matters is that they stop being invisible.
	for _, note := range b.skipped {
		slog.Warn("seed phase skipped", "reason", note)
	}
	return b.refs, nil
}

// fail records the first error and makes every later step a no-op.
func (b *builder) fail(err error) {
	if b.err == nil && err != nil {
		b.err = err
	}
}

func (b *builder) ok() bool { return b.err == nil }

// skip records a phase that found nothing to do, so a deployment can tell
// "there was nothing to add" from "this fixture no longer fits your estate".
//
// WRITTEN BECAUSE A PHASE THAT SILENTLY DID NOTHING WAS INDISTINGUISHABLE FROM
// ONE THAT SUCCEEDED. Every phase here is deliberately written to skip an estate
// that does not contain what it names -- right for a partial deployment, and
// the exact behaviour that let the capacity phase quietly declare nothing on a
// long-lived demo whose clusters had been renamed years earlier. Nothing failed,
// nothing was logged, and the estate simply stayed undeclared.
//
// Not an error: skipping is usually correct. It is a line in the log, which is
// all it takes to turn "no output" into a fact somebody can act on.
func (b *builder) skip(format string, args ...any) {
	b.skipped = append(b.skipped, fmt.Sprintf(format, args...))
}

// ---------- environments ----------

func (b *builder) environments() {
	// Production is in scope for audit; development explicitly is not; transit
	// exists to broker traffic between them and is in scope because that is
	// where the segmentation is actually enforced.
	envs := []struct {
		code, name, role string
		inScope          bool
		criticality      int
	}{
		{"prod", "Production", domain.EnvRoleProduction, true, 1},
		{"transit", "Transit / DMZ", domain.EnvRoleTransit, true, 2},
		{"dev", "Development", domain.EnvRoleDev, false, 4},
	}
	for _, e := range envs {
		if !b.ok() {
			return
		}
		env, err := domain.NewEnvironment(store.NewID(), e.code, e.name, e.role, e.inScope, e.criticality, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building environment %s: %w", e.code, err))
			return
		}
		if err := b.store.CreateEnvironment(b.ctx, Actor, env); err != nil {
			b.fail(fmt.Errorf("seeding environment %s: %w", e.code, err))
			return
		}
		b.refs.Environments[e.code] = env.ID
	}
}

func (b *builder) env(code string) string { return b.refs.Environments[code] }

// ---------- physical estate ----------

// asset creates one asset and records its id under its name.
func (b *builder) asset(kind, name, parent string, envs []string, fields func(*domain.Asset)) {
	if !b.ok() {
		return
	}
	// Topping up an estate that already holds this name: leave the live row
	// alone. Only in top-up mode -- on a fresh seed a repeated name is a bug in
	// the fixture, and silently skipping it would hide that.
	if b.topUp {
		if _, exists := b.refs.Assets[name]; exists {
			return
		}
	}
	var parentID *string
	if parent != "" {
		id, ok := b.refs.Assets[parent]
		if !ok {
			b.fail(fmt.Errorf("seeding asset %s: unknown parent %s", name, parent))
			return
		}
		parentID = &id
	}
	a, err := domain.NewAsset(store.NewID(), kind, name, parentID, b.now)
	if err != nil {
		b.fail(fmt.Errorf("building asset %s: %w", name, err))
		return
	}
	if fields != nil {
		fields(a)
	}
	envIDs := make([]string, 0, len(envs))
	for _, code := range envs {
		envIDs = append(envIDs, b.env(code))
	}
	if err := b.store.CreateAsset(b.ctx, Actor, a, envIDs); err != nil {
		b.fail(fmt.Errorf("seeding asset %s: %w", name, err))
		return
	}
	b.refs.Assets[name] = a.ID
}

func str(s string) *string { return &s }
func num(n int) *int       { return &n }

func (b *builder) physical() {
	b.asset(domain.KindSite, "dc-oslo", "", []string{"prod"}, nil)

	// Measured racks, so the elevation draws a real height rather than the
	// display default -- and so the fixture demonstrates the difference.
	b.asset(domain.KindRack, "rack-a1", "dc-oslo", []string{"prod"}, func(a *domain.Asset) {
		a.UHeight = num(42)
	})
	b.asset(domain.KindRack, "rack-b1", "dc-oslo", []string{"prod"}, nil) // height not recorded

	// The PDU is a containment parent for nothing, but losing it takes the
	// rack with it -- modelled here as a sibling so the demo can show that
	// "PDU fails" needs the operator to select the rack, not the PDU alone.
	b.asset(domain.KindPDU, "pdu-a1", "rack-a1", []string{"prod"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("APC"), str("AP8853")
		a.Serial = str("5A2134X09881")
		a.DeviceTypeID = b.deviceType("AP8853")
		a.RackPosition, a.RackFace = num(1), str(domain.FaceRear)
	})

	// The shared switch: it carries both production and development VLANs, so
	// it belongs to two non-transit environments and shows up in the
	// span-detection report. This is the case §10 asks for.
	b.asset(domain.KindSwitch, "sw-core-1", "rack-a1", []string{"prod", "dev"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Arista"), str("DCS-7050SX3-48YC8")
		a.Serial, a.AssetTag = str("JPE19140XYZ"), str("NET-0001")
		a.TeamID = b.team("network")
		// No date of its own: it INHERITS the model's, and every view says so.
		// The catalogue's whole argument, sitting in the fixture.
		a.DeviceTypeID = b.deviceType("DCS-7050SX3-48YC8")
		a.RackPosition, a.RackFace = num(40), str(domain.FaceFront)
	})

	// The MC-LAG peer, in the other rack. It carries the same VLANs as
	// sw-core-1 -- that is what an MC-LAG pair is -- so it belongs to the same
	// two non-transit environments and is a second, correct entry in the span
	// report. Declaring it production-only would make the fixture lie to make
	// a report shorter.
	b.asset(domain.KindSwitch, "sw-core-2", "rack-b1", []string{"prod", "dev"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Arista"), str("DCS-7050SX3-48YC8")
		a.Serial, a.AssetTag = str("JPE19140ABC"), str("NET-0002")
		a.TeamID = b.team("network")
		a.DeviceTypeID = b.deviceType("DCS-7050SX3-48YC8")
	})

	// The firewall spans production and transit. Transit is excluded from
	// span detection -- brokering between segments is its entire purpose, so
	// counting it would make every firewall a permanent false positive.
	b.asset(domain.KindFirewall, "fw-edge-1", "rack-a1", []string{"prod", "transit"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Palo Alto"), str("PA-3220")
		a.Serial = str("013101006789")
		a.TeamID = b.team("network")
	})

	// The passive half of the firewall pair. It is what makes losing
	// fw-edge-1 come out DEGRADED rather than DOWN -- and, because the pair's
	// failover_mode is manual, degraded rather than ok.
	b.asset(domain.KindFirewall, "fw-edge-2", "rack-b1", []string{"prod", "transit"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Palo Alto"), str("PA-3220")
		a.Serial = str("013101006790")
		a.TeamID = b.team("network")
	})

	// The out-of-band switch. Nothing on the data plane references it, which
	// is the point: losing it isolates every hypervisor's management path and
	// changes no service's status.
	b.asset(domain.KindSwitch, "sw-oob-1", "rack-a1", []string{"prod"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Aruba"), str("6100-48G")
		a.Serial, a.AssetTag = str("SG9ZKY1234"), str("NET-0003")
		a.TeamID = b.team("network")
	})

	for _, h := range hypervisors() {
		hh := h
		b.asset(domain.KindHypervisor, hh.name, hh.rack, []string{"prod"}, func(a *domain.Asset) {
			a.Vendor, a.Model = str("Dell"), str("PowerEdge R650")
			a.Serial = str(hh.serial)
			a.TeamID = b.team("platform")
			// hv-01 and hv-02 only, and hv-03 DELIBERATELY NOT.
			//
			// lifetimes() gives these two dates of their own, so they override
			// the model's and the asset page says "recorded on this asset".
			// hv-03 is left uncatalogued on purpose: it is the fixture's undated
			// asset, and the expiry report's closing callout -- "an estate where
			// nothing appears to expire is usually one where nobody wrote the
			// dates down" -- needs one to count. An estate with a box nobody has
			// catalogued yet is also the ordinary state of the world.
			//
			// pdu-a1 carries the INHERITANCE demonstration instead: no date of
			// its own, a model that has one.
			if hh.name != "hv-03" {
				a.DeviceTypeID = b.deviceType("PowerEdge R650")
			}
			// hv-01 and hv-02 share rack-a1; hv-03 is in rack-b1, whose height
			// nobody recorded -- so the fixture shows a measured elevation and an
			// assumed one side by side.
			switch hh.name {
			case "hv-01":
				a.RackPosition = num(10)
			case "hv-02":
				a.RackPosition = num(11)
			}
		})
	}

	// The half-installed box, and it is in the fixture on purpose.
	//
	// It is racked, it is in production, its console cable is patched, and its
	// data-plane NIC is not. That combination is one of the most common real
	// findings in an estate -- somebody racked and configured a machine, the
	// remote-hands ticket for the data cabling never closed, and monitoring
	// says nothing because the box answers on the management network.
	//
	// It is what gives every diagram something true and unpleasant to show:
	// the path view draws the backup agent on it as a box connected to
	// nothing and names it, and the prod environment map has one box with no
	// lines. The data-plane rule is demonstrated on real data rather than
	// asserted -- the machine HAS a cable, and it still has no path, because
	// that cable is management.
	b.asset(domain.KindServer, "srv-backup-proxy-1", "rack-b1", []string{"prod"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Dell"), str("PowerEdge R450")
		a.Serial, a.AssetTag = str("FCH2211V0ZZ"), str("SRV-0009")
		a.TeamID = b.team("platform")
		// attrs is opaque to every query by house rule; it is display detail
		// only, and here it carries the why so the demo does not have to.
		a.Attrs = `{"install_note":"data uplink not patched yet -- remote-hands ` +
			`ticket RH-8821 open; reachable on console only"}`
	})

	// Placement is the whole point of the fixture. vault is spread across all
	// three hypervisors so that losing one is survivable and losing a rack is
	// not; the two backend services deliberately share a host so the
	// route-as-node case has something to prove.
	//
	// The development VM at the end of the list sits on production hardware but
	// in the development environment -- which is exactly the kind of thing this
	// tool exists to make visible.
	for _, g := range guests() {
		gg := g
		b.asset(gg.kind, gg.name, gg.host, []string{gg.env}, func(a *domain.Asset) {
			a.TeamID = b.team(gg.team)
		})
	}

	// One storage pool, and a claim against it (migration 00046, WP-J4).
	//
	// IN THE BASE FIXTURE RATHER THAN ONLY THE COMPANY ONE, because this is the
	// estate every web test runs against and a feature the fixture cannot
	// exercise is one the tests cannot prove. That has been true four times
	// here already -- cabling, physical fit, notes and the whole of group J each
	// shipped before anything in the seed exercised them.
	//
	// Three-times replication is the case worth carrying: two thirds of the
	// array buys nothing anybody can put a workload on, and a reader seeing
	// 3 TB raw report 1 TB usable learns more from that one line than from any
	// amount of documentation about ratios.
	b.asset(domain.KindStorage, "ceph-block", "rack-a1", []string{"prod"}, func(a *domain.Asset) {
		a.StorageKind, a.RawCapacityGB = str("ceph_3x"), num(3072)
		a.TeamID = b.team("platform")
	})
	b.storageClaim("vm-db-1", "ceph-block", 200, "database files")
}

// storageClaim records what one workload holds in one pool, skipping anything
// the estate does not contain so a partial deployment neither fails nor
// invents a claim.
func (b *builder) storageClaim(asset, pool string, gb int, note string) {
	if !b.ok() {
		return
	}
	assetID, ok := b.refs.Assets[asset]
	if !ok {
		return
	}
	poolID, ok := b.refs.Assets[pool]
	if !ok {
		return
	}
	held, err := b.store.StorageClaimsFor(b.ctx, assetID)
	if err != nil {
		b.fail(fmt.Errorf("reading claims for %s: %w", asset, err))
		return
	}
	for _, h := range held {
		if h.PoolID == poolID {
			return // already recorded, so a top-up neither rewrites nor fails
		}
	}
	if err := b.store.SetStorageClaim(b.ctx, Actor, assetID, poolID, gb, &note); err != nil {
		b.fail(fmt.Errorf("recording %s in %s: %w", asset, pool, err))
	}
}

// hypervisor is one physical compute host.
type hypervisor struct{ name, rack, serial string }

// hypervisors is the compute inventory, shared by physical() and by the virtual
// layer that hangs a bridge off each one.
func hypervisors() []hypervisor {
	return []hypervisor{
		{"hv-01", "rack-a1", "FCH2033V0YR"},
		{"hv-02", "rack-a1", "FCH2033V0YS"},
		{"hv-03", "rack-b1", "FCH2033V0YT"},
	}
}

// guest is one VM or k8s node and where it runs.
type guest struct{ name, host, kind, env, team string }

// guests is the placement table.
//
// It is a function rather than two literals inside physical() because the
// virtual layer has to cable exactly these guests to exactly their host's
// bridge, in this order. Two copies of the placement table would drift, and the
// symptom would be a guest whose veth lands on another hypervisor's bridge --
// a diagram that confidently draws a wire that does not exist.
func guests() []guest {
	return []guest{
		{"vm-vault-1", "hv-01", domain.KindVM, "prod", "platform"},
		{"vm-db-1", "hv-01", domain.KindVM, "prod", "platform"},
		{"vm-app-1", "hv-01", domain.KindVM, "prod", "platform"},
		{"vm-vault-2", "hv-02", domain.KindVM, "prod", "platform"},
		{"vm-db-2", "hv-02", domain.KindVM, "prod", "platform"},
		{"vm-proxy-1", "hv-02", domain.KindVM, "prod", "platform"},
		{"vm-queue-1", "hv-02", domain.KindVM, "prod", "platform"},
		{"vm-vault-3", "hv-03", domain.KindVM, "prod", "platform"},
		{"vm-sso-1", "hv-03", domain.KindVM, "prod", "platform"},
		{"vm-k8s-1", "hv-03", domain.KindK8sNode, "prod", "platform"},
		{"vm-k8s-2", "hv-03", domain.KindK8sNode, "prod", "platform"},
		{"vm-dev-1", "hv-03", domain.KindVM, "dev", "developers"},
	}
}

// ---------- networking ----------

func (b *builder) networking() {
	if !b.ok() {
		return
	}
	prefixes := []struct {
		cidr, role, env string
		vlan            int
	}{
		{"10.20.0.0/16", "site-supernet", "prod", 0},
		{"10.20.10.0/24", "management", "prod", 10},
		{"10.20.30.0/24", "production-workloads", "prod", 30},
		{"10.20.40.0/24", "development", "dev", 40},
		{"10.20.99.0/24", "transit", "transit", 99},
		{"2001:db8:20::/64", "production-v6", "prod", 30},
	}
	// The VLANs first, because a prefix names one by REFERENCE now. It used to
	// carry a loose integer; the two coexisted for one release and disagreed
	// the first time anybody edited a prefix, which is why 00036 dropped it.
	//
	// Note 10.20.30.0/24 and 2001:db8:20::/64 both sit on VLAN 30 and get the
	// SAME row -- the v4 and v6 halves of one broadcast domain, which the
	// integer had no way to say.
	vlanIDs := map[int]string{}
	for _, p := range prefixes {
		if p.vlan == 0 || vlanIDs[p.vlan] != "" {
			continue
		}
		if !b.ok() {
			return
		}
		v, err := domain.NewVLAN(store.NewID(), p.vlan, p.role, nil)
		if err != nil {
			b.fail(fmt.Errorf("building vlan %d: %w", p.vlan, err))
			return
		}
		if id := b.env(p.env); id != "" {
			v.EnvironmentID = &id
		}
		if err := b.store.CreateVLAN(b.ctx, Actor, v); err != nil {
			b.fail(fmt.Errorf("seeding vlan %d: %w", p.vlan, err))
			return
		}
		vlanIDs[p.vlan] = v.ID
		b.refs.VLANs[v.Name] = v.ID
	}

	for _, p := range prefixes {
		if !b.ok() {
			return
		}
		prefix, err := domain.NewPrefix(store.NewID(), p.cidr)
		if err != nil {
			b.fail(fmt.Errorf("building prefix %s: %w", p.cidr, err))
			return
		}
		prefix.Role = str(p.role)
		if id := vlanIDs[p.vlan]; id != "" {
			prefix.VLANRefID = &id
		}
		if id := b.env(p.env); id != "" {
			prefix.EnvironmentID = &id
		}
		if err := b.store.CreatePrefix(b.ctx, Actor, prefix); err != nil {
			b.fail(fmt.Errorf("seeding prefix %s: %w", p.cidr, err))
			return
		}
	}

	// Interfaces and addresses, enough to make search resolve an IP or a MAC
	// to a box and to give the cabling view something to draw.
	type iface struct {
		asset, name, formFactor, mac, addr string
		speed                              int
		mgmt                               bool
	}
	// masters enslaves a port to a bond, keyed and valued as "asset/interface".
	// Kept beside the table rather than as an eighth positional field, because
	// six ports have a master and every other row in the table below would
	// carry an empty string apiece to say so.
	//
	// This is the first thing in the repository to write interface.lag_parent_id
	// with intent. CreateInterface has always persisted the column and nothing
	// has ever set it, so the self-referencing foreign key it declares had never
	// been exercised by any fixture on either engine.
	//
	// A master must be created BEFORE its members, since the id is resolved out
	// of b.interfaceIDs -- hence bond0 sitting above eno2/eno3 below.
	masters := map[string]string{
		"hv-01/eno2": "hv-01/bond0",
		"hv-01/eno3": "hv-01/bond0",
		"hv-02/eno2": "hv-02/bond0",
		"hv-02/eno3": "hv-02/bond0",
		"hv-03/eno2": "hv-03/bond0",
		// hv-03/eno3 is a configured member of hv-03's bond and carries no
		// cable, which is exactly what "the bond has two slots and one is
		// patched" looks like in a real estate. Enslaving it keeps hv-03's
		// single-homing a cabling decision rather than a missing NIC, which is
		// what the comment on the port itself has always claimed.
		"hv-03/eno3": "hv-03/bond0",
	}
	ifaces := []iface{
		{"sw-core-1", "Ethernet1", domain.FFSFP28, "aa:bb:cc:00:01:01", "", 25000, false},
		{"sw-core-1", "Ethernet2", domain.FFSFP28, "aa:bb:cc:00:01:02", "", 25000, false},
		{"sw-core-1", "Ethernet46", domain.FFQSFP28, "aa:bb:cc:00:01:05", "", 100000, false},
		{"sw-core-1", "Ethernet47", domain.FFQSFP28, "aa:bb:cc:00:01:03", "", 100000, false},
		{"sw-core-1", "Ethernet48", domain.FFSFPPlus, "aa:bb:cc:00:01:04", "", 10000, false},
		{"sw-core-1", "Management1", domain.FFRJ45, "aa:bb:cc:00:01:00", "10.20.10.2", 1000, true},

		{"sw-core-2", "Ethernet1", domain.FFSFP28, "aa:bb:cc:00:03:01", "", 25000, false},
		{"sw-core-2", "Ethernet2", domain.FFSFP28, "aa:bb:cc:00:03:02", "", 25000, false},
		{"sw-core-2", "Ethernet3", domain.FFSFP28, "aa:bb:cc:00:03:03", "", 25000, false},
		{"sw-core-2", "Ethernet46", domain.FFQSFP28, "aa:bb:cc:00:03:06", "", 100000, false},
		{"sw-core-2", "Ethernet47", domain.FFQSFP28, "aa:bb:cc:00:03:04", "", 100000, false},
		{"sw-core-2", "Ethernet48", domain.FFSFPPlus, "aa:bb:cc:00:03:05", "", 10000, false},
		{"sw-core-2", "Management1", domain.FFRJ45, "aa:bb:cc:00:03:00", "10.20.10.4", 1000, true},

		{"fw-edge-1", "ethernet1/1", domain.FFSFPPlus, "aa:bb:cc:00:02:01", "10.20.99.1", 10000, false},
		{"fw-edge-2", "ethernet1/1", domain.FFSFPPlus, "aa:bb:cc:00:04:01", "10.20.99.2", 10000, false},

		// Every port of an out-of-band switch carries management traffic, so
		// every one of them is is_mgmt. That is not cosmetic: derivation marks
		// a forwarder-to-forwarder cable as management-plane if EITHER end is
		// flagged, so if anyone ever patches sw-oob-1 into the core the
		// proposal comes out as a mgmt uplink instead of quietly joining the
		// management switch to the data-plane graph.
		{"sw-oob-1", "Ethernet1", domain.FFRJ45, "aa:bb:cc:00:05:01", "", 1000, true},
		{"sw-oob-1", "Ethernet2", domain.FFRJ45, "aa:bb:cc:00:05:02", "", 1000, true},
		{"sw-oob-1", "Ethernet3", domain.FFRJ45, "aa:bb:cc:00:05:03", "", 1000, true},
		{"sw-oob-1", "Ethernet4", domain.FFRJ45, "aa:bb:cc:00:05:04", "", 1000, true},
		{"sw-oob-1", "Management1", domain.FFRJ45, "aa:bb:cc:00:05:00", "10.20.10.3", 1000, true},

		// The half-installed box. eno1 is patched to the console switch; eno2
		// is the data uplink and is deliberately left UNPATCHED -- the port
		// exists, is enabled, and carries no cable, which is what an open
		// remote-hands ticket looks like in an inventory.
		{"srv-backup-proxy-1", "eno1", domain.FFRJ45, "aa:bb:cc:00:20:01", "10.20.10.30", 1000, true},
		{"srv-backup-proxy-1", "eno2", domain.FFSFP28, "aa:bb:cc:00:20:02", "", 25000, false},

		// eno1 is the management NIC on every hypervisor; eno2 and eno3 are the
		// data uplinks, one to each core chassis. The is_mgmt flag on the HOST
		// side is what derivation reads to set an attachment's plane, so a
		// fixture whose only cables were eno1 (which is what this was before
		// M5) can only ever produce management-plane attachments -- correct,
		// and useless as a demonstration of the data plane.
		{"hv-01", "eno1", domain.FFSFP28, "aa:bb:cc:00:10:01", "10.20.10.11", 25000, true},
		{"hv-02", "eno1", domain.FFSFP28, "aa:bb:cc:00:10:02", "10.20.10.12", 25000, true},
		{"hv-03", "eno1", domain.FFSFP28, "aa:bb:cc:00:10:03", "10.20.10.13", 25000, true},

		// The bond each hypervisor's data NICs are enslaved to, and the port a
		// bridge actually lands on. A host dual-homed into an MC-LAG pair does
		// not hand a bridge one of its two cables -- it bonds them and gives the
		// bridge the bond -- so this had to exist before the bridges in
		// seed_virtual.go could be modelled at all. Speed is the aggregate of
		// the CABLED members: 2x25G on hv-01 and hv-02, 25G on hv-03, whose
		// second slot is configured and unpatched.
		//
		// No MAC. Linux clones a bond's address from its first member rather
		// than assigning one, so a distinct value here would be a fabricated
		// fact and the member's own value would put one address on two rows;
		// every other virtual port in this fixture leaves it empty too.
		{"hv-01", "bond0", domain.FFLAG, "", "", 50000, false},
		{"hv-02", "bond0", domain.FFLAG, "", "", 50000, false},
		{"hv-03", "bond0", domain.FFLAG, "", "", 25000, false},

		{"hv-01", "eno2", domain.FFSFP28, "aa:bb:cc:00:11:01", "", 25000, false},
		{"hv-02", "eno2", domain.FFSFP28, "aa:bb:cc:00:11:02", "", 25000, false},
		{"hv-03", "eno2", domain.FFSFP28, "aa:bb:cc:00:11:03", "", 25000, false},
		{"hv-01", "eno3", domain.FFSFP28, "aa:bb:cc:00:12:01", "", 25000, false},
		{"hv-02", "eno3", domain.FFSFP28, "aa:bb:cc:00:12:02", "", 25000, false},
		// hv-03/eno3 exists and is deliberately left uncabled: hv-03 is the
		// single-homed host the design's scenario 4 turns on, and the spare
		// port is what makes that a cabling decision rather than a missing NIC.
		{"hv-03", "eno3", domain.FFSFP28, "aa:bb:cc:00:12:03", "", 25000, false},
		{"vm-vault-1", "eth0", domain.FFVirtual, "", "10.20.30.11", 10000, false},
		{"vm-vault-2", "eth0", domain.FFVirtual, "", "10.20.30.12", 10000, false},
		{"vm-vault-3", "eth0", domain.FFVirtual, "", "10.20.30.13", 10000, false},
		{"vm-db-1", "eth0", domain.FFVirtual, "", "10.20.30.21", 10000, false},
		{"vm-db-2", "eth0", domain.FFVirtual, "", "10.20.30.22", 10000, false},
		{"vm-app-1", "eth0", domain.FFVirtual, "", "10.20.30.31", 10000, false},
		{"vm-proxy-1", "eth0", domain.FFVirtual, "", "10.20.30.41", 10000, false},
		{"vm-queue-1", "eth0", domain.FFVirtual, "", "10.20.30.51", 10000, false},
		{"vm-sso-1", "eth0", domain.FFVirtual, "", "10.20.30.61", 10000, false},
		{"vm-k8s-1", "eth0", domain.FFVirtual, "", "10.20.30.71", 10000, false},
		{"vm-k8s-2", "eth0", domain.FFVirtual, "", "10.20.30.72", 10000, false},
		{"vm-dev-1", "eth0", domain.FFVirtual, "", "10.20.40.11", 10000, false},
	}

	for _, i := range ifaces {
		if !b.ok() {
			return
		}
		assetID, ok := b.refs.Assets[i.asset]
		if !ok {
			b.fail(fmt.Errorf("seeding interface %s: unknown asset %s", i.name, i.asset))
			return
		}
		iface, err := domain.NewInterface(store.NewID(), assetID, i.name, i.formFactor)
		if err != nil {
			b.fail(fmt.Errorf("building interface %s/%s: %w", i.asset, i.name, err))
			return
		}
		iface.SpeedMbps = num(i.speed)
		iface.IsMgmt = i.mgmt
		if master, ok := masters[i.asset+"/"+i.name]; ok {
			masterID, ok := b.interfaceIDs[master]
			if !ok {
				b.fail(fmt.Errorf("seeding interface %s/%s: unknown master %s", i.asset, i.name, master))
				return
			}
			iface.LagParentID = &masterID
		}
		if i.mac != "" {
			if err := iface.SetMAC(i.mac); err != nil {
				b.fail(fmt.Errorf("setting mac on %s/%s: %w", i.asset, i.name, err))
				return
			}
		}
		if err := b.store.CreateInterface(b.ctx, Actor, iface); err != nil {
			b.fail(fmt.Errorf("seeding interface %s/%s: %w", i.asset, i.name, err))
			return
		}
		b.interfaceIDs[i.asset+"/"+i.name] = iface.ID

		if i.addr != "" {
			addr, err := domain.NewIPAddress(store.NewID(), i.addr, &iface.ID, domain.IPRolePrimary)
			if err != nil {
				b.fail(fmt.Errorf("building address %s: %w", i.addr, err))
				return
			}
			if err := b.store.CreateIPAddress(b.ctx, Actor, addr); err != nil {
				b.fail(fmt.Errorf("seeding address %s: %w", i.addr, err))
				return
			}
		}
	}

	// The cable plant. Every cable here is backed by a declared attachment or
	// uplink in topology() below -- run "Propose from cabling" against a fresh
	// database and it correctly proposes nothing, because the model and the
	// plant already agree.
	//
	// Three shapes are deliberate and load-bearing:
	//   - hv-01 and hv-02 are dual-homed, one cable to each core chassis;
	//   - hv-03 lands on sw-core-2 only, so losing sw-core-2 cuts it off even
	//     though the group survives at 1-of-2;
	//   - sw-core-1 and sw-core-2 are peered, a cycle in the cable plant that
	//     union-find has to eat without looping.
	cables := []struct {
		a, b, medium string
		lengthM      int
	}{
		{"hv-01/eno2", "sw-core-1/Ethernet1", "DAC", 3},
		{"hv-01/eno3", "sw-core-2/Ethernet1", "OM4", 20},
		{"hv-02/eno2", "sw-core-1/Ethernet2", "DAC", 3},
		{"hv-02/eno3", "sw-core-2/Ethernet2", "OM4", 20},
		{"hv-03/eno2", "sw-core-2/Ethernet3", "DAC", 3},

		// The MC-LAG peer bond is TWO cables, as a real one is -- and on the
		// neighbourhood diagram they are the parallel-edge case: two separate
		// lines with two separate port pairs in their hover text, not one line
		// drawn twice. One cable here would demo redundancy that isn't there.
		{"sw-core-1/Ethernet46", "sw-core-2/Ethernet46", "OM4", 25},
		{"sw-core-1/Ethernet47", "sw-core-2/Ethernet47", "OM4", 25},
		{"sw-core-1/Ethernet48", "fw-edge-1/ethernet1/1", "DAC", 2},
		{"sw-core-2/Ethernet48", "fw-edge-2/ethernet1/1", "DAC", 2},

		// Console access, and nothing else. srv-backup-proxy-1/eno2 is left
		// unpatched on purpose; see the asset comment in physical().
		{"srv-backup-proxy-1/eno1", "sw-oob-1/Ethernet4", "Cat6", 3},

		{"hv-01/eno1", "sw-oob-1/Ethernet1", "Cat6", 3},
		{"hv-02/eno1", "sw-oob-1/Ethernet2", "Cat6", 3},
		{"hv-03/eno1", "sw-oob-1/Ethernet3", "Cat6", 15},
	}
	for _, c := range cables {
		if !b.ok() {
			return
		}
		// A missing key here would otherwise reach NewLink as an empty string
		// and be reported as "a_interface_id is required", which names the
		// constraint rather than the typo that caused it.
		aID, ok := b.interfaceIDs[c.a]
		if !ok {
			b.fail(fmt.Errorf("seeding link %s-%s: unknown interface %s", c.a, c.b, c.a))
			return
		}
		bID, ok := b.interfaceIDs[c.b]
		if !ok {
			b.fail(fmt.Errorf("seeding link %s-%s: unknown interface %s", c.a, c.b, c.b))
			return
		}
		link, err := domain.NewLink(store.NewID(), aID, bID)
		if err != nil {
			b.fail(fmt.Errorf("building link %s-%s: %w", c.a, c.b, err))
			return
		}
		link.Medium = str(c.medium)
		link.LengthM = num(c.lengthM)
		if err := b.store.CreateLink(b.ctx, Actor, link); err != nil {
			b.fail(fmt.Errorf("seeding link %s-%s: %w", c.a, c.b, err))
			return
		}
	}
}

// ---------- identities ----------

func (b *builder) identities() {
	identities := []struct{ kind, name, realm, secretRef string }{
		{domain.IdentityServiceAccount, "svc-orders", "vault", "kv/prod/orders/db"},
		{domain.IdentityServiceAccount, "svc-sso", "vault", "kv/prod/sso/db"},
		{domain.IdentityMachineAccount, "svc-backup$", "AD", "kv/prod/backup/windows"},
	}
	for _, i := range identities {
		if !b.ok() {
			return
		}
		identity, err := domain.NewIdentity(store.NewID(), i.kind, i.name)
		if err != nil {
			b.fail(fmt.Errorf("building identity %s: %w", i.name, err))
			return
		}
		identity.Realm = str(i.realm)
		// A path, never a secret. If this field ever held a credential the
		// whole database would become a secret store, which it must not be.
		identity.SecretRef = str(i.secretRef)
		identity.RotationDays = num(90)
		identity.TeamID = b.team("platform")
		if err := b.store.CreateIdentity(b.ctx, Actor, identity); err != nil {
			b.fail(fmt.Errorf("seeding identity %s: %w", i.name, err))
			return
		}
		b.identityIDs[i.name] = identity.ID
	}
}
