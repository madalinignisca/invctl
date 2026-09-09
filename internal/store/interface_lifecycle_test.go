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
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// A port can be withdrawn (migration 00062), and the whole design rests on one
// invariant: A RETIRED PORT IS ALWAYS BARE.
//
// `interface` is the most-referenced table in this schema -- nine foreign-key
// columns across eight tables point at it, and 58 queries read it. If retiring
// could leave a cable, an address or a VLAN membership behind, every one of
// those read sites would need its own judgement about whether to exclude
// retired rows, and a wrong default is silent: a removed port still deriving a
// reachability edge, or still offered in a picker. Refusing while anything is
// attached is what makes them correct unchanged.

// TestARetiredInterfaceIsAlwaysBare drives the refusal through a real cable.
func TestARetiredInterfaceIsAlwaysBare(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a := mustAsset(t, s, ctx, domain.KindSwitch, "sw-bare", nil)
			b := mustAsset(t, s, ctx, domain.KindServer, "srv-bare", nil)
			ifA := mustInterface(t, s, ctx, a, "eth0")
			ifB := mustInterface(t, s, ctx, b, "eth0")

			l, err := domain.NewLink(NewID(), ifA, ifB)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testPermit, l); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			// Patched: refused.
			err = s.RetireInterface(ctx, testPermit, ifA)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a patched port = %v, want ErrConflict. Without the "+
					"refusal a retired port could still carry a cable, and every query "+
					"that trusts otherwise is wrong in the direction that looks fine", err)
			}
			if got := lifecycleOf(t, s, ctx, ifA); got != domain.LifecycleActive {
				t.Errorf("the refused retire still changed lifecycle to %q", got)
			}

			// Unpatched: allowed.
			if err := s.RetireLink(ctx, testPermit, l.ID); err != nil {
				t.Fatalf("unpatching: %v", err)
			}
			if err := s.RetireInterface(ctx, testPermit, ifA); err != nil {
				t.Fatalf("retiring a bare port: %v", err)
			}
			if got := lifecycleOf(t, s, ctx, ifA); got != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q after retiring a bare port, want retired", got)
			}
		})
	}
}

// TestRetiringAPortTwiceLogsOneWithdrawal. A second audit entry would claim a
// withdrawal that did not happen -- RetireIPRange and RetireProvider both
// guard this and it is easy to leave out.
func TestRetiringAPortTwiceLogsOneWithdrawal(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a := mustAsset(t, s, ctx, domain.KindSwitch, "sw-twice", nil)
			id := mustInterface(t, s, ctx, a, "eth9")

			if err := s.RetireInterface(ctx, testPermit, id); err != nil {
				t.Fatalf("first retire: %v", err)
			}
			before := countChangeLog(t, s, ctx, id)
			if err := s.RetireInterface(ctx, testPermit, id); err != nil {
				t.Fatalf("second retire: %v", err)
			}
			if after := countChangeLog(t, s, ctx, id); after != before {
				t.Errorf("a second retire wrote another change_log row (%d -> %d), "+
					"claiming a withdrawal that did not happen", before, after)
			}
		})
	}
}

// TestReaddingARetiredPortReactivatesIt is the NIC swap, and the reason
// withdrawing a port is not a trap.
//
// The unique constraint on (asset_id, name) is a table constraint, so a retired
// `eth0` keeps the name. Without reactivation nobody could ever put `eth0`
// back, which would be worse than having no retire at all.
func TestReaddingARetiredPortReactivatesIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a := mustAsset(t, s, ctx, domain.KindServer, "srv-swap", nil)
			original := mustInterface(t, s, ctx, a, "eth0")
			if err := s.RetireInterface(ctx, testPermit, original); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			// The replacement NIC: same name, different form factor.
			replacement, err := domain.NewInterface(NewID(), a, "eth0", "sfp28")
			if err != nil {
				t.Fatalf("building the replacement: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, replacement); err != nil {
				t.Fatalf("re-adding eth0 after a NIC swap: %v", err)
			}

			if replacement.ID != original {
				t.Errorf("the port came back as a new row (%s, was %s). It must come "+
					"back as ITSELF -- a second row claiming to be the same physical "+
					"port splits its history and leaves every past cable and address "+
					"pointing at the wrong one", replacement.ID, original)
			}
			if got := lifecycleOf(t, s, ctx, original); got != domain.LifecycleActive {
				t.Errorf("lifecycle = %q after re-adding, want active", got)
			}
			// The replacement's own attributes took.
			back, err := s.GetInterface(ctx, original)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if back.FormFactor != "sfp28" {
				t.Errorf("form_factor = %q, want sfp28 -- a replacement part's own "+
					"attributes must apply", back.FormFactor)
			}
		})
	}
}

// TestAddingAPortThatCollidesWithALivePortIsStillRefused. Reactivation applies
// to RETIRED rows only; a duplicate live name is the error it always was.
func TestAddingAPortThatCollidesWithALivePortIsStillRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a := mustAsset(t, s, ctx, domain.KindServer, "srv-dup", nil)
			mustInterface(t, s, ctx, a, "eth0")

			dup, err := domain.NewInterface(NewID(), a, "eth0", "rj45")
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateInterface(ctx, testPermit, dup); err == nil {
				t.Error("a second live eth0 was accepted on the same asset")
			}
		})
	}
}

// TestUpdateInterfaceCannotLogAWithdrawalThatDidNotHappen guards the audit,
// which is what `i.Lifecycle = before.Lifecycle` actually protects.
//
// THE FIRST VERSION OF THIS TEST ASSERTED THE WRONG PROPERTY, and the mutation
// said so: it checked that a submitted lifecycle does not reach the database,
// which it cannot, because UpdateInterface's UPDATE does not name that column.
// Deleting the pin left the test green.
//
// THE SAME MISTAKE AS TestUpdateProviderCannotLogAWithdrawalThatDidNotHappen,
// made again on the same day. Both times the plausible property (the row) is
// protected by the SQL, and the real one (the diff) is protected by the pin:
// without it, logUpdate records lifecycle retired while the row stays active --
// an audit trail saying somebody withdrew a port nobody withdrew, on a port
// that still carries a cable.
func TestUpdateInterfaceCannotLogAWithdrawalThatDidNotHappen(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a := mustAsset(t, s, ctx, domain.KindSwitch, "sw-pin", nil)
			b := mustAsset(t, s, ctx, domain.KindServer, "srv-pin", nil)
			id := mustInterface(t, s, ctx, a, "eth0")
			other := mustInterface(t, s, ctx, b, "eth0")
			l, err := domain.NewLink(NewID(), id, other)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testPermit, l); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			iface, err := s.GetInterface(ctx, id)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			// A REAL change alongside the forged one, so an update is genuinely
			// logged and there is a diff to inspect. Submitting only the
			// lifecycle produces no diff at all once the pin is in place --
			// correct behaviour, and it would leave this asserting over nothing.
			forged := *iface
			forged.Name = "eth0-renamed"
			forged.Lifecycle = domain.LifecycleRetired
			if err := s.UpdateInterface(ctx, testPermit, &forged); err != nil {
				t.Fatalf("UpdateInterface with a submitted lifecycle: %v", err)
			}
			if got := lifecycleOf(t, s, ctx, id); got != domain.LifecycleActive {
				t.Errorf("the row is %q, want active", got)
			}

			var diffs []string
			if err := s.DB().Reader.Select(&diffs, s.DB().Reader.Rebind(
				`SELECT COALESCE(diff, '') FROM change_log
				  WHERE entity_id = ? AND action = 'update'`), id); err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("no update was logged at all, so this test is checking nothing")
			}
			for _, d := range diffs {
				if strings.Contains(d, "lifecycle") {
					t.Errorf("change_log records a lifecycle change for a port that is "+
						"still active: %s\nThe audit trail now says somebody withdrew a "+
						"port nobody withdrew -- one that still carries a cable.", d)
				}
			}
		})
	}
}

// TestTheAttachmentListMatchesTheSchema is the census that keeps
// interfaceAttachments honest.
//
// Nine columns across eight tables reference interface(id) today. A new one
// added later without an entry would let a port retire with that thing still
// attached, breaking the invariant every read site depends on -- silently. So
// the list is checked against the migrations rather than maintained by hand.
func TestTheAttachmentListMatchesTheSchema(t *testing.T) {
	root := repoRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "internal", "store", "migrations", "sqlite", "*.sql"))
	if err != nil {
		t.Fatalf("globbing migrations: %v", err)
	}
	if len(paths) < 40 {
		t.Fatalf("found only %d migrations; the scan has stopped matching the tree", len(paths))
	}

	type ref struct{ table, column string }
	inSchema := map[ref]bool{}
	table := regexp.MustCompile(`^\s*CREATE TABLE (\w+)`)
	column := regexp.MustCompile(`^\s*(\w+)\s`)
	for _, path := range paths {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		current := ""
		for _, line := range strings.Split(string(raw), "\n") {
			if m := table.FindStringSubmatch(line); m != nil {
				// _new/_old are the rebuild dance of migrations 00004/00005;
				// the live table is the name they rename to.
				current = strings.TrimSuffix(strings.TrimSuffix(m[1], "_new"), "_old")
			}
			if !strings.Contains(line, "REFERENCES interface(id)") {
				continue
			}
			if c := column.FindStringSubmatch(line); c != nil && current != "" {
				inSchema[ref{current, c[1]}] = true
			}
		}
	}

	listed := map[ref]bool{}
	for _, a := range interfaceAttachments {
		listed[ref{a.table, a.column}] = true
	}
	for r := range inSchema {
		if !listed[r] {
			t.Errorf("%s.%s references interface(id) and is NOT in "+
				"interfaceAttachments, so a port can be withdrawn while it still "+
				"holds one. Every query that trusts a retired port to be bare is "+
				"then wrong, and nothing says so.", r.table, r.column)
		}
	}
	for r := range listed {
		if !inSchema[r] {
			t.Errorf("interfaceAttachments lists %s.%s, which no longer references "+
				"interface(id). Delete the entry -- RetireInterface runs a query "+
				"per entry and this one is checking a column that is not there.",
				r.table, r.column)
		}
	}
	// Every listed table must also appear in the lifecycle map, or
	// attachmentHolds silently treats it as having none.
	for _, a := range interfaceAttachments {
		if _, ok := tableHasLifecycle[a.table]; !ok {
			t.Errorf("%s is an attachment table with no entry in tableHasLifecycle, "+
				"so a WITHDRAWN row there would still hold its port", a.table)
		}
	}
}

func lifecycleOf(t *testing.T, s *SQLStore, ctx context.Context, id string) string {
	t.Helper()
	i, err := s.GetInterface(ctx, id)
	if err != nil {
		t.Fatalf("reading interface %s: %v", id, err)
	}
	return i.Lifecycle
}

func countChangeLog(t *testing.T, s *SQLStore, ctx context.Context, id string) int {
	t.Helper()
	var n int
	if err := s.DB().Reader.Get(&n, s.DB().Reader.Rebind(
		`SELECT COUNT(*) FROM change_log WHERE entity_id = ?`), id); err != nil {
		t.Fatalf("counting change_log: %v", err)
	}
	return n
}

// TestNothingCanBeAttachedToARetiredPort is the other half of the invariant,
// and the easier half to forget.
//
// RetireInterface refuses while anything is attached. Without the symmetric
// check, an operator could retire a bare port and then patch a cable straight
// into it -- arriving at precisely the state the refusal exists to prevent,
// from the direction nobody was watching. Both halves are needed for "a retired
// port is bare" to hold, and 58 read sites are trusting it.
func TestNothingCanBeAttachedToARetiredPort(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a := mustAsset(t, s, ctx, domain.KindSwitch, "sw-closed", nil)
			b := mustAsset(t, s, ctx, domain.KindServer, "srv-closed", nil)
			dead := mustInterface(t, s, ctx, a, "eth0")
			live := mustInterface(t, s, ctx, b, "eth0")
			if err := s.RetireInterface(ctx, testPermit, dead); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			t.Run("a cable", func(t *testing.T) {
				l, err := domain.NewLink(NewID(), dead, live)
				if err != nil {
					t.Fatalf("building link: %v", err)
				}
				if err := s.CreateLink(ctx, testPermit, l); err == nil {
					t.Error("a cable was patched into a withdrawn port, so a retired " +
						"port now carries one and every query trusting otherwise is wrong")
				}
			})

			// The pickers must not offer it either -- an option that can only be
			// refused is the offered-and-refused defect this codebase closes
			// wherever it finds it.
			t.Run("the cable picker does not offer it", func(t *testing.T) {
				opts, err := s.ListAvailableInterfaces(ctx, "")
				if err != nil {
					t.Fatalf("listing: %v", err)
				}
				for _, o := range opts {
					if o.ID == dead {
						t.Error("ListAvailableInterfaces offered a withdrawn port")
					}
				}
			})

			t.Run("the VLAN picker does not offer it", func(t *testing.T) {
				opts, err := s.ListPortOptions(ctx)
				if err != nil {
					t.Fatalf("listing: %v", err)
				}
				for _, o := range opts {
					if o.ID == dead {
						t.Error("ListPortOptions offered a withdrawn port")
					}
				}
			})
		})
	}
}
