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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// user_project: which projects a person is assigned to (WP-G1 piece 3, task
// 11). Nothing in the running application consults this table yet -- these
// tests exercise the store methods directly, the same as every other task in
// this work package before its gate flips.

// mustProjectForAssignment creates a project with the given code, for tests
// that only need a project to point an assignment at and nothing about its
// estate.
func mustProjectForAssignment(t *testing.T, s *SQLStore, ctx context.Context, code string) string {
	t.Helper()
	p, err := domain.NewProject(NewID(), domain.ProjectSpec{Code: code, Name: code}, s.Now())
	if err != nil {
		t.Fatalf("building project %s: %v", code, err)
	}
	if err := s.CreateProject(ctx, testPermit, p); err != nil {
		t.Fatalf("creating project %s: %v", code, err)
	}
	return p.ID
}

// TestAssigningAProjectWritesAnAuditRowAgainstTheUser.
//
// Mutation: delete the t.logCreate call from AssignProject -- this must fail.
func TestAssigningAProjectWritesAnAuditRowAgainstTheUser(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := mustUserWithRole(t, s, ctx, "admin", domain.RoleAdministrator)
			subject := mustUserWithRole(t, s, ctx, "alice", domain.RoleObserver)
			projectID := mustProjectForAssignment(t, s, ctx, "orders")

			permit := domain.AdministratorPermit(domain.UserActor(admin))
			if err := s.AssignProject(ctx, permit, subject.ID, projectID); err != nil {
				t.Fatalf("assigning project: %v", err)
			}

			n, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND action = ?`,
				"user_project", domain.ActionCreate)
			if err != nil {
				t.Fatalf("counting change_log rows: %v", err)
			}
			if n != 1 {
				t.Fatalf("change_log create rows for user_project = %d, want 1", n)
			}

			var row domain.ChangeLog
			if err := s.db.Reader.GetContext(ctx, &row, s.db.Reader.Rebind(
				`SELECT * FROM change_log WHERE entity_type = ? AND action = ?`),
				"user_project", domain.ActionCreate); err != nil {
				t.Fatalf("loading the change_log row: %v", err)
			}
			if row.Actor != admin.ID {
				t.Errorf("actor = %q, want the granting admin's opaque id %q", row.Actor, admin.ID)
			}
			if row.ActorKind != domain.ActorKindUser {
				t.Errorf("actor_kind = %q, want %q", row.ActorKind, domain.ActorKindUser)
			}
		})
	}
}

// TestReleasingAProjectRetiresTheRowAndDoesNotDeleteIt.
//
// Mutation: change ReleaseProject's UPDATE to a DELETE FROM -- this must fail.
func TestReleasingAProjectRetiresTheRowAndDoesNotDeleteIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			subject := mustUserWithRole(t, s, ctx, "alice", domain.RoleObserver)
			projectID := mustProjectForAssignment(t, s, ctx, "orders")

			if err := s.AssignProject(ctx, testPermit, subject.ID, projectID); err != nil {
				t.Fatalf("assigning project: %v", err)
			}
			if err := s.ReleaseProject(ctx, testPermit, subject.ID, projectID); err != nil {
				t.Fatalf("releasing project: %v", err)
			}

			var lifecycle string
			err := s.db.Reader.GetContext(ctx, &lifecycle, s.db.Reader.Rebind(
				`SELECT lifecycle FROM user_project WHERE user_id = ? AND project_id = ?`),
				subject.ID, projectID)
			if err != nil {
				t.Fatalf("the row is gone -- ReleaseProject must retire it, never delete it: %v", err)
			}
			if lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q, want %q", lifecycle, domain.LifecycleRetired)
			}
		})
	}
}

// TestAReleasedAssignmentCanBeMadeAgain proves the partial unique index: a
// total UNIQUE on (user_id, project_id) would refuse the second assignment
// and an operator could never re-add somebody they had removed.
//
// Mutation: make the unique index total (drop the WHERE lifecycle = 'active'
// clause) -- this must fail.
func TestAReleasedAssignmentCanBeMadeAgain(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			subject := mustUserWithRole(t, s, ctx, "alice", domain.RoleObserver)
			projectID := mustProjectForAssignment(t, s, ctx, "orders")

			if err := s.AssignProject(ctx, testPermit, subject.ID, projectID); err != nil {
				t.Fatalf("first assignment: %v", err)
			}
			if err := s.ReleaseProject(ctx, testPermit, subject.ID, projectID); err != nil {
				t.Fatalf("releasing: %v", err)
			}
			if err := s.AssignProject(ctx, testPermit, subject.ID, projectID); err != nil {
				t.Fatalf("re-assigning after release must succeed: %v", err)
			}

			projects, err := s.ProjectsForUser(ctx, subject.ID)
			if err != nil {
				t.Fatalf("loading projects for user: %v", err)
			}
			if len(projects) != 1 || projects[0] != projectID {
				t.Fatalf("ProjectsForUser = %v, want [%s]", projects, projectID)
			}
		})
	}
}

// TestProjectsForCircuitsReturnsTheSameShapeAsAssetsAndServices.
func TestProjectsForCircuitsReturnsTheSameShapeAsAssetsAndServices(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// Built directly rather than through newProjectFixture: this test
			// cares about ProjectsForCircuits' shape, not the wider project
			// fixture's asset/service graph.
			projectID := mustProjectForAssignment(t, s, ctx, "circuit-shape-project")
			providerID := mustProvider(t, s, ctx, "provider-for-circuits-shape")
			circuitID := mustCircuit(t, s, ctx, providerID, "cid-1", nil)

			link, err := domain.NewProjectCircuitLink(projectID, circuitID, domain.ProjectOwns, nil, s.Now())
			if err != nil {
				t.Fatalf("building project circuit link: %v", err)
			}
			if err := s.LinkProjectCircuit(ctx, testPermit, link); err != nil {
				t.Fatalf("linking circuit to project: %v", err)
			}

			out, err := s.ProjectsForCircuits(ctx, []string{circuitID})
			if err != nil {
				t.Fatalf("ProjectsForCircuits: %v", err)
			}
			rows, ok := out[circuitID]
			if !ok || len(rows) != 1 {
				t.Fatalf("ProjectsForCircuits[%s] = %v, want exactly one row", circuitID, rows)
			}
			row := rows[0]
			if row.EntityID != circuitID {
				t.Errorf("EntityID = %q, want %q", row.EntityID, circuitID)
			}
			if row.ProjectID != projectID {
				t.Errorf("ProjectID = %q, want %q", row.ProjectID, projectID)
			}
			if row.Relation != domain.ProjectOwns {
				t.Errorf("Relation = %q, want %q", row.Relation, domain.ProjectOwns)
			}

			// Same map shape ProjectsForAssets/ProjectsForServices already
			// produce: an id absent from the argument list is simply absent
			// from the map, not present with an empty slice.
			if _, present := out["not-a-real-circuit-id"]; present {
				t.Errorf("map contains an entry for an id that was never asked about")
			}
		})
	}
}

// TestProjectsForUserExcludesRetiredAssignmentsAndRetiredProjects.
//
// Both halves matter -- see ProjectsForUser's doc comment. This test asserts
// both independently so that fixing one does not hide a break in the other.
//
// Mutation A: drop the `up.lifecycle = ?` filter -- this must fail.
// Mutation B: drop the `p.lifecycle <> ?` filter -- this must fail.
func TestProjectsForUserExcludesRetiredAssignmentsAndRetiredProjects(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			subject := mustUserWithRole(t, s, ctx, "alice", domain.RoleObserver)

			live := mustProjectForAssignment(t, s, ctx, "live-project")
			retiredAssignment := mustProjectForAssignment(t, s, ctx, "retired-assignment-project")
			retiredProject := mustProjectForAssignment(t, s, ctx, "retired-project")

			for _, pid := range []string{live, retiredAssignment, retiredProject} {
				if err := s.AssignProject(ctx, testPermit, subject.ID, pid); err != nil {
					t.Fatalf("assigning %s: %v", pid, err)
				}
			}

			// Half one: an assignment released, but the project stays active.
			if err := s.ReleaseProject(ctx, testPermit, subject.ID, retiredAssignment); err != nil {
				t.Fatalf("releasing assignment: %v", err)
			}

			// Half two: the assignment stays active, but the project itself
			// is retired.
			if err := s.RetireProject(ctx, testPermit, retiredProject); err != nil {
				t.Fatalf("retiring project: %v", err)
			}

			got, err := s.ProjectsForUser(ctx, subject.ID)
			if err != nil {
				t.Fatalf("ProjectsForUser: %v", err)
			}
			if len(got) != 1 || got[0] != live {
				t.Fatalf("ProjectsForUser = %v, want exactly [%s]", got, live)
			}
		})
	}
}
