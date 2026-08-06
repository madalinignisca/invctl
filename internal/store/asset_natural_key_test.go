// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The asset natural key: a name is unique among an asset's LIVE siblings.
//
// This is the first natural key assets have ever had, and it exists so bulk
// import can name one without knowing our UUIDs. Everything below is therefore
// a statement about what an import file is allowed to mean, not only about what
// a form will accept.

// fieldError returns the message attached to one field, or "" when the error is
// not a field failure on it.
//
// Asserting on the FIELD and not merely that something failed. Both halves of
// this rule refuse a duplicate -- the Go pre-check and the partial index -- but
// only one of them produces something a form can render: the index's refusal
// arrives as ErrConflict and a bare 409, losing everything typed. A test that
// accepts "an error happened" cannot tell those apart, and mutation testing
// caught exactly that here: deleting the reparent pre-check left the suite green
// because the index still refused.
func fieldError(err error, field string) string {
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		return ""
	}
	for _, f := range ve.Fields {
		if f.Field == field {
			return f.Message
		}
	}
	return ""
}

func TestAnAssetNameIsUniqueAmongItsLiveSiblings(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			dcA := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			dcB := mustAsset(t, s, ctx, domain.KindSite, "dc-b", nil)
			mustAsset(t, s, ctx, domain.KindRack, "rack-1", &dcA)

			t.Run("a sibling of the same name is refused", func(t *testing.T) {
				a, err := domain.NewAsset(NewID(), domain.KindRack, "rack-1", &dcA, s.Now())
				if err != nil {
					t.Fatalf("building asset: %v", err)
				}
				err = s.CreateAsset(ctx, testActor, a, nil)
				if err == nil {
					t.Fatal("two live assets called rack-1 in dc-a were both created; " +
						"an import keyed on (parent, name) would then be ambiguous")
				}
				if !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("error = %v, want ErrInvalid so the handler returns 422", err)
				}
				if msg := fieldError(err, "name"); !strings.Contains(msg, "rack-1") {
					t.Errorf("name field error = %q, want it to quote the colliding name. "+
						"A refusal that does not say WHAT collided makes the operator hunt for it.", msg)
				}
			})

			t.Run("the same name under a different parent is allowed", func(t *testing.T) {
				a, err := domain.NewAsset(NewID(), domain.KindRack, "rack-1", &dcB, s.Now())
				if err != nil {
					t.Fatalf("building asset: %v", err)
				}
				if err := s.CreateAsset(ctx, testActor, a, nil); err != nil {
					t.Fatalf("rack-1 in dc-b was refused: %v\n"+
						"Two racks with the same name in different sites is normal, and "+
						"forbidding it is the whole reason the key is (parent, name) and "+
						"not a global name.", err)
				}
			})

			t.Run("a second top-level asset of the same name is refused", func(t *testing.T) {
				// The case a composite index alone would MISS: SQL treats NULLs
				// as distinct on both engines, so (NULL, 'dc-a') never collides
				// with (NULL, 'dc-a') without the second, partial index.
				a, err := domain.NewAsset(NewID(), domain.KindSite, "dc-a", nil, s.Now())
				if err != nil {
					t.Fatalf("building asset: %v", err)
				}
				err = s.CreateAsset(ctx, testActor, a, nil)
				if err == nil {
					t.Fatal("two top-level assets called dc-a were both created. NULL parents " +
						"do not collide in SQL, so the roots -- the layer everything else " +
						"hangs off -- would be the one place the rule did not hold.")
				}
				if msg := fieldError(err, "name"); msg == "" {
					t.Errorf("error = %v, want a field failure on `name`", err)
				}
			})

			t.Run("a retired sibling does not hold its name", func(t *testing.T) {
				gone := mustAsset(t, s, ctx, domain.KindServer, "recycled", &dcB)
				if err := s.RetireAsset(ctx, testActor, gone); err != nil {
					t.Fatalf("retiring: %v", err)
				}
				a, err := domain.NewAsset(NewID(), domain.KindServer, "recycled", &dcB, s.Now())
				if err != nil {
					t.Fatalf("building asset: %v", err)
				}
				if err := s.CreateAsset(ctx, testActor, a, nil); err != nil {
					t.Fatalf("a retired asset blocked its own name: %v\n"+
						"Entities here are soft-deleted and never removed, so a plain unique "+
						"index would spend that name forever on a row nobody can see and "+
						"nobody can delete.", err)
				}
			})

			t.Run("renaming onto a live sibling is refused", func(t *testing.T) {
				victim := mustAsset(t, s, ctx, domain.KindServer, "srv-a", &dcA)
				mustAsset(t, s, ctx, domain.KindServer, "srv-b", &dcA)

				got, err := s.GetAsset(ctx, victim)
				if err != nil {
					t.Fatalf("reading back: %v", err)
				}
				got.Name = "srv-b"
				err = s.UpdateAsset(ctx, testActor, &got.Asset, nil)
				if err == nil {
					t.Fatal("srv-a was renamed to srv-b alongside the real srv-b")
				}
				if msg := fieldError(err, "name"); msg == "" {
					t.Errorf("error = %v, want a field failure on `name` so the form "+
						"re-renders with what was typed", err)
				}
			})

			t.Run("a row being retired is not checked, because the index will not check it either", func(t *testing.T) {
				// The pre-check must not be STRICTER than the constraint. The
				// partial indexes exclude retired rows, so a row on its way out
				// may take a name that is already spoken for -- and if the Go
				// check refused that, the store would reject a write the
				// database was perfectly willing to accept. A guard tighter than
				// the rule it guards is its own kind of bug.
				mustAsset(t, s, ctx, domain.KindServer, "keeper", &dcA)
				leaving := mustAsset(t, s, ctx, domain.KindServer, "leaving", &dcA)

				got, err := s.GetAsset(ctx, leaving)
				if err != nil {
					t.Fatalf("reading back: %v", err)
				}
				got.Name = "keeper"
				got.Lifecycle = domain.LifecycleRetired
				if err := s.UpdateAsset(ctx, testActor, &got.Asset, nil); err != nil {
					t.Fatalf("retiring a row onto a taken name was refused: %v\n"+
						"The indexes exclude retired rows, so this write is one the database "+
						"accepts. A pre-check that refuses it makes the store stricter than "+
						"the constraint it exists to explain.", err)
				}
			})

			t.Run("moving onto a live name at the destination is refused", func(t *testing.T) {
				// Neither the name nor the parent is wrong on its own. Together
				// they are, which is why the check lives in the reparent path
				// too and not only where a name is typed.
				mustAsset(t, s, ctx, domain.KindServer, "twin", &dcA)
				mover := mustAsset(t, s, ctx, domain.KindServer, "twin", &dcB)

				err := s.ReparentAsset(ctx, testActor, mover, &dcA)
				if err == nil {
					t.Fatal("an asset was moved into a parent that already had a live " +
						"asset of that name")
				}
				// On parent_id: the destination is the only input the move form
				// has. Asserting the FIELD is what makes this test notice when the
				// pre-check is gone and the index alone is refusing with a 409.
				if msg := fieldError(err, "parent_id"); msg == "" {
					t.Errorf("error = %v, want a field failure on `parent_id`. The index "+
						"refuses this too, but as ErrConflict -- a bare 409 with no field "+
						"named and nothing rendered back.", err)
				}
			})
		})
	}
}

// TestTheSiblingNameRuleIsInTheDatabaseNotOnlyInGo is the half that matters
// once bulk import exists.
//
// requireUniqueSiblingName is a check-then-act, and a check-then-act is a
// convention. It is there to produce a sentence instead of a status code -- it
// is NOT what makes the rule true. If the only thing standing between two
// rack-1s is a SELECT in Go, then a second writer, a future code path that
// forgets the helper, or an importer built in a hurry all get to create the
// ambiguity the key exists to prevent.
//
// So this bypasses the store entirely and writes the row with SQL. It must
// still fail.
func TestTheSiblingNameRuleIsInTheDatabaseNotOnlyInGo(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc", nil)
			mustAsset(t, s, ctx, domain.KindRack, "rack-1", &site)

			at := domain.FormatTime(s.Now())
			insert := s.db.Rebind(
				`INSERT INTO asset (id, kind, name, parent_id, lifecycle, attrs, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, '{}', ?, ?)`)

			_, err := s.db.Writer.ExecContext(ctx, insert,
				NewID(), domain.KindRack, "rack-1", site, domain.LifecycleActive, at, at)
			if err == nil {
				t.Fatal("a duplicate sibling name was written straight past the store. " +
					"The Go check is the message; the partial unique index is supposed to " +
					"be the guarantee, and it is not there.")
			}

			// And the same statement must SUCCEED once the name differs, so the
			// failure above is the index and not a broken INSERT.
			_, err = s.db.Writer.ExecContext(ctx, insert,
				NewID(), domain.KindRack, "rack-2", site, domain.LifecycleActive, at, at)
			if err != nil {
				t.Fatalf("the control insert failed too, so the test above proved nothing: %v", err)
			}

			// The ROOT index, separately. Without this case the Go pre-check
			// masks its absence completely: every route into the store goes
			// through requireUniqueSiblingName, so dropping asset_root_name_key
			// leaves the whole suite green while two top-level dc-a rows become
			// writable by anything that is not this package.
			_, err = s.db.Writer.ExecContext(ctx, insert,
				NewID(), domain.KindSite, "dc", nil, domain.LifecycleActive, at, at)
			if err == nil {
				t.Fatal("a duplicate top-level name was written straight past the store. " +
					"NULL parents do not collide in SQL, so the roots need their own " +
					"partial index and it is not there.")
			}
		})
	}
}
