// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Bridges routes.go's unexported route table and CSRF exemption builder to
// the external web_test package, so a test can walk them directly instead of
// trusting the diff -- without exporting either from the package's real API.
package web

import "github.com/madalinignisca/invctl/internal/web/middleware"

// RegisteredAPIPatternsForTest returns the full net/http.ServeMux pattern
// every entry in apiRoutes would register, built by the same apiPattern
// function Routes uses to mount them. A test asserting "no pattern here is
// anything but GET" is asserting apiPattern's own hard-coded prefix holds --
// which is what makes the property true by construction.
func RegisteredAPIPatternsForTest() []string {
	patterns := make([]string, 0, len(apiRoutes))
	for _, route := range apiRoutes {
		patterns = append(patterns, apiPattern(route))
	}
	return patterns
}

// CSRFExemptionsForTest exposes csrfExemptions to the external test package.
func CSRFExemptionsForTest(agents *AgentSurface) []middleware.ExactPath {
	return csrfExemptions(agents)
}
