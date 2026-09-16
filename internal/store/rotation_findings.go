// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/madalinignisca/invctl/internal/domain"
)

// derefOrInt normalises an optional int the way derefOr normalises an
// optional string, for the same reason: a finding's Detail has to name a
// concrete rule, and a nil rotation_days should never be reachable here (see
// RotationFindings' own header on why RotationUnmanaged is never counted),
// but printing "?" rather than panicking on a nil pointer costs nothing and
// keeps this file from being the place a nil-deref bug surfaces.
func derefOrInt(n *int) any {
	if n == nil {
		return "?"
	}
	return *n
}

// RotationFindings says what the rotation policy currently says about this
// estate.
//
// IT DERIVES, IT DOES NOT DECIDE. Every count here comes from
// ListIdentities -- the same store method the /identities page uses -- and
// folds in Go through domain.Identity.RotationStatus, so a finding cannot
// appear here and not there, or say something different in the two places.
// That is the failure mode a summary invites, and findings.go's own header
// names it.
//
// FOLDED IN GO RATHER THAN QUERIED, and not only for that reason: the state
// depends on today's date (CLAUDE.md forbids a clock in SQL) and on whether
// the stored value PARSES, which no portable SQL expression can decide. It
// also keeps last_rotated out of every SQL literal outside
// RecordIdentityRotation, which is what
// internal/store/last_rotated_source_test.go asserts.
//
// rotation_days IS NULL IS NOT A FINDING. A credential nobody intended to
// rotate is not a problem, and flagging every cert_subject and human row
// would swamp the page -- "teach people to ignore the page", the reasoning
// EstateFindings already applies to expected power convergence. It renders as
// `no policy` on the list, which is enough.
//
// NO "DUE SOON" BAND. It would need a horizon constant nobody has asked for,
// and the list already sorts by days remaining. Add it when somebody asks,
// with ExpiryHorizonMonths as the precedent for where the number lives.
func (s *SQLStore) RotationFindings(ctx context.Context) ([]Finding, error) {
	rows, err := s.ListIdentities(ctx, IdentityFilter{IncludeRetired: true})
	if err != nil {
		return nil, fmt.Errorf("gathering rotation findings: %w", err)
	}
	now := s.now()

	var overdue, never int
	var firstOverdue, firstNever string
	for _, r := range rows {
		if r.Lifecycle == domain.LifecycleRetired {
			continue
		}
		switch r.RotationStatus(now) {
		case domain.RotationOverdue:
			overdue++
			if firstOverdue == "" {
				firstOverdue = fmt.Sprintf("%s was last rotated %s, against a %v-day rule",
					r.Name, derefOr(r.LastRotated, "?"), derefOrInt(r.RotationDays))
			}
		case domain.RotationNeverRecorded:
			never++
			if firstNever == "" {
				firstNever = fmt.Sprintf("%s has a %v-day rule and no rotation has ever been recorded",
					r.Name, derefOrInt(r.RotationDays))
			}
		case domain.RotationUnreadable:
			// Folded into `never`: the inventory cannot say when this was last
			// rotated, which is the same claim and the same severity. Counting
			// it as overdue would assert a lapse from a value nobody can read.
			never++
			if firstNever == "" {
				firstNever = r.Name + " has a rotation date that will not parse"
			}
		case domain.RotationUnmanaged, domain.RotationWithinWindow:
			// No finding. RotationUnmanaged is the "no policy" non-finding this
			// file's header argues at length; RotationWithinWindow is the rule
			// being MET. Named explicitly rather than left to a bare `default`,
			// the same reason findings.go gives for FHRPRedundant: a default
			// would make this silence look like an oversight and would swallow a
			// sixth state nobody had considered.
		}
	}

	var out []Finding
	if overdue > 0 {
		// FAULT: the estate's own declared rule says 90 days and it has been
		// 200. Something is wrong NOW -- the same shape as "a contract has
		// lapsed", which findings.go's severity comment names as the archetypal
		// Fault.
		//
		// Href IS THE FILTERED LIST, not the one example -- "/identities" is
		// what /identities?rotation=overdue exists for, and the dashboard's
		// own "For example" column already carries Detail's single credential;
		// linking Href to that same one would strand the other eleven behind
		// a page nobody reaches. domain.RotationStates is the source for the
		// query value, so this cannot drift from what the list's own <select>
		// accepts.
		out = append(out, Finding{Severity: FindingFault, Count: overdue,
			Label: "credential past its own rotation rule", Detail: firstOverdue,
			Href: "/identities?rotation=" + string(domain.RotationOverdue)})
	}
	if never > 0 {
		// GAP, not Fault, and this is the call that makes the other two
		// trustworthy. The inventory does not know when this was last rotated,
		// so it cannot say whether the rule is met; calling it a Fault would
		// claim knowledge nobody has -- it might have been rotated last week by
		// somebody who did not write it down. "A report that cannot say 'I do
		// not know' is a report that guesses."
		out = append(out, Finding{Severity: FindingGap, Count: never,
			Label:  "credential with a rotation rule and no rotation ever recorded",
			Detail: firstNever, Href: "/identities?rotation=" + string(domain.RotationNeverRecorded)})
	}

	// The third: a LIVE dependency naming a RETIRED credential. GAP, for the
	// call template_drift made for the same reason -- the inventory contradicts
	// itself: either the edge is stale or the service is authenticating with a
	// withdrawn credential, and it is not knowable from here. The
	// counter-argument, that a withdrawn credential still in use is sharper than
	// a missing port template and should be a Fault, is real; it is recorded
	// here rather than left for the next person to re-make.
	var stale []struct {
		IdentityID   string `db:"identity_id"`
		IdentityName string `db:"identity_name"`
		ConsumerCode string `db:"consumer_code"`
	}
	if err := s.read(ctx, &stale, `
		SELECT i.id AS identity_id, i.name AS identity_name,
		       COALESCE(cs.code, '') AS consumer_code
		FROM dependency d
		JOIN identity i ON i.id = d.identity_id
		LEFT JOIN service cs ON cs.id = d.consumer_service_id
		WHERE d.lifecycle <> ? AND i.lifecycle = ?`,
		domain.LifecycleRetired, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("gathering withdrawn-credential findings: %w", err)
	}
	if len(stale) > 0 {
		// Go, for collation -- and the first row becomes the finding's example
		// and its Href, so an unstable order would make the dashboard link
		// somewhere different on each render.
		sort.SliceStable(stale, func(a, b int) bool {
			x, y := stale[a], stale[b]
			if x.ConsumerCode != y.ConsumerCode {
				return x.ConsumerCode < y.ConsumerCode
			}
			if x.IdentityName != y.IdentityName {
				return x.IdentityName < y.IdentityName
			}
			return x.IdentityID < y.IdentityID
		})
		out = append(out, Finding{Severity: FindingGap, Count: len(stale),
			Label:  "live dependency naming a withdrawn credential",
			Detail: fmt.Sprintf("%s still authenticates as %s", stale[0].ConsumerCode, stale[0].IdentityName),
			Href:   "/identities/" + stale[0].IdentityID})
	}
	return out, nil
}
