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
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The expiry report, against a FIXED clock. Every date below is expressed as an
// offset from it, so the test says what it means and does not start failing on
// a particular Tuesday.
var expiryNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

func expiryDate(days int) string { return domain.FormatDate(expiryNow.AddDate(0, 0, days)) }

// setAssetEOL dates an asset, handing its environments back the way any correct
// caller of UpdateAsset must.
func (f *projectFixture) setAssetEOL(t *testing.T, name string, days int) {
	t.Helper()
	row, err := f.s.GetAsset(f.ctx, f.assets[name])
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	a := row.Asset
	d := expiryDate(days)
	a.EOLDate = &d
	envIDs := make([]string, len(row.Environments))
	for i, e := range row.Environments {
		envIDs[i] = e.ID
	}
	if err := f.s.UpdateAsset(f.ctx, testActor, &a, envIDs); err != nil {
		t.Fatalf("dating %s: %v", name, err)
	}
}

func (f *projectFixture) setServiceEOL(t *testing.T, code string, days int) {
	t.Helper()
	row, err := f.s.GetService(f.ctx, f.services[code])
	if err != nil {
		t.Fatalf("reading %s: %v", code, err)
	}
	svc := row.Service
	d := expiryDate(days)
	svc.EOLDate = &d
	if err := f.s.UpdateService(f.ctx, testActor, &svc); err != nil {
		t.Fatalf("dating %s: %v", code, err)
	}
}

// place puts a service instance on a host asset.
func (f *projectFixture) place(t *testing.T, service, host string) {
	t.Helper()
	si, err := domain.NewServiceInstance(NewID(), f.services[service], f.assets[host],
		domain.RuntimeSystemd, 0, f.s.Now())
	if err != nil {
		t.Fatalf("building the instance of %s: %v", service, err)
	}
	if err := f.s.CreateInstance(f.ctx, testActor, si); err != nil {
		t.Fatalf("placing %s on %s: %v", service, host, err)
	}
}

func rowsByName(report *ExpiryReport) map[string]ExpiringRow {
	out := make(map[string]ExpiringRow, len(report.Rows))
	for _, r := range report.Rows {
		key := r.Name
		if r.EntityType == "service" {
			key = r.Code
		}
		out[key] = r
	}
	return out
}

// TestExpiryHorizonKeepsThePastAndCutsTheFuture.
//
// The two halves are one test because they are one rule: a window is a question
// about the future, so something already lapsed is never outside it. Testing
// only the inclusion would pass against a query with no upper bound at all;
// testing only the exclusion would pass against one that dropped the past.
func TestExpiryHorizonKeepsThePastAndCutsTheFuture(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			f.setAssetEOL(t, "sw-core-1", -900) // long past, far outside any window
			f.setAssetEOL(t, "hv-01", 30)       // soon
			f.setAssetEOL(t, "vm-app-1", 400)   // beyond a 12-month horizon
			f.setServiceEOL(t, "orders-api", -5)
			// pgsql-core is left undated, as the control for the undated count.

			report, err := f.s.Expiring(f.ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("running the report: %v", err)
			}
			got := rowsByName(report)

			if _, ok := got["sw-core-1"]; !ok {
				t.Error("a long-expired asset was dropped because it fell outside the window; " +
					"that is how an estate accumulates unsupported hardware nobody looks at")
			}
			if _, ok := got["hv-01"]; !ok {
				t.Error("an asset expiring inside the window is missing")
			}
			if _, ok := got["vm-app-1"]; ok {
				t.Error("an asset expiring beyond the horizon was included; the window means nothing")
			}
			if _, ok := got["orders-api"]; !ok {
				t.Error("an expired service is missing")
			}

			if report.Expired != 2 || report.Soon != 1 {
				t.Errorf("expired=%d soon=%d, want 2 and 1", report.Expired, report.Soon)
			}

			// Widening the horizon must reach the row that was cut, or the
			// control above proved only that the query returns fewer rows.
			wider, err := f.s.Expiring(f.ctx, expiryNow, 24)
			if err != nil {
				t.Fatalf("running the wider report: %v", err)
			}
			if _, ok := rowsByName(wider)["vm-app-1"]; !ok {
				t.Error("widening the horizon to 24 months did not reach a date 400 days out")
			}
		})
	}
}

// The rows are ordered soonest-first across BOTH entity types. Two queries
// merged in Go is exactly where an ordering quietly becomes per-half.
func TestExpiryRowsAreOrderedAcrossBothKinds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			f.setAssetEOL(t, "hv-01", 100)
			f.setServiceEOL(t, "orders-api", -10)
			f.setAssetEOL(t, "sw-core-1", 50)
			f.setServiceEOL(t, "pgsql-core", 20)

			report, err := f.s.Expiring(f.ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("running the report: %v", err)
			}
			want := []string{"orders-api", "pgsql-core", "sw-core-1", "hv-01"}
			if len(report.Rows) != len(want) {
				t.Fatalf("got %d rows, want %d", len(report.Rows), len(want))
			}
			for i, r := range report.Rows {
				name := r.Name
				if r.EntityType == "service" {
					name = r.Code
				}
				if name != want[i] {
					t.Errorf("row %d is %s, want %s (order: %v)", i, name, want[i], want)
				}
			}
		})
	}
}

// TestExpiryCountsWhatRidesOnAnAssetThroughContainment.
//
// A service placed on a GUEST must count for the hypervisor, or every
// expiring host in a virtualised estate reads as carrying nothing. That is the
// difference between the closure table and a parent_id comparison, and it is
// the number the reader decides on.
func TestExpiryCountsWhatRidesOnAnAssetThroughContainment(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			f.place(t, "orders-api", "vm-app-1")
			f.place(t, "pgsql-core", "vm-app-1")

			f.setAssetEOL(t, "hv-01", 30)
			f.setAssetEOL(t, "sw-core-1", 30) // nothing placed on it: the control

			report, err := f.s.Expiring(f.ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("running the report: %v", err)
			}
			got := rowsByName(report)

			hv := got["hv-01"]
			if hv.ServiceCount != 2 {
				t.Errorf("hv-01 carries %d services, want 2 — the guest's workload did not "+
					"reach the host through asset_closure", hv.ServiceCount)
			}
			if hv.BestTier != 2 {
				t.Errorf("hv-01 best tier = %d, want 2", hv.BestTier)
			}
			if sw := got["sw-core-1"]; sw.ServiceCount != 0 {
				t.Errorf("sw-core-1 carries %d services, want 0", sw.ServiceCount)
			}
		})
	}
}

// TestExpiryAttributesToTheOwnerOnly. A project that merely USES a thing is not
// who has to replace it, and naming it here sends the renewal conversation to
// the wrong team.
func TestExpiryAttributesToTheOwnerOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("owning hv-01: %v", err)
			}
			if err := f.link(t, "orders", "hv-01", domain.ProjectUses); err != nil {
				t.Fatalf("using hv-01: %v", err)
			}
			// sw-core-1 is claimed by nobody: unowned has to be a visible
			// answer, not an empty cell that reads like a missing join.
			f.setAssetEOL(t, "hv-01", 30)
			f.setAssetEOL(t, "sw-core-1", 30)

			report, err := f.s.Expiring(f.ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("running the report: %v", err)
			}
			got := rowsByName(report)

			if got["hv-01"].ProjectCode != "platform" {
				t.Errorf("hv-01 attributed to %q, want platform", got["hv-01"].ProjectCode)
			}
			if got["sw-core-1"].ProjectID != "" {
				t.Errorf("an unowned asset was attributed to %q", got["sw-core-1"].ProjectCode)
			}
		})
	}
}

// TestExpiryCountsWhatItCannotSee. The undated count is what makes the rest
// honest: four dated rows in an estate of four hundred reads as "almost nothing
// expires" and the true answer is "almost nothing is recorded".
func TestExpiryCountsWhatItCannotSee(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			f.setAssetEOL(t, "hv-01", 30)
			f.setServiceEOL(t, "orders-api", 30)

			report, err := f.s.Expiring(f.ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("running the report: %v", err)
			}
			// The fixture holds three assets and two services.
			if report.UndatedAssets != 2 {
				t.Errorf("undated assets = %d, want 2", report.UndatedAssets)
			}
			if report.UndatedServices != 1 {
				t.Errorf("undated services = %d, want 1", report.UndatedServices)
			}

			// Retiring a dated asset removes it from the report AND from the
			// undated count. A retired thing does not need replacing.
			if err := f.s.RetireAsset(f.ctx, testActor, f.assets["sw-core-1"]); err != nil {
				t.Fatalf("retiring sw-core-1: %v", err)
			}
			after, err := f.s.Expiring(f.ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("re-running the report: %v", err)
			}
			if after.UndatedAssets != 1 {
				t.Errorf("undated assets after retiring one = %d, want 1", after.UndatedAssets)
			}
		})
	}
}

// A retired asset with a date is not somebody's problem any more.
func TestExpiryIgnoresRetiredRows(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			f.setAssetEOL(t, "sw-core-1", -30)
			f.setServiceEOL(t, "orders-api", -30)
			before, err := f.s.Expiring(f.ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("running the report: %v", err)
			}
			if len(before.Rows) != 2 {
				t.Fatalf("got %d rows before retiring, want 2", len(before.Rows))
			}

			if err := f.s.RetireAsset(f.ctx, testActor, f.assets["sw-core-1"]); err != nil {
				t.Fatalf("retiring the asset: %v", err)
			}
			if err := f.s.RetireService(f.ctx, testActor, f.services["orders-api"]); err != nil {
				t.Fatalf("retiring the service: %v", err)
			}

			after, err := f.s.Expiring(f.ctx, expiryNow, 12)
			if err != nil {
				t.Fatalf("re-running the report: %v", err)
			}
			if len(after.Rows) != 0 {
				t.Errorf("retired rows are still in the report: %+v", after.Rows)
			}
		})
	}
}

// Dating something is a declared change and takes a change_log row like any
// other. It is the assertion that stops eol_date being quietly reclassified as
// telemetry the first time somebody wants a script to set it.
func TestDatingAnAssetIsAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			before := f.changeRows(t, "asset")
			f.setAssetEOL(t, "hv-01", 30)
			if after := f.changeRows(t, "asset"); after != before+1 {
				t.Errorf("change_log rows went %d -> %d, want one more", before, after)
			}

			// And the entry has to say what changed, not merely that something
			// did -- an audit trail that records "updated" answers nothing.
			var diff string
			err := f.s.readOne(f.ctx, &diff,
				`SELECT diff FROM change_log WHERE entity_type = ? AND entity_id = ?
				 ORDER BY at DESC, id DESC`, "asset", f.assets["hv-01"])
			if err != nil {
				t.Fatalf("reading the change_log entry: %v", err)
			}
			if !strings.Contains(diff, "eol_date") || !strings.Contains(diff, expiryDate(30)) {
				t.Errorf("the audit entry does not record the new date: %s", diff)
			}
		})
	}
}

// The database CHECK is the second line of defence, and this proves it is a
// line at all. Go rejects a bad date first, so the constraint is only ever
// reached by something that bypassed the constructor -- an import, a fixture, a
// psql session. Both engines must refuse it the same way, which is the whole
// reason the expression is built from length() and substr() rather than a
// regular expression neither agrees on.
func TestTheDatabaseRefusesAMalformedEOLDate(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			for _, bad := range []string{"30/06/2027", "2027-6-3", "2027-06-30T00:00:00Z", "soon"} {
				_, err := f.s.db.Writer.Exec(f.s.db.Writer.Rebind(
					`UPDATE asset SET eol_date = ? WHERE id = ?`), bad, f.assets["hv-01"])
				if err == nil {
					t.Errorf("the database accepted %q as an EOL date", bad)
				}
			}
			// The control: a well-formed date still goes in, so the test above
			// is not passing because every UPDATE fails.
			if _, err := f.s.db.Writer.Exec(f.s.db.Writer.Rebind(
				`UPDATE asset SET eol_date = ? WHERE id = ?`),
				"2027-06-30", f.assets["hv-01"]); err != nil {
				t.Errorf("the database refused a well-formed date: %v", err)
			}
		})
	}
}
