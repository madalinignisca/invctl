// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"fmt"
	"sort"
	"testing"
)

// The fixture has to DEMONSTRATE the component template, not merely contain
// one -- seed_engine_test.go's argument, applied to WP-C1 and WP-B4.

// TestTheCoreSwitchModelDeclaresTheEstatesOwnPortNames.
//
// THIS IS THE TEST FOR THE MISTAKE THAT WAS ACTUALLY MADE. WP-C1 shipped
// "Ethernet1/[1-48]" as its worked example -- correct for a chassis switch,
// wrong for this model, whose ports are Ethernet1..Ethernet48. Seeded that
// way the template would declare fifty-five ports on a forty-eight-port
// switch, match not one real port, and report every asset of the model as
// both missing everything and carrying everything.
//
// Asserting the exact name set rather than the count is what makes that
// loud: forty-nine of the wrong names is still forty-nine.
func TestTheCoreSwitchModelDeclaresTheEstatesOwnPortNames(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		id, ok := f.refs.DeviceTypes["DCS-7050SX3-48YC8"]
		if !ok {
			t.Fatal("the fixture has no DCS-7050SX3-48YC8")
		}
		components, err := f.store.ListDeviceTypeComponents(f.ctx, id)
		if err != nil {
			t.Fatalf("listing the model's template: %v", err)
		}
		got := make([]string, len(components))
		for i, c := range components {
			got[i] = c.Name
		}
		want := make([]string, 0, 49)
		for i := 1; i <= 48; i++ {
			want = append(want, fmt.Sprintf("Ethernet%d", i))
		}
		want = append(want, "Management1")

		sorted := append([]string(nil), got...)
		sort.Strings(sorted)
		sort.Strings(want)
		if len(sorted) != len(want) {
			t.Fatalf("the template declares %d ports, want %d: %v", len(sorted), len(want), got)
		}
		for i := range want {
			if sorted[i] != want[i] {
				t.Fatalf("the template declares %q where the estate's switches have %q; "+
					"a template whose names do not match the model it describes reports "+
					"every asset of that model as wrong in both directions",
					sorted[i], want[i])
			}
		}

		// position is assigned from expansion order and CONTINUES across the
		// four batches rather than restarting -- which is what makes
		// ListDeviceTypeComponents return Ethernet2 before Ethernet10 instead
		// of after it. A lexical sort of these names does not.
		if got[0] != "Ethernet1" || got[1] != "Ethernet2" || got[len(got)-1] != "Management1" {
			t.Errorf("the template lists %q, %q .. %q; want port order, not lexical order. "+
				"Four batches restarting position at 0 would interleave them",
				got[0], got[1], got[len(got)-1])
		}
	})
}

// TestTheHandRecordedSwitchesDriftFromTheirOwnModel.
//
// THE ORDERING IN Load IS WHAT THIS ASSERTS, and it is invisible in the
// fixture's data. componentTemplates() runs AFTER networking(): the template
// is instantiated onto an asset at creation and never reaches back, so
// declaring it after the two core switches were recorded port by port leaves
// them drifting -- which is the state the finding reports and ApplyTemplate
// repairs. Move the phase earlier and both switches are silently filled in,
// the finding disappears, and the demo has nothing to show. Nothing else in
// the suite would notice.
func TestTheHandRecordedSwitchesDriftFromTheirOwnModel(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		drift, err := f.store.TemplateDriftFindings(f.ctx)
		if err != nil {
			t.Fatalf("gathering template drift: %v", err)
		}
		if len(drift) != 1 {
			t.Fatalf("the fixture produces %d drift findings, want exactly one; the two "+
				"core switches are meant to be missing the ports their model declares",
				len(drift))
		}
		if drift[0].Count != 2 {
			t.Errorf("%d asset(s) drift from their device type, want 2 (sw-core-1 and "+
				"sw-core-2). Detail: %s", drift[0].Count, drift[0].Detail)
		}

		// THE OTHER DIRECTION, and it is the half that catches a wrong
		// template rather than a stale asset. Every port these switches
		// actually have is one the template names, so this finding must stay
		// silent. It firing means the template and the estate disagree about
		// what a port is called -- the "Ethernet1/1" failure, which would
		// otherwise look like a richer demo rather than a broken one.
		extra, err := f.store.TemplateExtraFindings(f.ctx)
		if err != nil {
			t.Fatalf("gathering template extras: %v", err)
		}
		if len(extra) != 0 {
			t.Errorf("the fixture reports %d asset(s) carrying a port their template does "+
				"not declare: %s. In the base estate every real port IS declared, so this "+
				"firing means the template's names no longer match the estate's",
				extra[0].Count, extra[0].Detail)
		}
	})
}
