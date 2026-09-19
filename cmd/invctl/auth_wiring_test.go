// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/config"
)

// TestBuildAuthenticatorChainsExactlyThePasswordRoutesTheLoginPageOffers.
//
// This is the other half of internal/web's login-page test, and until it
// existed that test was checking a claim it could not see. It asserted that
// the page shows a password form when AuthLocal || AuthLDAP -- while the
// thing that decides whether POST /login can authenticate anybody lives here,
// in package main, with no test of any kind. Chain the local authenticator
// unconditionally and the page test stays green while D3's actual property is
// gone.
//
// So the invariant is stated here, against the real function: the chain is
// non-empty for exactly the configurations the page shows a form for. The two
// tests are deliberately written from the same expression so that a change to
// one policy and not the other cannot pass both.
func TestBuildAuthenticatorChainsExactlyThePasswordRoutesTheLoginPageOffers(t *testing.T) {
	cases := []struct {
		name      string
		cfg       config.Config
		wantNames []string
	}{
		{
			name:      "local only, the historical default",
			cfg:       config.Config{AuthLocal: true},
			wantNames: []string{"local"},
		},
		{
			name:      "ldap only, which validate() accepts",
			cfg:       config.Config{AuthLDAP: true},
			wantNames: []string{"ldap"},
		},
		{
			name:      "local and ldap together",
			cfg:       config.Config{AuthLocal: true, AuthLDAP: true},
			wantNames: []string{"local", "ldap"},
		},
		{
			// Spec D3. Nothing to chain: OIDC is not an auth.Authenticator,
			// it never sees a password, and POST /login must therefore have
			// nothing to try.
			name:      "oidc only",
			cfg:       config.Config{AuthOIDC: true},
			wantNames: nil,
		},
		{
			name:      "oidc with ldap deliberately left on",
			cfg:       config.Config{AuthOIDC: true, AuthLDAP: true},
			wantNames: []string{"ldap"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A nil store is safe here and deliberate: this asserts WIRING,
			// and nothing below calls Authenticate. A store would only make
			// the test able to fail for reasons that are not this policy.
			chain, err := buildAuthenticator(nil, &tc.cfg)
			if err != nil {
				t.Fatalf("buildAuthenticator: %v", err)
			}
			got := chain.Authenticators()

			if len(got) != len(tc.wantNames) {
				t.Fatalf("chained %v, want %v", got, tc.wantNames)
			}
			for i := range got {
				if got[i] != tc.wantNames[i] {
					t.Fatalf("chained %v, want %v", got, tc.wantNames)
				}
			}

			// The agreement itself, spelled the same way the login page
			// spells it (App.passwordLoginEnabled).
			pageWouldShowAForm := tc.cfg.AuthLocal || tc.cfg.AuthLDAP
			routeCanAuthenticate := len(got) > 0
			if pageWouldShowAForm != routeCanAuthenticate {
				t.Errorf("the login page would show a password form = %v, but POST /login "+
					"can authenticate = %v.\nThese are one policy in two files. A form with "+
					"nothing behind it is a dead control; a working route with no form is a "+
					"password path around Keycloak's MFA that the operator cannot see.",
					pageWouldShowAForm, routeCanAuthenticate)
			}
		})
	}
}
