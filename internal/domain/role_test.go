// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"os"
	"regexp"
	"testing"
	"time"
)

// TestANewAccountIsAnObserverUntilSomebodySaysOtherwise pins the security
// decision in migration 00058's comment: every account, including one an
// unauthenticated LDAP first bind upserts, starts read-only. NewAppUser must
// never take a role parameter -- see the doc comment on NewAppUser -- so this
// is the only place that default can be asserted.
func TestANewAccountIsAnObserverUntilSomebodySaysOtherwise(t *testing.T) {
	u, err := NewAppUser("id-1", "alice", UserSourceLocal, time.Now())
	if err != nil {
		t.Fatalf("NewAppUser: %v", err)
	}
	if u.Role != RoleObserver {
		t.Fatalf("Role = %q, want %q", u.Role, RoleObserver)
	}
}

// TestARoleOutsideTheConstantSetIsRefused exercises values that are not in
// Roles, including two that are near-misses of a real value -- a
// capitalisation and a space -- because those are exactly the mistakes a
// human typing a role by hand would make, and the message must say what was
// given and what is allowed.
func TestARoleOutsideTheConstantSetIsRefused(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		wantMsg string
	}{
		{
			name:    "admin is not a role",
			role:    "admin",
			wantMsg: "must be one of administrator, observer, project_owner",
		},
		{
			name:    "wrong case is not a role",
			role:    "Administrator",
			wantMsg: "must be one of administrator, observer, project_owner",
		},
		{
			name:    "empty is not a role",
			role:    "",
			wantMsg: "must be one of administrator, observer, project_owner",
		},
		{
			name:    "a space is not an underscore",
			role:    "project owner",
			wantMsg: "must be one of administrator, observer, project_owner",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &AppUser{ID: "id-1", Username: "alice", Source: UserSourceLocal, Role: tt.role}
			err := u.ValidateRole()
			if err == nil {
				t.Fatalf("ValidateRole(%q) = nil, want an error", tt.role)
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("ValidateRole(%q) error is not a *ValidationError: %v", tt.role, err)
			}
			got := ve.Messages()["role"]
			if got != tt.wantMsg {
				t.Fatalf("ValidateRole(%q) message = %q, want %q", tt.role, got, tt.wantMsg)
			}
		})
	}
}

// TestTheGoConstantSetMatchesTheDatabaseCheck reads the CHECK clause out of
// the migration file on disk and asserts it names exactly the values in
// Roles -- no more, no fewer. Two sources of truth for an enum drift
// silently: the drift is only discovered when a legitimate value is refused
// in production, or worse, when an illegitimate one is accepted. This test is
// cheap because the file is right there; it does not need a database.
func TestTheGoConstantSetMatchesTheDatabaseCheck(t *testing.T) {
	const migrationPath = "../store/migrations/sqlite/00058_user_role.sql"
	raw, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("reading %s: %v", migrationPath, err)
	}

	re := regexp.MustCompile(`CHECK \(role IN \(([^)]*)\)\)`)
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no `CHECK (role IN (...))` clause found in %s", migrationPath)
	}

	quoted := regexp.MustCompile(`'([^']*)'`).FindAllSubmatch(m[1], -1)
	if quoted == nil {
		t.Fatalf("CHECK clause in %s has no quoted values: %q", migrationPath, m[1])
	}
	fromMigration := make(map[string]bool, len(quoted))
	for _, q := range quoted {
		fromMigration[string(q[1])] = true
	}

	fromGo := make(map[string]bool, len(Roles))
	for _, r := range Roles {
		fromGo[r] = true
	}

	for v := range fromMigration {
		if !fromGo[v] {
			t.Errorf("migration CHECK names %q, but domain.Roles does not", v)
		}
	}
	for v := range fromGo {
		if !fromMigration[v] {
			t.Errorf("domain.Roles names %q, but the migration CHECK does not", v)
		}
	}
}
