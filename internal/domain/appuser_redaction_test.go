// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"reflect"
	"testing"
)

// appUserColumnsThatAreNotPersonal is the argued other half of the census
// below: every `db`-tagged column of AppUser is either redacted from the audit
// trail or listed here with a reason it does not need to be.
//
// Add a column to AppUser and this test fails until you have made that
// decision. That is the entire point. `subject` is the worked example of why
// it is worth having: the Keycloak work DID redact it (RedactedFields, global,
// migration 00072) — but nothing in the suite would have failed if it had not,
// because every redaction test here names one field and a new field is named
// by none of them.
var appUserColumnsThatAreNotPersonal = map[string]string{
	"id": "invctl's own opaque id. It is what change_log.actor already holds " +
		"BY DESIGN, and it resolves to a person only through this database — " +
		"the one an erasure request scrubs.",
	"source": "which authenticator vouched for the account: local, ldap or " +
		"oidc. A category with three possible values, not a fact about a person.",
	"is_active":     "whether the account may sign in. A state, and one an auditor needs.",
	"role":          "the grant itself. Recording who was made an administrator and when is the reason this trail exists.",
	"can_see_costs": "a grant, same as role.",
	"created_at":    "when the account was made.",
	"last_login_at": "observed telemetry, excluded from the audited comparison entirely (see updateOIDCUser and TouchLogin).",
}

// TestEveryAppUserColumnIsEitherRedactedOrArgued is a census, not a spot check.
//
// The audit trail's whole retention argument is that it carries no personal
// data: `change_log.actor` holds an opaque id, so the log can be kept forever
// with no retention policy, and scrubbing an app_user answers an erasure
// request while the log keeps its integrity (CLAUDE.md, docs/AUDIT.md).
//
// That argument is only true if EVERY personal column of AppUser is redacted
// before it reaches a change_log payload — and "every" is a claim no
// field-by-field test can make. Each existing redaction test asserts one named
// column; a column nobody thought of is asserted by none of them and the suite
// stays green. A census fails on what nobody thought to write a test for,
// which is the only class of bug that gets this far.
func TestEveryAppUserColumnIsEitherRedactedOrArgued(t *testing.T) {
	typ := reflect.TypeOf(AppUser{})
	for i := 0; i < typ.NumField(); i++ {
		column := typ.Field(i).Tag.Get("db")
		if column == "" || column == "-" {
			continue
		}
		redacted := IsRedacted("AppUser", column)
		_, argued := appUserColumnsThatAreNotPersonal[column]

		switch {
		case redacted && argued:
			t.Errorf("app_user.%s is both redacted and listed as not personal. "+
				"Pick one: the exemption list is for columns that may appear in "+
				"change_log in the clear.", column)
		case !redacted && !argued:
			t.Errorf("app_user.%s reaches change_log in the clear and nothing says "+
				"that is intended.\n"+
				"change_log is append-only and kept forever on the promise that it "+
				"holds no personal data. Either add %q to "+
				"RedactedFieldsByEntity[\"AppUser\"], or add it to "+
				"appUserColumnsThatAreNotPersonal with the reason it is safe.",
				column, column)
		}
	}
}

// TestAppUserRedactionCensusCoversEveryListedColumn is the other direction: a
// column dropped from AppUser must not leave a stale entry behind, or the
// census above starts passing on the strength of an exemption for something
// that no longer exists.
func TestAppUserRedactionCensusCoversEveryListedColumn(t *testing.T) {
	live := map[string]bool{}
	typ := reflect.TypeOf(AppUser{})
	for i := 0; i < typ.NumField(); i++ {
		if column := typ.Field(i).Tag.Get("db"); column != "" && column != "-" {
			live[column] = true
		}
	}
	for column := range appUserColumnsThatAreNotPersonal {
		if !live[column] {
			t.Errorf("appUserColumnsThatAreNotPersonal names app_user.%s, which "+
				"AppUser no longer has. Remove the entry.", column)
		}
	}
	for column := range RedactedFieldsByEntity["AppUser"] {
		if !live[column] {
			t.Errorf("RedactedFieldsByEntity[\"AppUser\"] names %s, which AppUser "+
				"no longer has. Remove the entry.", column)
		}
	}
}
