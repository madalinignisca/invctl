// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "testing"

// TestBackendPoolValidateIsReachableWithoutTheConstructor is Identity.
// Validate's own test, restated for BackendPool: a value that already exists
// and is then corrupted the way UpdateBackendPool's caller would hand it
// back, with NewBackendPool never in the loop.
func TestBackendPoolValidateIsReachableWithoutTheConstructor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(p *BackendPool)
		field string
	}{
		{"a blank name", func(p *BackendPool) { p.Name = "" }, "name"},
		{"whitespace for a name", func(p *BackendPool) { p.Name = "   " }, "name"},
		{"a blank service", func(p *BackendPool) { p.ServiceID = "" }, "service_id"},
		{"an unknown lifecycle", func(p *BackendPool) { p.Lifecycle = "gone" }, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &BackendPool{
				ID: "pool-1", ServiceID: "svc-1", Name: "web-backends",
				Lifecycle: LifecycleActive, RowVersion: 2,
			}
			tc.spoil(p)

			err := p.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value; UpdateBackendPool would write it", tc.name)
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()[tc.field]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), tc.field)
			}
		})
	}
}

// TestRouteValidateIsReachableWithoutTheConstructor is the same test, for
// Route: a value that already exists and is then corrupted the way
// UpdateRoute's caller would hand it back, with NewRoute never in the loop.
func TestRouteValidateIsReachableWithoutTheConstructor(t *testing.T) {
	tlsTerminate := "terminate"
	for _, tc := range []struct {
		name  string
		spoil func(r *Route)
		field string
	}{
		{"a blank frontend", func(r *Route) { r.FrontendEndpointID = "" }, "frontend_endpoint_id"},
		{"a blank pool", func(r *Route) { r.BackendPoolID = "" }, "backend_pool_id"},
		{"an unknown match type", func(r *Route) { r.MatchType = "regex" }, "match_type"},
		{"an unknown tls termination", func(r *Route) {
			bogus := "obfuscate"
			r.TLSTermination = &bogus
		}, "tls_termination"},
		{"an unknown lifecycle", func(r *Route) { r.Lifecycle = "gone" }, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Route{
				ID: "route-1", FrontendEndpointID: "ep-1", MatchType: "host_header",
				BackendPoolID: "pool-1", TLSTermination: &tlsTerminate, Priority: 100,
				Lifecycle: LifecycleActive, RowVersion: 3,
			}
			tc.spoil(r)

			err := r.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value; UpdateRoute would write it", tc.name)
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()[tc.field]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), tc.field)
			}
		})
	}
}
