// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestAppUserCarriesTheOIDCColumns proves migration 00072 landed: before it,
// the CHECK constraint rejects source='oidc' and the subject column does not
// exist, so this insert is the whole test.
func TestAppUserCarriesTheOIDCColumns(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			id := NewID()
			if _, err := s.DB().Writer.Exec(s.DB().Writer.Rebind(
				`INSERT INTO app_user (id, username, source, subject, is_active, created_at)
				 VALUES (?, ?, 'oidc', ?, TRUE, ?)`),
				id, "alice", "kc-sub-1", domain.FormatTime(s.Now())); err != nil {
				t.Fatalf("inserting an oidc user: %v", err)
			}
			_ = ctx
		})
	}
}

// TestTwoAccountsCannotShareASubject pins down the partial unique index: it is
// what makes account takeover by subject collision impossible rather than
// merely unlikely (spec D2).
func TestTwoAccountsCannotShareASubject(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, _ := newStore(t, e)
			at := domain.FormatTime(s.Now())
			ins := func(username, subject string) error {
				_, err := s.DB().Writer.Exec(s.DB().Writer.Rebind(
					`INSERT INTO app_user (id, username, source, subject, is_active, created_at)
					 VALUES (?, ?, 'oidc', ?, TRUE, ?)`), NewID(), username, subject, at)
				return err
			}
			if err := ins("alice", "kc-sub-1"); err != nil {
				t.Fatalf("first insert: %v", err)
			}
			if err := ins("bob", "kc-sub-1"); err == nil {
				t.Fatal("two accounts shared one subject. The partial unique index is " +
					"what makes account takeover by subject collision impossible rather " +
					"than merely unlikely (spec D2).")
			}
		})
	}
}

// TestSubjectIsNullableForLocalAndLDAP proves the unique index is partial: a
// plain unique index over NULLs would make a second local/LDAP account
// impossible to create at all.
func TestSubjectIsNullableForLocalAndLDAP(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, _ := newStore(t, e)
			at := domain.FormatTime(s.Now())
			for i, src := range []string{"local", "ldap"} {
				if _, err := s.DB().Writer.Exec(s.DB().Writer.Rebind(
					`INSERT INTO app_user (id, username, source, is_active, created_at)
					 VALUES (?, ?, ?, TRUE, ?)`),
					NewID(), fmt.Sprintf("u%d", i), src, at); err != nil {
					t.Fatalf("%s user with no subject: %v", src, err)
				}
			}
			// Two NULL subjects must not collide: a partial unique index over
			// NULLs would make a second password user impossible to create.
		})
	}
}
