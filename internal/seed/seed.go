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
	"time"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// Actor attributes seeded rows in the change log, so demo data is instantly
// distinguishable from anything an operator typed.
var Actor = domain.Actor{Name: "seed", Kind: "system"}

// Refs holds the ids of the fixture rows the tests need to name. Building it
// as a return value keeps the tests from hard-coding ids or looking rows up by
// display name.
type Refs struct {
	Environments map[string]string // code -> id
	Assets       map[string]string // name -> id
	Services     map[string]string // code -> id
	Endpoints    map[string]string // "service/endpoint" -> id
	Routes       map[string]string // match value -> id
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

	interfaceIDs map[string]string // "asset/interface" -> id
	identityIDs  map[string]string // name -> id
	poolIDs      map[string]string // name -> id
}

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
			Endpoints:    map[string]string{},
			Routes:       map[string]string{},
		},
	}

	b.environments()
	b.physical()
	b.networking()
	b.identities()
	b.services()
	b.endpoints()
	b.routing()
	b.dependencies()

	if b.err != nil {
		return nil, b.err
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

	b.asset(domain.KindRack, "rack-a1", "dc-oslo", []string{"prod"}, nil)
	b.asset(domain.KindRack, "rack-b1", "dc-oslo", []string{"prod"}, nil)

	// The PDU is a containment parent for nothing, but losing it takes the
	// rack with it -- modelled here as a sibling so the demo can show that
	// "PDU fails" needs the operator to select the rack, not the PDU alone.
	b.asset(domain.KindPDU, "pdu-a1", "rack-a1", []string{"prod"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("APC"), str("AP8853")
		a.Serial = str("5A2134X09881")
	})

	// The shared switch: it carries both production and development VLANs, so
	// it belongs to two non-transit environments and shows up in the
	// span-detection report. This is the case §10 asks for.
	b.asset(domain.KindSwitch, "sw-core-1", "rack-a1", []string{"prod", "dev"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Arista"), str("DCS-7050SX3-48YC8")
		a.Serial, a.AssetTag = str("JPE19140XYZ"), str("NET-0001")
		a.OwnerTeam = str("network")
	})

	// The firewall spans production and transit. Transit is excluded from
	// span detection -- brokering between segments is its entire purpose, so
	// counting it would make every firewall a permanent false positive.
	b.asset(domain.KindFirewall, "fw-edge-1", "rack-a1", []string{"prod", "transit"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Palo Alto"), str("PA-3220")
		a.Serial = str("013101006789")
		a.OwnerTeam = str("network")
	})

	hypervisors := []struct{ name, rack, serial string }{
		{"hv-01", "rack-a1", "FCH2033V0YR"},
		{"hv-02", "rack-a1", "FCH2033V0YS"},
		{"hv-03", "rack-b1", "FCH2033V0YT"},
	}
	for _, h := range hypervisors {
		hh := h
		b.asset(domain.KindHypervisor, hh.name, hh.rack, []string{"prod"}, func(a *domain.Asset) {
			a.Vendor, a.Model = str("Dell"), str("PowerEdge R650")
			a.Serial = str(hh.serial)
			a.OwnerTeam = str("platform")
		})
	}

	// Placement is the whole point of the fixture. vault is spread across all
	// three hypervisors so that losing one is survivable and losing a rack is
	// not; the two backend services deliberately share a host so the
	// route-as-node case has something to prove.
	vms := []struct{ name, host, kind string }{
		{"vm-vault-1", "hv-01", domain.KindVM},
		{"vm-db-1", "hv-01", domain.KindVM},
		{"vm-app-1", "hv-01", domain.KindVM},
		{"vm-vault-2", "hv-02", domain.KindVM},
		{"vm-db-2", "hv-02", domain.KindVM},
		{"vm-proxy-1", "hv-02", domain.KindVM},
		{"vm-queue-1", "hv-02", domain.KindVM},
		{"vm-vault-3", "hv-03", domain.KindVM},
		{"vm-sso-1", "hv-03", domain.KindVM},
		{"vm-k8s-1", "hv-03", domain.KindK8sNode},
		{"vm-k8s-2", "hv-03", domain.KindK8sNode},
	}
	for _, vm := range vms {
		b.asset(vm.kind, vm.name, vm.host, []string{"prod"}, func(a *domain.Asset) {
			a.OwnerTeam = str("platform")
		})
	}

	// The development VM sits on production hardware but in the development
	// environment -- which is exactly the kind of thing this tool exists to
	// make visible.
	b.asset(domain.KindVM, "vm-dev-1", "hv-03", []string{"dev"}, func(a *domain.Asset) {
		a.OwnerTeam = str("developers")
	})
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
		if p.vlan > 0 {
			prefix.VLANID = num(p.vlan)
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
	ifaces := []iface{
		{"sw-core-1", "Ethernet1", domain.FFSFP28, "aa:bb:cc:00:01:01", "", 25000, false},
		{"sw-core-1", "Ethernet2", domain.FFSFP28, "aa:bb:cc:00:01:02", "", 25000, false},
		{"sw-core-1", "Management1", domain.FFRJ45, "aa:bb:cc:00:01:00", "10.20.10.2", 1000, true},
		{"fw-edge-1", "ethernet1/1", domain.FFSFPPlus, "aa:bb:cc:00:02:01", "10.20.99.1", 10000, false},
		{"hv-01", "eno1", domain.FFSFP28, "aa:bb:cc:00:10:01", "10.20.10.11", 25000, true},
		{"hv-02", "eno1", domain.FFSFP28, "aa:bb:cc:00:10:02", "10.20.10.12", 25000, true},
		{"hv-03", "eno1", domain.FFSFP28, "aa:bb:cc:00:10:03", "10.20.10.13", 25000, true},
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

	// A couple of cables, so the interface view has a far end to show.
	cables := []struct{ a, b string }{
		{"hv-01/eno1", "sw-core-1/Ethernet1"},
		{"hv-02/eno1", "sw-core-1/Ethernet2"},
	}
	for _, c := range cables {
		if !b.ok() {
			return
		}
		link, err := domain.NewLink(store.NewID(), b.interfaceIDs[c.a], b.interfaceIDs[c.b])
		if err != nil {
			b.fail(fmt.Errorf("building link %s-%s: %w", c.a, c.b, err))
			return
		}
		link.Medium = str("DAC")
		link.LengthM = num(3)
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
		identity.OwnerTeam = str("platform")
		if err := b.store.CreateIdentity(b.ctx, Actor, identity); err != nil {
			b.fail(fmt.Errorf("seeding identity %s: %w", i.name, err))
			return
		}
		b.identityIDs[i.name] = identity.ID
	}
}
