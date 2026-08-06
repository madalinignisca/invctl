// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The derived footprint and the hidden-dependency finding.
//
// Every test here has a CONTROL: something that must be absent as well as
// something that must be present. A footprint test with only positive cases
// goes green against an implementation that returns the whole estate, and the
// dependency classifier goes green against one that puts every edge in one
// bucket.

// footprintFixture builds an estate with the shapes that distinguish a correct
// derivation from a plausible one:
//
//	orders   owns hv-01           -> implies vm-app-1, vm-db-1 and what runs there
//	         uses sw-core-1       -> implies NOTHING (this is the control)
//	platform owns hv-02           -> its guest vm-shared-1 is somebody else's
//	         owns vm-db-1         -> a CONFLICT: inside orders' hypervisor
type footprintFixture struct {
	*projectFixture
}

func newFootprintFixture(t *testing.T, e Engine) *footprintFixture {
	t.Helper()
	base := newProjectFixture(t, e)
	f := &footprintFixture{projectFixture: base}
	s, ctx := f.s, f.ctx

	hv := f.assets["hv-01"]
	f.assets["vm-db-1"] = mustAsset(t, s, ctx, domain.KindVM, "vm-db-1", &hv, f.env)

	// A second hypervisor with a guest, owned by the other project. Nothing
	// about it may appear in orders' footprint.
	f.assets["hv-02"] = mustAsset(t, s, ctx, domain.KindHypervisor, "hv-02", nil, f.env)
	hv2 := f.assets["hv-02"]
	f.assets["vm-shared-1"] = mustAsset(t, s, ctx, domain.KindVM, "vm-shared-1", &hv2, f.env)

	// A guest of the switch orders merely USES. If derivation ever follows
	// `uses`, this shows up and the test fails -- which is the point.
	sw := f.assets["sw-core-1"]
	f.assets["sw-blade-1"] = mustAsset(t, s, ctx, domain.KindServer, "sw-blade-1", &sw, f.env)

	f.mustLinkAsset(t, "orders", "hv-01", domain.ProjectOwns)
	f.mustLinkAsset(t, "orders", "sw-core-1", domain.ProjectUses)
	f.mustLinkAsset(t, "platform", "hv-02", domain.ProjectOwns)
	return f
}

func (f *footprintFixture) mustLinkAsset(t *testing.T, project, asset, relation string) {
	t.Helper()
	if err := f.link(t, project, asset, relation); err != nil {
		t.Fatalf("linking %s %s %s: %v", project, relation, asset, err)
	}
}

func (f *footprintFixture) mustLinkService(t *testing.T, project, service, relation string) {
	t.Helper()
	l, err := domain.NewProjectServiceLink(f.projects[project], f.services[service], relation, nil, f.s.Now())
	if err != nil {
		t.Fatalf("building service link: %v", err)
	}
	if err := f.s.LinkProjectService(f.ctx, testActor, l); err != nil {
		t.Fatalf("linking %s %s %s: %v", project, relation, service, err)
	}
}

func (f *footprintFixture) impliedNames(fp *ProjectFootprint) map[string]bool {
	out := map[string]bool{}
	for _, a := range fp.ImpliedAssets {
		out[a.Name] = true
	}
	return out
}

// TestFootprintFollowsOwnershipOnly is the central claim of the whole design.
func TestFootprintFollowsOwnershipOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newFootprintFixture(t, e)

			fp, err := f.s.ProjectFootprint(f.ctx, f.projects["orders"])
			if err != nil {
				t.Fatalf("ProjectFootprint: %v", err)
			}
			implied := f.impliedNames(fp)

			// Inside the owned hypervisor: derived.
			for _, want := range []string{"vm-app-1", "vm-db-1"} {
				if !implied[want] {
					t.Errorf("%s is inside an owned hypervisor and is not in the footprint", want)
				}
			}
			// Inside the merely-used switch: NOT derived. This is the control,
			// and without it the test passes against an implementation that
			// derives from every link.
			if implied["sw-blade-1"] {
				t.Error("sw-blade-1 sits inside an asset this project only USES; deriving it " +
					"attributes somebody else's estate — and later somebody else's cost")
			}
			// Another project's hypervisor and guest: nowhere near it.
			for _, never := range []string{"hv-02", "vm-shared-1"} {
				if implied[never] {
					t.Errorf("%s belongs to another project and appears in this footprint", never)
				}
			}
			// The owned asset itself is declared, not implied.
			if implied["hv-01"] {
				t.Error("the owned asset is listed as implied; the footprint would double-count it")
			}
		})
	}
}

// TestFootprintInventsNoRows. The derivation must leave the tables exactly as
// it found them -- a derived fact written back is indistinguishable from a
// declared one in change_log.
func TestFootprintInventsNoRows(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newFootprintFixture(t, e)

			before, err := f.s.countOne(f.ctx, `SELECT COUNT(*) FROM project_asset`)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			beforeAudit := f.changeRows(t, "project_asset")

			if _, err := f.s.ProjectFootprint(f.ctx, f.projects["orders"]); err != nil {
				t.Fatalf("ProjectFootprint: %v", err)
			}

			after, err := f.s.countOne(f.ctx, `SELECT COUNT(*) FROM project_asset`)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if after != before {
				t.Errorf("project_asset rows went %d -> %d; the footprint materialised itself",
					before, after)
			}
			if got := f.changeRows(t, "project_asset"); got != beforeAudit {
				t.Errorf("computing a footprint wrote %d audit rows", got-beforeAudit)
			}
		})
	}
}

// TestFootprintReportsAConflictRatherThanAbsorbing.
//
// A VM inside our hypervisor that another project owns outright is not ours to
// count. Absorbing it silently would double-count it the day cost lands, and
// the owner would never know.
func TestFootprintReportsAConflictRatherThanAbsorbing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newFootprintFixture(t, e)
			// platform owns vm-db-1, which lives inside orders' hv-01.
			f.mustLinkAsset(t, "platform", "vm-db-1", domain.ProjectOwns)

			fp, err := f.s.ProjectFootprint(f.ctx, f.projects["orders"])
			if err != nil {
				t.Fatalf("ProjectFootprint: %v", err)
			}
			if len(fp.Conflicts) != 1 {
				t.Fatalf("%d conflicts, want exactly the one another project owns", len(fp.Conflicts))
			}
			c := fp.Conflicts[0]
			if c.AssetName != "vm-db-1" || c.ProjectCode != "platform" {
				t.Errorf("conflict = %s owned by %s, want vm-db-1 owned by platform",
					c.AssetName, c.ProjectCode)
			}
			// And the control: with no competing owner there is no conflict, so
			// the report is not just always non-empty.
			plain, err := f.s.ProjectFootprint(f.ctx, f.projects["platform"])
			if err != nil {
				t.Fatalf("ProjectFootprint: %v", err)
			}
			for _, c := range plain.Conflicts {
				if c.AssetName == "vm-shared-1" {
					t.Error("an uncontested guest is reported as a conflict")
				}
			}
		})
	}
}

// TestTheNodeBudgetCutsThePictureAndNotTheFootprint.
//
// The budget used to live on ProjectFootprint, which made it a rendering limit
// leaking into an accounting one: everything downstream -- conflicts, implied
// services, and then the cost rollup -- inherited a cap that exists so an SVG
// stays legible. The symptom was a total that was simply wrong, with nothing on
// the page to say so.
//
// So both halves are asserted together, because they are one rule: the map cuts
// and reports the cut, and the footprint does not cut at all.
func TestTheNodeBudgetCutsThePictureAndNotTheFootprint(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newFootprintFixture(t, e)
			hv := f.assets["hv-01"]
			for _, n := range []string{"vm-x1", "vm-x2", "vm-x3"} {
				mustAsset(t, f.s, f.ctx, domain.KindVM, n, &hv, f.env)
			}

			fp, err := f.s.ProjectFootprint(f.ctx, f.projects["orders"])
			if err != nil {
				t.Fatalf("ProjectFootprint: %v", err)
			}
			if len(fp.ImpliedAssets) != 5 {
				t.Errorf("the footprint holds %d implied assets, want all 5 — it must not "+
					"apply a drawing budget to an accounting question", len(fp.ImpliedAssets))
			}

			g, err := f.s.ProjectMap(f.ctx, f.projects["orders"], 2)
			if err != nil {
				t.Fatalf("ProjectMap: %v", err)
			}
			if len(g.Assets) != 2 {
				t.Errorf("the map drew %d assets, want the budget of 2", len(g.Assets))
			}
			// A cut nobody is told about is indistinguishable from a fact that
			// does not exist.
			if g.AssetsElided == 0 {
				t.Error("the map cut assets without reporting how many")
			}
		})
	}
}

// TestHiddenDependenciesAreClassified.
//
// Three buckets, each with its own edge, because a classifier that puts
// everything in one bucket passes any test that only checks one.
func TestHiddenDependenciesAreClassified(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newFootprintFixture(t, e)
			s, ctx := f.s, f.ctx

			// orders owns orders-api. platform owns pgsql-core. A third service
			// belongs to nobody.
			f.mustLinkService(t, "orders", "orders-api", domain.ProjectOwns)
			f.mustLinkService(t, "platform", "pgsql-core", domain.ProjectOwns)

			orphan, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "orphan", Name: "orphan", Kind: domain.SvcAPI,
				EnvironmentID: f.env, Availability: domain.AvailStandalone, Tier: 3,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateService(ctx, testActor, orphan); err != nil {
				t.Fatalf("creating: %v", err)
			}
			f.services["orphan"] = orphan.ID

			// A fourth that orders explicitly declared it uses -- known, so it
			// must be reported as shared rather than as a finding.
			shared, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "shared-bus", Name: "shared-bus", Kind: domain.SvcQueue,
				EnvironmentID: f.env, Availability: domain.AvailStandalone, Tier: 3,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateService(ctx, testActor, shared); err != nil {
				t.Fatalf("creating: %v", err)
			}
			f.services["shared-bus"] = shared.ID
			f.mustLinkService(t, "orders", "shared-bus", domain.ProjectUses)

			dep := func(consumer, provider string) {
				t.Helper()
				port := 5432
				ep, err := domain.NewEndpoint(NewID(), f.services[provider], "svc",
					domain.ProtoTCP, &port, domain.BindHost)
				if err != nil {
					t.Fatalf("building endpoint: %v", err)
				}
				if err := s.CreateEndpoint(ctx, testActor, ep); err != nil {
					t.Fatalf("creating endpoint: %v", err)
				}
				d, err := domain.NewDependency(NewID(), domain.DependencySpec{
					ConsumerServiceID:  f.services[consumer],
					ProviderEndpointID: &ep.ID,
					Nature:             domain.NatureHard,
					FailureMode:        "stops",
				}, s.Now())
				if err != nil {
					t.Fatalf("building dependency: %v", err)
				}
				if err := s.CreateDependency(ctx, testActor, d, nil); err != nil {
					t.Fatalf("creating dependency: %v", err)
				}
			}
			dep("orders-api", "pgsql-core") // owned by another project
			dep("orders-api", "orphan")     // owned by nobody
			dep("orders-api", "shared-bus") // declared as used

			fp, err := s.ProjectFootprint(ctx, f.projects["orders"])
			if err != nil {
				t.Fatalf("footprint: %v", err)
			}
			report, err := s.ProjectExternalDependencies(ctx, fp)
			if err != nil {
				t.Fatalf("dependencies: %v", err)
			}

			if len(report.External) != 1 || report.External[0].ProviderCode != "pgsql-core" {
				t.Errorf("External = %+v, want exactly pgsql-core", codesOfDeps(report.External))
			}
			if len(report.External) == 1 && report.External[0].OwningProjectCode != "platform" {
				t.Errorf("the external dependency does not name its owning project: %q",
					report.External[0].OwningProjectCode)
			}
			if len(report.Unowned) != 1 || report.Unowned[0].ProviderCode != "orphan" {
				t.Errorf("Unowned = %v, want exactly orphan", codesOfDeps(report.Unowned))
			}
			if len(report.Shared) != 1 || report.Shared[0].ProviderCode != "shared-bus" {
				t.Errorf("Shared = %v, want exactly shared-bus", codesOfDeps(report.Shared))
			}
		})
	}
}

// TestUsedServicesDoNotDragInTheirDependencies.
//
// If a project merely uses shared PostgreSQL, PostgreSQL's own upstreams are
// not this project's hidden dependencies. Without this the panel fills with
// other teams' problems and stops being read.
func TestUsedServicesDoNotDragInTheirDependencies(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newFootprintFixture(t, e)
			s, ctx := f.s, f.ctx

			f.mustLinkService(t, "orders", "pgsql-core", domain.ProjectUses)

			// pgsql-core depends on something. That is the platform team's
			// concern, not orders'.
			port := 8200
			ep, err := domain.NewEndpoint(NewID(), f.services["orders-api"], "api",
				domain.ProtoTCP, &port, domain.BindHost)
			if err != nil {
				t.Fatalf("building endpoint: %v", err)
			}
			if err := s.CreateEndpoint(ctx, testActor, ep); err != nil {
				t.Fatalf("creating endpoint: %v", err)
			}
			d, err := domain.NewDependency(NewID(), domain.DependencySpec{
				ConsumerServiceID:  f.services["pgsql-core"],
				ProviderEndpointID: &ep.ID,
				Nature:             domain.NatureHard,
				FailureMode:        "stops",
			}, s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := s.CreateDependency(ctx, testActor, d, nil); err != nil {
				t.Fatalf("creating dependency: %v", err)
			}

			fp, err := s.ProjectFootprint(ctx, f.projects["orders"])
			if err != nil {
				t.Fatalf("footprint: %v", err)
			}
			report, err := s.ProjectExternalDependencies(ctx, fp)
			if err != nil {
				t.Fatalf("dependencies: %v", err)
			}
			if report.Total() != 0 {
				t.Errorf("a merely-used service dragged in %d of its own dependencies: %v",
					report.Total(), codesOfDeps(append(report.External, report.Unowned...)))
			}
		})
	}
}

// TestProjectMapDrawsMembershipNotAWalk: the picture is what the project
// declared plus what that implies, and nothing else.
func TestProjectMapDrawsMembershipNotAWalk(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newFootprintFixture(t, e)

			g, err := f.s.ProjectMap(f.ctx, f.projects["orders"], 0)
			if err != nil {
				t.Fatalf("ProjectMap: %v", err)
			}
			drawn := map[string]bool{}
			for _, a := range g.Assets {
				drawn[a.Name] = true
			}
			for _, want := range []string{"hv-01", "vm-app-1", "vm-db-1", "sw-core-1"} {
				if !drawn[want] {
					t.Errorf("the map is missing %q", want)
				}
			}
			// Another project's estate is not in this picture, however cabled.
			for _, never := range []string{"hv-02", "vm-shared-1"} {
				if drawn[never] {
					t.Errorf("%q belongs to another project and is drawn on this map", never)
				}
			}
		})
	}
}

func codesOfDeps(deps []ExternalDependency) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.ProviderCode)
	}
	return out
}
