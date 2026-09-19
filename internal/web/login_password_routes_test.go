// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/config"
)

// The login page must offer every password route that POST /login can
// actually authenticate, and no others.
//
// These are one property tested three ways, because the property is an
// agreement between two files that have no compile-time link: the template
// gates the form on ShowPassword, and cmd/invctl/main.go's buildAuthenticator
// decides independently which authenticators POST /login chains. When
// ShowPassword was AuthLocal alone, they disagreed in both directions at once
// -- an LDAP-only deployment rendered no way in at all, and an OIDC+LDAP
// deployment hid a form whose handler still worked.
func TestLoginPageOffersExactlyThePasswordRoutesThatWork(t *testing.T) {
	const passwordField = `name="password"`

	cases := []struct {
		name            string
		local, ldap     bool
		oidc            bool
		wantPasswordBox bool
		why             string
	}{
		{
			name: "ldap only", local: false, ldap: true, oidc: false,
			wantPasswordBox: true,
			why: "LDAP is the only authenticator and it takes a password. " +
				"With no form and no Keycloak button the page offers no way in at " +
				"all, while POST /login would have bound perfectly well -- a lockout " +
				"with a working back end. config.validate() accepts this exact " +
				"configuration, so it is not hypothetical.",
		},
		{
			name: "oidc with ldap deliberately left on", local: false, ldap: true, oidc: true,
			wantPasswordBox: true,
			why: "An operator who sets INV_AUTH_LDAP=true alongside an issuer has " +
				"chosen a password route deliberately, exactly as INV_AUTH_LOCAL=true " +
				"would be. Hiding the form does not close it -- POST /login still " +
				"binds against LDAP -- it only stops the login page telling the truth " +
				"about which routes are open.",
		},
		{
			name: "oidc only", local: false, ldap: false, oidc: true,
			wantPasswordBox: false,
			why: "Spec D3. No password authenticator is chained, so a form here " +
				"would be a control that cannot work, and the whole point of the " +
				"default is that Keycloak's MFA is the only way in.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issuer := newFakeOIDCIssuer(t)
			h := newOIDCHarness(t, issuer, tc.local, func(c *config.Config) {
				c.AuthLDAP = tc.ldap
				c.AuthOIDC = tc.oidc
			})

			b := body2(t, h.get("/login"))
			got := strings.Contains(b, passwordField)

			if got != tc.wantPasswordBox {
				t.Errorf("password form present = %v, want %v.\n%s", got, tc.wantPasswordBox, tc.why)
			}
			if tc.oidc && !strings.Contains(b, "/auth/oidc") {
				t.Error("login page does not offer the Keycloak sign-in link")
			}
			if !tc.oidc && strings.Contains(b, "/auth/oidc") {
				t.Error("login page offers a Keycloak link with no issuer configured")
			}
		})
	}
}
