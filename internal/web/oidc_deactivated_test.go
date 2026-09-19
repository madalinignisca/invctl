// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/madalinignisca/invctl/internal/store"
)

// TestOIDCCallbackRefusesADeactivatedAccount.
//
// Deactivating an account is what an administrator does INSTEAD of scrubbing
// it when somebody should simply stop being able to sign in -- the manual
// says so in as many words ("Deactivating stops somebody signing in").
//
// middleware.Authenticate would drop the session on the very next request, so
// without this the person gets a session that can reach nothing. That is why
// this is about the audit trail as much as about access: a sign-in that did
// not succeed was being logged as EventSignInSucceeded, while LDAP logs a
// failure for the identical situation (internal/auth/ldap.go). Two
// authenticators reporting opposite outcomes for the same event is worse for
// whoever is reading the log than either answer would be on its own.
//
// Asserted at the callback's own status rather than by chasing the log line:
// the refusal is what makes the log line right, and a 401 here is a claim the
// middleware cannot quietly satisfy on this path's behalf.
func TestOIDCCallbackRefusesADeactivatedAccount(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	h := newOIDCHarness(t, issuer, false)

	// First sign-in: creates the account.
	state, nonce := h.startOIDC()
	code := issuer.issueCode(issuer.token(t, defaultOIDCClaims(issuer.issuer, nonce, "kc-sub-deact")))
	resp := h.get(fmt.Sprintf("/auth/oidc/callback?state=%s&code=%s",
		url.QueryEscape(state), url.QueryEscape(code)))
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("first callback = %d, want a redirect; the account was never created", resp.StatusCode)
	}

	// An administrator deactivates them. Written straight to the column the
	// handler reads, so this test fails for one reason only.
	db, err := store.Open(store.DriverSQLite, "file:"+h.dsn)
	if err != nil {
		t.Fatalf("reopening the test database: %v", err)
	}
	defer db.Close()
	if _, err := db.SQLDB().Exec(`UPDATE app_user SET is_active = FALSE WHERE subject = ?`,
		"kc-sub-deact"); err != nil {
		t.Fatalf("deactivating the account: %v", err)
	}

	// Same person, same Keycloak, a fresh and entirely valid token.
	state2, nonce2 := h.startOIDC()
	code2 := issuer.issueCode(issuer.token(t, defaultOIDCClaims(issuer.issuer, nonce2, "kc-sub-deact")))
	resp2 := h.get(fmt.Sprintf("/auth/oidc/callback?state=%s&code=%s",
		url.QueryEscape(state2), url.QueryEscape(code2)))

	if resp2.StatusCode == http.StatusSeeOther || resp2.StatusCode == http.StatusFound {
		t.Fatalf("callback = %d: a deactivated account completed sign-in and was redirected "+
			"as though it had succeeded. Keycloak still vouches for who they are -- it has "+
			"no idea invctl deactivated them -- so this is the only place that can say no.",
			resp2.StatusCode)
	}
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("callback = %d, want %d: the refusal should look like every other "+
			"refused sign-in on this route", resp2.StatusCode, http.StatusUnauthorized)
	}
}
