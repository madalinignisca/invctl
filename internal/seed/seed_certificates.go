// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// ---------- TLS certificates ----------
//
// The estate's most urgent findings live here, because a certificate lapses on
// a ninety-day cycle rather than a five-year one. Each of the five below makes
// one point the rest of the fixture cannot:
//
//   the wildcard          one certificate, four deployments -- an expiry that is
//                         four outages, and the row that shows why "where is it
//                         used" is worth recording at all
//   the partner mTLS      expires in eleven days and NOBODY is recorded as
//                         renewing it. The edge it sits on is also unowned by
//                         any project and looked after by no team, so the
//                         estate's sharpest finding now has three faces
//   the expired internal  lapsed six weeks ago and is still deployed, which is
//                         what an internal CA with no automation looks like
//   the vault cert        renewed by the team that runs the service, the
//                         ordinary case, present so the alarming ones are not
//                         the only ones
//   the undated one       no expiry recorded at all -- the report calls this out
//                         separately because every certificate has an expiry, so
//                         a blank means nobody looked
//
// Dates are relative to the seeding clock, so the demo stays true whenever it is
// loaded. Key references are PATHS; no key material exists in this fixture and
// none may ever be added to it.

type certSpec struct {
	subject    string
	issuer     string
	sans       []string
	fromDays   int
	untilDays  int // 0 means no recorded expiry
	keyRef     string
	team       string
	role       string
	onAssets   []string
	onServices []string
}

func (b *builder) certificates() {
	if !b.ok() {
		return
	}

	specs := []certSpec{
		{
			subject: "*.example.com", issuer: "Let's Encrypt R3",
			sans:     []string{"example.com", "www.example.com", "orders.example.com"},
			fromDays: -68, untilDays: 22,
			keyRef: "vault:secret/tls/wildcard-example",
			team:   "network", role: "operator",
			// Four deployments: the edge that terminates it, the two k8s nodes
			// behind it, and the storefront service that depends on the name.
			onAssets:   []string{"vm-proxy-1", "vm-k8s-1", "vm-k8s-2"},
			onServices: []string{"orders-web"},
		},
		{
			// Eleven days, and nobody is recorded as renewing it. The partner
			// gateway depends on the route this terminates, so its expiry is a
			// partner-facing outage with no team to escalate to.
			subject: "partners.example.com", issuer: "DigiCert Global G2",
			sans:     []string{},
			fromDays: -354, untilDays: 11,
			keyRef:     "vault:secret/tls/partners",
			onAssets:   []string{"vm-proxy-1"},
			onServices: []string{"partner-gateway"},
		},
		{
			// Lapsed six weeks ago and still deployed. An internal CA with no
			// automation is exactly how this happens, and it is invisible until
			// somebody looks at a page like this one.
			subject: "vault.internal", issuer: "Example Internal CA",
			sans:     []string{"vault-1.internal", "vault-2.internal", "vault-3.internal"},
			fromDays: -430, untilDays: -44,
			keyRef: "vault:secret/tls/vault-internal",
			team:   "platform", role: "operator",
			onAssets: []string{"vm-vault-1", "vm-vault-2", "vm-vault-3"},
		},
		{
			// The ordinary case: in date, owned, renewed by the team that runs
			// the service. Present so the alarming rows are not the only rows.
			subject: "sso.example.com", issuer: "Let's Encrypt R3",
			sans:     []string{},
			fromDays: -40, untilDays: 50,
			keyRef: "vault:secret/tls/sso",
			team:   "platform", role: "operator",
			onServices: []string{"sso"},
		},
		{
			// No expiry recorded. Every certificate has one, so this is not a
			// certificate that never expires -- it is one nobody looked at.
			subject: "mimir.internal", issuer: "Example Internal CA",
			sans:     []string{},
			fromDays: -200, untilDays: 0,
			team: "observability", role: "operator",
			onServices: []string{"mimir-ingester"},
		},
	}

	for _, spec := range specs {
		if !b.ok() {
			return
		}
		def := domain.CertificateSpec{
			SubjectCN: spec.subject,
			Issuer:    str(spec.issuer),
			SANs:      spec.sans,
			Lifecycle: domain.LifecycleActive,
		}
		if spec.fromDays != 0 {
			from := domain.FormatDate(b.now.AddDate(0, 0, spec.fromDays))
			def.NotBefore = &from
		}
		if spec.untilDays != 0 {
			until := domain.FormatDate(b.now.AddDate(0, 0, spec.untilDays))
			def.NotAfter = &until
		}
		if spec.keyRef != "" {
			def.KeyRef = str(spec.keyRef)
		}
		if spec.team != "" {
			def.TeamID = b.team(spec.team)
			def.ManagerRole = str(spec.role)
		}

		c, err := domain.NewCertificate(store.NewID(), def, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building certificate %s: %w", spec.subject, err))
			return
		}
		if err := b.store.CreateCertificate(b.ctx, Permit, c); err != nil {
			b.fail(fmt.Errorf("seeding certificate %s: %w", spec.subject, err))
			return
		}

		for _, name := range spec.onAssets {
			id, ok := b.refs.Assets[name]
			if !ok {
				b.fail(fmt.Errorf("deploying %s: unknown asset %s", spec.subject, name))
				return
			}
			if err := b.store.DeployCertificateToAsset(b.ctx, Permit, c.ID, id, nil); err != nil {
				b.fail(fmt.Errorf("deploying %s to %s: %w", spec.subject, name, err))
				return
			}
		}
		for _, code := range spec.onServices {
			id, ok := b.refs.Services[code]
			if !ok {
				b.fail(fmt.Errorf("deploying %s: unknown service %s", spec.subject, code))
				return
			}
			if err := b.store.DeployCertificateToService(b.ctx, Permit, c.ID, id, nil); err != nil {
				b.fail(fmt.Errorf("deploying %s to %s: %w", spec.subject, code, err))
				return
			}
		}
	}
}
