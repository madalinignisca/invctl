// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestRotationFindings covers the three findings and, as importantly, the two
// non-findings: an unmanaged credential produces nothing, and a within-window
// one produces nothing.
func TestRotationFindings(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			now := f.s.Now()

			// Neither of these is a finding: no rule to break, and a rule that
			// is being met.
			f.identity(t, "metrics-scrape", 0)
			current := f.identity(t, "svc-current", 90)
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, current.ID,
				domain.FormatDate(now.AddDate(0, 0, -10))); err != nil {
				t.Fatalf("rotating svc-current: %v", err)
			}

			// A rule the estate set for itself and has not met for 110 days.
			overdue := f.identity(t, "svc-overdue", 90)
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, overdue.ID,
				domain.FormatDate(now.AddDate(0, 0, -200))); err != nil {
				t.Fatalf("rotating svc-overdue: %v", err)
			}

			// A rule with no evidence it has ever been followed.
			f.identity(t, "svc-unrecorded", 90)

			// A withdrawn credential a live edge still names.
			withdrawn := f.identity(t, "svc-withdrawn", 90)
			f.dependency(t, withdrawn.ID)
			if err := f.s.RetireIdentity(f.ctx, testPermit, withdrawn.ID); err != nil {
				t.Fatalf("withdrawing svc-withdrawn: %v", err)
			}

			findings, err := f.s.RotationFindings(f.ctx)
			if err != nil {
				t.Fatalf("RotationFindings: %v", err)
			}

			byLabel := map[string]Finding{}
			for _, x := range findings {
				if _, dup := byLabel[x.Label]; dup {
					t.Errorf("two findings share the label %q. One row per KIND is the "+
						"rule -- forty overdue credentials is one decision.", x.Label)
				}
				byLabel[x.Label] = x
			}
			if len(findings) != 3 {
				t.Fatalf("got %d findings, want 3 (overdue, never recorded, withdrawn "+
					"but still named): %+v", len(findings), findings)
			}

			for _, tc := range []struct {
				what         string
				wantSeverity string
				wantCount    int
				wantExample  string
				why          string
			}{
				{
					what: "past its own rotation rule", wantSeverity: FindingFault,
					wantCount: 1, wantExample: "svc-overdue",
					why: "the estate's own declared rule says 90 days and it has been 200. " +
						"Something is wrong NOW -- the same shape as a contract having " +
						"lapsed, which findings.go names as the archetypal Fault.",
				},
				{
					what: "no rotation ever recorded", wantSeverity: FindingGap,
					wantCount: 1, wantExample: "svc-unrecorded",
					why: "the inventory does not know when this was last rotated, so it " +
						"cannot say whether the rule is met. Calling it a Fault would " +
						"claim knowledge nobody has; Gap is the severity that makes the " +
						"other two trustworthy.",
				},
				{
					what: "withdrawn credential", wantSeverity: FindingGap,
					wantCount: 1, wantExample: "svc-withdrawn",
					why: "the inventory contradicts itself: either the edge is stale or " +
						"the service is authenticating with a withdrawn credential, and " +
						"it is not knowable from here.",
				},
			} {
				t.Run(tc.what, func(t *testing.T) {
					var got Finding
					var found bool
					for label, x := range byLabel {
						if strings.Contains(label, tc.what) ||
							strings.Contains(x.Detail, tc.wantExample) {
							got, found = x, true
							break
						}
					}
					if !found {
						t.Fatalf("no finding for %q. %s", tc.what, tc.why)
					}
					if got.Severity != tc.wantSeverity {
						t.Errorf("severity = %q, want %q. %s",
							got.Severity, tc.wantSeverity, tc.why)
					}
					if got.Count != tc.wantCount {
						t.Errorf("count = %d, want %d", got.Count, tc.wantCount)
					}
					if !strings.Contains(got.Detail, tc.wantExample) {
						t.Errorf("detail = %q and does not name %q. One row per kind is "+
							"exactly why the row has to carry a concrete example, or it "+
							"is only a number.", got.Detail, tc.wantExample)
					}
					if got.Href == "" {
						t.Error("the finding has no Href, so the dashboard row is a dead " +
							"end and the reader has to go and find the credential by hand")
					}
				})
			}

			// The two non-findings, asserted rather than assumed: neither the
			// unmanaged credential nor the current one may appear anywhere.
			for _, x := range findings {
				for _, quiet := range []string{"metrics-scrape", "svc-current"} {
					if strings.Contains(x.Detail, quiet) {
						t.Errorf("finding %q names %q. A credential nobody intended to "+
							"rotate is not a problem, and one that is inside its window "+
							"is the rule being MET.", x.Label, quiet)
					}
				}
			}
		})
	}
}

// TestAnUnmanagedCredentialIsNotAFinding, on its own, because it is the
// judgement most likely to be "improved" by somebody later. "A credential nobody
// intended to rotate is not a problem, and flagging every cert_subject and human
// row would swamp the page" -- the reasoning EstateFindings already applies to
// expected power convergence and template_drift applies to extra components.
func TestAnUnmanagedCredentialIsNotAFinding(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			now := f.s.Now()

			// An estate of credentials nobody asked to have rotated, plus one
			// that is rotated and current. Neither is a problem.
			f.identity(t, "metrics-scrape", 0)
			f.identity(t, "cert-subject-web", 0)
			f.identity(t, "human-oncall", 0)
			current := f.identity(t, "svc-current", 90)
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, current.ID,
				domain.FormatDate(now.AddDate(0, 0, -10))); err != nil {
				t.Fatalf("rotating svc-current: %v", err)
			}

			findings, err := f.s.RotationFindings(f.ctx)
			if err != nil {
				t.Fatalf("RotationFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("an estate with three unmanaged credentials and one current one "+
					"produced %d findings: %+v.\nA credential nobody intended to rotate "+
					"is not a problem. Flagging every cert_subject and human row would "+
					"swamp the page and teach people to ignore it -- the reasoning "+
					"EstateFindings already applies to expected power convergence and "+
					"template_drift applies to extra components. It renders as `no "+
					"policy` on the list, which is enough.", len(findings), findings)
			}

			// Now one credential with a rule and no evidence it was followed.
			// EXACTLY ONE finding must appear, and it must be the never-recorded
			// one -- which is also what proves the three unmanaged rows are
			// still contributing nothing rather than having been folded in.
			f.identity(t, "svc-unrecorded", 90)
			findings, err = f.s.RotationFindings(f.ctx)
			if err != nil {
				t.Fatalf("RotationFindings after adding an unrecorded credential: %v", err)
			}
			if len(findings) != 1 {
				t.Fatalf("got %d findings, want exactly 1: %+v", len(findings), findings)
			}
			got := findings[0]
			if got.Count != 1 {
				t.Errorf("finding count = %d, want 1 -- the three unmanaged credentials "+
					"have been folded into this finding", got.Count)
			}
			if got.Severity != FindingGap {
				t.Errorf("severity = %q, want %q. Calling it a Fault would claim knowledge "+
					"nobody has: it might have been rotated last week by somebody who did "+
					"not write it down. Gap is the severity that makes the other two "+
					"trustworthy.", got.Severity, FindingGap)
			}
			if !strings.Contains(got.Detail, "svc-unrecorded") {
				t.Errorf("detail = %q and names no example. One row per KIND of finding is "+
					"the rule, which is exactly why the row has to carry a concrete "+
					"example or it is only a number.", got.Detail)
			}
		})
	}
}
