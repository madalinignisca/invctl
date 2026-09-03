// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-1.1 item 1: dependency moves from ScopeTopology to
// domain.ScopeSubjectDerived. This file is the two-ended shape
// journal_scope_test.go's single-subject version and
// subject_derived_scope_test.go's service_instance case already proved,
// applied to an edge whose two subjects are both services: the consumer
// directly, and the provider one hop away through an endpoint or a route.
//
// EACH END IS EXERCISED INDEPENDENTLY, deliberately -- a table that only
// ever varied both ends together would pass with a fix that checked just
// one of them, e.g. a copy-paste of authorizeInstanceSubjects that dropped
// the second p.Covers call.

// mustEndpoint builds and stores a minimal active socket on a service.
func mustEndpoint(t *testing.T, s *SQLStore, ctx context.Context, serviceID, name string, port int) string {
	t.Helper()
	p := port
	ep, err := domain.NewEndpoint(NewID(), serviceID, name, domain.ProtoTCP, &p, domain.BindHost)
	if err != nil {
		t.Fatalf("building endpoint %s: %v", name, err)
	}
	if err := s.CreateEndpoint(ctx, testPermit, ep); err != nil {
		t.Fatalf("creating endpoint %s: %v", name, err)
	}
	return ep.ID
}

// mustRoute builds a route fronting a fresh socket on serviceID, through a
// pool on that same service -- enough to prove resolveProviderService's
// route -> frontend endpoint -> service join, without the pool's own
// membership mattering to authorization at all.
func mustRoute(t *testing.T, s *SQLStore, ctx context.Context, serviceID, name string, port int) string {
	t.Helper()
	frontend := mustEndpoint(t, s, ctx, serviceID, name+"-front", port)
	pool := &domain.BackendPool{ID: NewID(), ServiceID: serviceID, Name: name + "-pool"}
	if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
		t.Fatalf("creating backend pool for route %s: %v", name, err)
	}
	r, err := domain.NewRoute(NewID(), frontend, "default", pool.ID)
	if err != nil {
		t.Fatalf("building route %s: %v", name, err)
	}
	if err := s.CreateRoute(ctx, testPermit, r); err != nil {
		t.Fatalf("creating route %s: %v", name, err)
	}
	return r.ID
}

// depScopeFixture sets up three services -- S1 (the project owner's scope),
// S2 (an unrelated consumer) and S3 (an unrelated provider) -- and one
// socket on each of S1 and S3, which is all every case below needs.
type depScopeFixture struct {
	s          *SQLStore
	ctx        context.Context
	s1, s2, s3 string
	ep1, ep3   string // sockets on s1 and s3
	permit     domain.Permit
}

func newDepScopeFixture(t *testing.T, e Engine) *depScopeFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	frontend := mustProjectForAssignment(t, s, ctx, "frontend")
	s1 := mustService(t, s, ctx, "s1")
	s2 := mustService(t, s, ctx, "s2")
	s3 := mustService(t, s, ctx, "s3")
	ep1 := mustEndpoint(t, s, ctx, s1, "sock", 8001)
	ep3 := mustEndpoint(t, s, ctx, s3, "sock", 8003)

	permit := domain.ScopedPermit(
		domain.Actor{ID: "po-11", Name: "po-11", Kind: domain.ActorKindUser},
		[]string{frontend},
		domain.ScopedEntities{"service": {s1: true}})

	return &depScopeFixture{s: s, ctx: ctx, s1: s1, s2: s2, s3: s3, ep1: ep1, ep3: ep3, permit: permit}
}

func newDependencySpec(consumerServiceID, providerEndpointID string) domain.DependencySpec {
	return domain.DependencySpec{
		ConsumerServiceID:  consumerServiceID,
		ProviderEndpointID: &providerEndpointID,
		Nature:             domain.NatureHard,
		FailureMode:        "it stops",
	}
}

// TestDependencyScopeCoversNeitherEnd: consumer S2, provider on S3, a
// permit scoped to S1 alone. Both subjects are out of scope.
func TestDependencyScopeCoversNeitherEnd(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s2, f.ep3), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))
			err = f.s.CreateDependency(f.ctx, f.permit, d, nil)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateDependency covering neither end = %v, want domain.ErrForbidden", err)
			}
			after := len(mustChangesForDependency(t, f, d.ID))
			if after != before {
				t.Errorf("a refused create wrote %d change_log rows, want %d", after, before)
			}
		})
	}
}

// TestDependencyScopeCoversConsumerOnly: consumer S1 (in scope), provider on
// S3 (out of scope). Checking only the consumer would let this through.
func TestDependencyScopeCoversConsumerOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep3), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))
			err = f.s.CreateDependency(f.ctx, f.permit, d, nil)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateDependency covering only the consumer = %v, want domain.ErrForbidden", err)
			}
			after := len(mustChangesForDependency(t, f, d.ID))
			if after != before {
				t.Errorf("a refused create wrote %d change_log rows, want %d", after, before)
			}
		})
	}
}

// TestDependencyScopeCoversProviderOnly: consumer S2 (out of scope),
// provider on S1 (in scope). Checking only the provider would let this
// through.
func TestDependencyScopeCoversProviderOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s2, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))
			err = f.s.CreateDependency(f.ctx, f.permit, d, nil)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateDependency covering only the provider = %v, want domain.ErrForbidden", err)
			}
			after := len(mustChangesForDependency(t, f, d.ID))
			if after != before {
				t.Errorf("a refused create wrote %d change_log rows, want %d", after, before)
			}
		})
	}
}

// TestDependencyScopeCoversBothEnds: consumer S1, provider on S1. Both
// subjects are in scope, so the write is allowed and audited once.
func TestDependencyScopeCoversBothEnds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, f.permit, d, nil); err != nil {
				t.Fatalf("CreateDependency covering both ends = %v, want nil", err)
			}
			changes := mustChangesForDependency(t, f, d.ID)
			if len(changes) != 1 {
				t.Errorf("an allowed create wrote %d change_log rows, want 1", len(changes))
			}
		})
	}
}

// TestDependencyScopeUpdateMovesTheConsumer: an edge already covered by the
// permit (consumer and provider both on S1) is submitted with a NEW
// consumer, S3, which the permit does not cover. Checking only the
// submitted struct's own id, or only the stored subjects, would miss this;
// UpdateDependency must check both.
func TestDependencyScopeUpdateMovesTheConsumer(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge: %v", err)
			}

			moved := *d
			moved.ConsumerServiceID = f.s3
			if err := f.s.UpdateDependency(f.ctx, f.permit, &moved, nil); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateDependency moving the consumer out of scope = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.ConsumerServiceID != f.s1 {
				t.Error("a refused update still moved the consumer")
			}
		})
	}
}

// TestDependencyScopeUpdateMovesTheProvider is the mirror: the edge starts
// with its provider on S1 and is submitted pointing at a socket on S3.
func TestDependencyScopeUpdateMovesTheProvider(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge: %v", err)
			}

			moved := *d
			ep3 := f.ep3
			moved.ProviderEndpointID = &ep3
			if err := f.s.UpdateDependency(f.ctx, f.permit, &moved, nil); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateDependency moving the provider out of scope = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.ProviderEndpointID == nil || *after.ProviderEndpointID != f.ep1 {
				t.Error("a refused update still moved the provider")
			}
		})
	}
}

// TestDependencyScopeUpdateSeizesAForeignEdge is the case the brief's
// original "moves the consumer"/"moves the provider" pair missed: both of
// those start from a row the caller ALREADY owns, so the SUBMITTED check
// alone refuses them and the STORED check never carries any weight. This is
// the mirror image -- the row belongs to nobody the permit covers (consumer
// S2, provider on S3), and the caller submits subjects it DOES own (S1,
// S1). Without the stored-subject check, the submitted check passes on its
// own, the permit is minted, and somebody else's declared edge is rewritten
// out from under them -- attributed to the hijacker, with a clean diff in
// change_log. See internal/store/deps.go's UpdateDependency comment: this is
// exactly why there are two checks, not one.
func TestDependencyScopeUpdateSeizesAForeignEdge(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s2, f.ep3), f.s.Now())
			if err != nil {
				t.Fatalf("building the foreign edge: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the foreign edge: %v", err)
			}

			seized := *d
			seized.ConsumerServiceID = f.s1
			ep1 := f.ep1
			seized.ProviderEndpointID = &ep1
			if err := f.s.UpdateDependency(f.ctx, f.permit, &seized, nil); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateDependency hijacking a foreign edge = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.ConsumerServiceID != f.s2 {
				t.Errorf("a refused hijack still moved the consumer to %s, want %s", after.ConsumerServiceID, f.s2)
			}
			if after.ProviderEndpointID == nil || *after.ProviderEndpointID != f.ep3 {
				t.Error("a refused hijack still moved the provider")
			}
		})
	}
}

// TestDependencyScopeUpdateBothEndsMineSucceeds is the positive path this
// task actually delivers -- a project owner correcting their own declared
// edge -- and until this test existed nothing in the suite proved it works:
// TestDependencyScopeCoversBothEnds only exercises CreateDependency, and
// every other Update* case above is a refusal. A mutant that made
// UpdateDependency refuse every project owner unconditionally (e.g. always
// running the STORED check against the wrong subject, or dropping
// depPermit and writing under p) could have shipped as a silent, permanent
// 403 with the rest of the suite still green.
func TestDependencyScopeUpdateBothEndsMineSucceeds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			// The create above already wrote its own change_log row against
			// this same dependency id, so the assertion below has to be a
			// delta, not an absolute count.
			before := len(mustChangesForDependency(t, f, d.ID))

			corrected := *d
			corrected.FailureMode = "it stops loudly"
			if err := f.s.UpdateDependency(f.ctx, f.permit, &corrected, nil); err != nil {
				t.Fatalf("UpdateDependency with both stored and submitted ends mine = %v, want nil", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.FailureMode != "it stops loudly" {
				t.Errorf("failure_mode after an allowed update = %q, want %q", after.FailureMode, "it stops loudly")
			}
			gotChanges := len(mustChangesForDependency(t, f, d.ID))
			if gotChanges-before != 1 {
				t.Errorf("an allowed update wrote %d change_log rows, want 1", gotChanges-before)
			}
		})
	}
}

// TestDependencyScopeAdministrator: an AdministratorPermit covers every
// row regardless of subject, the same as it does for every other
// subject-derived type.
func TestDependencyScopeAdministrator(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			admin := domain.AdministratorPermit(
				domain.Actor{ID: "admin-1", Name: "admin-1", Kind: domain.ActorKindUser})
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s2, f.ep3), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, admin, d, nil); err != nil {
				t.Fatalf("CreateDependency as an administrator = %v, want nil", err)
			}
		})
	}
}

// TestDependencyScopeProviderThroughARoute proves resolveProviderService's
// second branch -- route -> frontend endpoint -> service_id -- the same way
// the endpoint branch is proved above, since route carries no service_id
// column of its own and the JOIN is new code this task added.
func TestDependencyScopeProviderThroughARoute(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			routeOnS1 := mustRoute(t, f.s, f.ctx, f.s1, "r1", 9001)
			routeOnS3 := mustRoute(t, f.s, f.ctx, f.s3, "r3", 9003)

			inScope, err := domain.NewDependency(NewID(), domain.DependencySpec{
				ConsumerServiceID: f.s1, ProviderRouteID: &routeOnS1,
				Nature: domain.NatureHard, FailureMode: "it stops",
			}, f.s.Now())
			if err != nil {
				t.Fatalf("building the in-scope dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, f.permit, inScope, nil); err != nil {
				t.Fatalf("CreateDependency through a route on the covered service = %v, want nil", err)
			}

			outOfScope, err := domain.NewDependency(NewID(), domain.DependencySpec{
				ConsumerServiceID: f.s1, ProviderRouteID: &routeOnS3,
				Nature: domain.NatureHard, FailureMode: "it stops",
			}, f.s.Now())
			if err != nil {
				t.Fatalf("building the out-of-scope dependency: %v", err)
			}
			err = f.s.CreateDependency(f.ctx, f.permit, outOfScope, nil)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateDependency through a route on an uncovered service = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestDependencyScopeRetireConsumerMineProviderForeign is the retire-side
// sibling deps_scope_test.go was missing entirely: RetireDependency and
// VerifyDependency both call authorizeDependencySubjects off the STORED
// row, the same as UpdateDependency's stored-subject check, but until this
// test existed nothing exercised either with only ONE end foreign. A
// mutant that replaced either call with domain.AdministratorPermit(p.Actor())
// -- collapsing the two-ended check to "any signed-in caller" -- left the
// whole repo suite green, because every other RetireDependency/
// VerifyDependency call in the suite runs as testPermit (an administrator)
// or (via TestDependencyScopeCoversNeitherEnd's shape) with BOTH ends
// foreign, where the first p.Covers call alone already refuses and the
// second one never carries any weight. Consumer S1 (the permit's own
// scope) is in scope here; the provider socket on S3 is not.
func TestDependencyScopeRetireConsumerMineProviderForeign(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep3), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))

			if err := f.s.RetireDependency(f.ctx, f.permit, d.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireDependency with the consumer mine and the provider foreign = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.Lifecycle == domain.LifecycleRetired {
				t.Error("a refused retire still retired the dependency")
			}
			gotChanges := len(mustChangesForDependency(t, f, d.ID))
			if gotChanges != before {
				t.Errorf("a refused retire wrote %d change_log rows, want %d", gotChanges, before)
			}
		})
	}
}

// TestDependencyScopeRetireConsumerForeignProviderMine is the mirror: the
// consumer is on S2, outside the permit's scope, and only the provider
// socket on S1 belongs to the caller.
func TestDependencyScopeRetireConsumerForeignProviderMine(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s2, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))

			if err := f.s.RetireDependency(f.ctx, f.permit, d.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireDependency with the consumer foreign and the provider mine = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.Lifecycle == domain.LifecycleRetired {
				t.Error("a refused retire still retired the dependency")
			}
			gotChanges := len(mustChangesForDependency(t, f, d.ID))
			if gotChanges != before {
				t.Errorf("a refused retire wrote %d change_log rows, want %d", gotChanges, before)
			}
		})
	}
}

// TestDependencyScopeRetireBothEndsMineSucceeds is the positive path: a
// project owner retiring their own edge, both ends covered. Until this
// test existed nothing in the suite proved a project owner can retire a
// dependency at all -- every other passing RetireDependency call in the
// suite runs as testPermit.
func TestDependencyScopeRetireBothEndsMineSucceeds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))

			if err := f.s.RetireDependency(f.ctx, f.permit, d.ID); err != nil {
				t.Fatalf("RetireDependency with both ends mine = %v, want nil", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle after an allowed retire = %q, want %q", after.Lifecycle, domain.LifecycleRetired)
			}
			gotChanges := len(mustChangesForDependency(t, f, d.ID))
			if gotChanges-before != 1 {
				t.Errorf("an allowed retire wrote %d change_log rows, want 1", gotChanges-before)
			}
		})
	}
}

// TestDependencyScopeRetireAdministrator: an AdministratorPermit covers
// every row regardless of subject.
func TestDependencyScopeRetireAdministrator(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			admin := domain.AdministratorPermit(
				domain.Actor{ID: "admin-1", Name: "admin-1", Kind: domain.ActorKindUser})
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s2, f.ep3), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, admin, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			if err := f.s.RetireDependency(f.ctx, admin, d.ID); err != nil {
				t.Fatalf("RetireDependency as an administrator = %v, want nil", err)
			}
		})
	}
}

// TestDependencyScopeVerifyConsumerMineProviderForeign is VerifyDependency's
// twin of TestDependencyScopeRetireConsumerMineProviderForeign: same
// authorizeDependencySubjects call, same stored-row-only authorization, a
// different mutation target (deps.go's VerifyDependency rather than
// RetireDependency).
func TestDependencyScopeVerifyConsumerMineProviderForeign(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep3), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))

			if err := f.s.VerifyDependency(f.ctx, f.permit, d.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("VerifyDependency with the consumer mine and the provider foreign = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.VerifiedBy != nil {
				t.Error("a refused verify still recorded a verifier")
			}
			gotChanges := len(mustChangesForDependency(t, f, d.ID))
			if gotChanges != before {
				t.Errorf("a refused verify wrote %d change_log rows, want %d", gotChanges, before)
			}
		})
	}
}

// TestDependencyScopeVerifyConsumerForeignProviderMine is the mirror: the
// consumer is on S2, outside the permit's scope, and only the provider
// socket on S1 belongs to the caller.
func TestDependencyScopeVerifyConsumerForeignProviderMine(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s2, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))

			if err := f.s.VerifyDependency(f.ctx, f.permit, d.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("VerifyDependency with the consumer foreign and the provider mine = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.VerifiedBy != nil {
				t.Error("a refused verify still recorded a verifier")
			}
			gotChanges := len(mustChangesForDependency(t, f, d.ID))
			if gotChanges != before {
				t.Errorf("a refused verify wrote %d change_log rows, want %d", gotChanges, before)
			}
		})
	}
}

// TestDependencyScopeVerifyBothEndsMineSucceeds is the positive path: a
// project owner verifying their own edge, both ends covered. Until this
// test existed nothing in the suite proved a project owner can verify a
// dependency at all.
func TestDependencyScopeVerifyBothEndsMineSucceeds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s1, f.ep1), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			before := len(mustChangesForDependency(t, f, d.ID))

			if err := f.s.VerifyDependency(f.ctx, f.permit, d.ID); err != nil {
				t.Fatalf("VerifyDependency with both ends mine = %v, want nil", err)
			}

			after, err := f.s.GetDependency(f.ctx, d.ID)
			if err != nil {
				t.Fatalf("re-reading the edge: %v", err)
			}
			if after.VerifiedBy == nil || *after.VerifiedBy != f.permit.Actor().ID {
				t.Errorf("verified_by after an allowed verify = %v, want %q", after.VerifiedBy, f.permit.Actor().ID)
			}
			gotChanges := len(mustChangesForDependency(t, f, d.ID))
			if gotChanges-before != 1 {
				t.Errorf("an allowed verify wrote %d change_log rows, want 1", gotChanges-before)
			}
		})
	}
}

// TestDependencyScopeVerifyAdministrator: an AdministratorPermit covers
// every row regardless of subject.
func TestDependencyScopeVerifyAdministrator(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newDepScopeFixture(t, e)
			admin := domain.AdministratorPermit(
				domain.Actor{ID: "admin-1", Name: "admin-1", Kind: domain.ActorKindUser})
			d, err := domain.NewDependency(NewID(), newDependencySpec(f.s2, f.ep3), f.s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := f.s.CreateDependency(f.ctx, admin, d, nil); err != nil {
				t.Fatalf("seeding the edge as an administrator: %v", err)
			}
			if err := f.s.VerifyDependency(f.ctx, admin, d.ID); err != nil {
				t.Fatalf("VerifyDependency as an administrator = %v, want nil", err)
			}
		})
	}
}

func mustChangesForDependency(t *testing.T, f *depScopeFixture, id string) []domain.ChangeLog {
	t.Helper()
	changes, err := f.s.ListChangesForEntity(f.ctx, "dependency", id, 10)
	if err != nil {
		t.Fatalf("listing changes for dependency %s: %v", id, err)
	}
	return changes
}
