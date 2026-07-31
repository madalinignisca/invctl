package seed

import (
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// ---------- projects ----------
//
// The business layer of the fixture, and the successor to the single
// `application` row migration 00010 absorbed.
//
// Every link below is chosen so that one derived panel has something true to
// say. A demo where the interesting boxes are empty proves nothing, and a demo
// where they are full of invented findings is worse -- so the findings here are
// consequences of the estate the rest of the seed already describes:
//
//   platform owns the three hypervisors, commerce owns a VM inside one of them
//     -> a FOOTPRINT CONFLICT, the double-counting a cost model would inherit
//   commerce owns vm-app-1, and Veeam runs on it uninvited
//     -> an IMPLIED SERVICE somebody else owns, on your hardware
//   nobody owns haproxy-edge
//     -> an UNOWNED dependency: the edge every partner order crosses, with no
//        team to escalate to. The most realistic finding in the fixture.
//   orders declared `uses` for the database and SSO but not for the queue
//     -> SHARED versus EXTERNAL, side by side, from the same dependency list
//
// orders-api-dev is deliberately left in no project at all: the service list
// has to be able to show "belongs to nobody" as an ordinary state, because in a
// real estate most rows start there.

type projectSpec struct {
	code, name, owner, description string
	ownsAssets                     []string
	usesAssets                     []string
	ownsServices                   []string
	usesServices                   []string
}

func (b *builder) projects() {
	if !b.ok() {
		return
	}

	specs := []projectSpec{
		{
			code: "orders", name: "Orders Platform", owner: "commerce",
			description: "Everything a customer touches when they place an order, " +
				"plus the partner submission path.",
			// The VM, not the hypervisor under it. Commerce is answerable for
			// what runs in this box and for its capacity; it is not answerable
			// for the metal, and saying otherwise is how one team ends up
			// nominally owning another team's hardware.
			ownsAssets:   []string{"vm-app-1"},
			ownsServices: []string{"orders-api", "orders-web", "partner-gateway"},
			// Declared, so the database and SSO come back as `shared` rather
			// than as findings: commerce already knows it is standing on them.
			// The queue is deliberately NOT declared, which is what makes the
			// external/shared split visible on one page.
			usesServices: []string{"pgsql-core", "sso"},
		},
		{
			code: "platform", name: "Platform & Core Services", owner: "platform",
			description: "The shared substrate: virtualisation hosts, identity, " +
				"secrets, the core database and the message broker.",
			// The metal. Owning it is what makes every guest an IMPLIED asset,
			// which is the derivation the whole feature exists to demonstrate.
			ownsAssets:   []string{"hv-01", "hv-02", "hv-03"},
			ownsServices: []string{"vault", "pgsql-core", "sso", "rabbitmq", "backup-agent"},
			// haproxy-edge is missing on purpose. See the header.
		},
		{
			code: "observability", name: "Observability", owner: "observability",
			description: "Metrics and logs for the whole estate. Owns the collectors, " +
				"owns nothing that produces the data.",
			// The stranded box: observability owns the host whose log shipper
			// cannot reach anything, so the finding lands on the team that can
			// act on it rather than on nobody.
			ownsAssets:   []string{"srv-backup-proxy-1"},
			ownsServices: []string{"mimir-ingester", "log-shipper"},
			usesServices: []string{"rabbitmq"},
		},
	}

	for _, spec := range specs {
		if !b.ok() {
			return
		}
		p, err := domain.NewProject(store.NewID(), domain.ProjectSpec{
			Code: spec.code, Name: spec.name,
			Description: str(spec.description),
			OwnerTeam:   str(spec.owner),
			Lifecycle:   domain.LifecycleActive,
		}, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building project %s: %w", spec.code, err))
			return
		}
		if err := b.store.CreateProject(b.ctx, Actor, p); err != nil {
			b.fail(fmt.Errorf("seeding project %s: %w", spec.code, err))
			return
		}
		b.refs.Projects[spec.code] = p.ID

		b.linkAssets(p, domain.ProjectOwns, spec.ownsAssets)
		b.linkAssets(p, domain.ProjectUses, spec.usesAssets)
		b.linkServices(p, domain.ProjectOwns, spec.ownsServices)
		b.linkServices(p, domain.ProjectUses, spec.usesServices)
	}
}

func (b *builder) linkAssets(p *domain.Project, relation string, names []string) {
	for _, name := range names {
		if !b.ok() {
			return
		}
		id, ok := b.refs.Assets[name]
		if !ok {
			b.fail(fmt.Errorf("linking %s to project %s: unknown asset", name, p.Code))
			return
		}
		link, err := domain.NewProjectAssetLink(p.ID, id, relation, nil, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building the %s link from %s to %s: %w", relation, p.Code, name, err))
			return
		}
		if err := b.store.LinkProjectAsset(b.ctx, Actor, link); err != nil {
			b.fail(fmt.Errorf("linking %s to project %s: %w", name, p.Code, err))
			return
		}
	}
}

func (b *builder) linkServices(p *domain.Project, relation string, codes []string) {
	for _, code := range codes {
		if !b.ok() {
			return
		}
		id, ok := b.refs.Services[code]
		if !ok {
			b.fail(fmt.Errorf("linking %s to project %s: unknown service", code, p.Code))
			return
		}
		link, err := domain.NewProjectServiceLink(p.ID, id, relation, nil, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building the %s link from %s to %s: %w", relation, p.Code, code, err))
			return
		}
		if err := b.store.LinkProjectService(b.ctx, Actor, link); err != nil {
			b.fail(fmt.Errorf("linking %s to project %s: %w", code, p.Code, err))
			return
		}
	}
}
