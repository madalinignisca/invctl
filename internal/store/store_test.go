package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gabriel/invctl/internal/domain"
)

var testActor = domain.Actor{Name: "tester", Kind: "user"}

func newStore(t *testing.T, e Engine) (*SQLStore, context.Context) {
	t.Helper()
	return New(e.Open(t)), context.Background()
}

// mustEnvironment creates an environment and returns its id.
func mustEnvironment(t *testing.T, s *SQLStore, ctx context.Context, code, role string) string {
	t.Helper()
	env, err := domain.NewEnvironment(NewID(), code, code, role, true, 2, s.Now())
	if err != nil {
		t.Fatalf("building environment %s: %v", code, err)
	}
	if err := s.CreateEnvironment(ctx, testActor, env); err != nil {
		t.Fatalf("creating environment %s: %v", code, err)
	}
	return env.ID
}

// mustAsset creates an asset and returns its id.
func mustAsset(t *testing.T, s *SQLStore, ctx context.Context, kind, name string, parentID *string, envs ...string) string {
	t.Helper()
	a, err := domain.NewAsset(NewID(), kind, name, parentID, s.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := s.CreateAsset(ctx, testActor, a, envs); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

// TestClosureTableMaintenance covers the structure every containment query and
// the impact engine's placement phase depend on.
func TestClosureTableMaintenance(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			site := mustAsset(t, s, ctx, domain.KindSite, "dc", nil)
			rack := mustAsset(t, s, ctx, domain.KindRack, "rack-1", &site)
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-1", &rack)
			vm := mustAsset(t, s, ctx, domain.KindVM, "vm-1", &hv)

			t.Run("subtree resolves from any level", func(t *testing.T) {
				cases := []struct {
					from string
					want int
				}{
					{site, 4}, // itself plus everything below
					{rack, 3},
					{hv, 2},
					{vm, 1}, // the self row at depth 0
				}
				for _, tc := range cases {
					ids, err := s.SubtreeIDs(ctx, []string{tc.from})
					if err != nil {
						t.Fatalf("resolving subtree: %v", err)
					}
					if len(ids) != tc.want {
						t.Errorf("subtree of %s has %d members, want %d", tc.from, len(ids), tc.want)
					}
				}
			})

			t.Run("ancestors are ordered root first", func(t *testing.T) {
				ancestors, err := s.Ancestors(ctx, vm)
				if err != nil {
					t.Fatalf("loading ancestors: %v", err)
				}
				if len(ancestors) != 3 {
					t.Fatalf("got %d ancestors, want 3", len(ancestors))
				}
				want := []string{"dc", "rack-1", "hv-1"}
				for i, a := range ancestors {
					if a.Name != want[i] {
						t.Errorf("ancestor %d = %s, want %s", i, a.Name, want[i])
					}
				}
			})
		})
	}
}

// TestReparentRebuildsSubtree checks the operation the handover warns about:
// moving a node has to rebuild the closure rows for everything under it, not
// just the node itself.
func TestReparentRebuildsSubtree(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			site := mustAsset(t, s, ctx, domain.KindSite, "dc", nil)
			rackA := mustAsset(t, s, ctx, domain.KindRack, "rack-a", &site)
			rackB := mustAsset(t, s, ctx, domain.KindRack, "rack-b", &site)
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-1", &rackA)
			vm := mustAsset(t, s, ctx, domain.KindVM, "vm-1", &hv)

			if err := s.ReparentAsset(ctx, testActor, hv, &rackB); err != nil {
				t.Fatalf("reparenting: %v", err)
			}

			// The VM moved with its hypervisor even though nothing touched it
			// directly. This is the case a naive implementation gets wrong.
			underB, err := s.SubtreeIDs(ctx, []string{rackB})
			if err != nil {
				t.Fatalf("resolving subtree: %v", err)
			}
			if !containsID(underB, vm) {
				t.Errorf("vm-1 is not under rack-b after moving its hypervisor; the subtree was not rebuilt")
			}

			underA, err := s.SubtreeIDs(ctx, []string{rackA})
			if err != nil {
				t.Fatalf("resolving subtree: %v", err)
			}
			if containsID(underA, vm) || containsID(underA, hv) {
				t.Errorf("rack-a still contains the moved subtree: %v", underA)
			}

			// The site is still an ancestor of everything.
			underSite, err := s.SubtreeIDs(ctx, []string{site})
			if err != nil {
				t.Fatalf("resolving subtree: %v", err)
			}
			if len(underSite) != 5 {
				t.Errorf("site subtree has %d members, want 5", len(underSite))
			}

			// And the depths are right, not merely the membership.
			ancestors, err := s.Ancestors(ctx, vm)
			if err != nil {
				t.Fatalf("loading ancestors: %v", err)
			}
			want := []string{"dc", "rack-b", "hv-1"}
			if len(ancestors) != len(want) {
				t.Fatalf("got %d ancestors, want %d", len(ancestors), len(want))
			}
			for i, a := range ancestors {
				if a.Name != want[i] {
					t.Errorf("ancestor %d = %s, want %s", i, a.Name, want[i])
				}
			}
		})
	}
}

// TestReparentRejectsCycles: the closure table cannot represent a cycle, and a
// detached subtree is worse than a refused edit.
func TestReparentRejectsCycles(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			site := mustAsset(t, s, ctx, domain.KindSite, "dc", nil)
			rack := mustAsset(t, s, ctx, domain.KindRack, "rack-a", &site)
			hv := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-1", &rack)

			// Moving the rack under its own hypervisor.
			err := s.ReparentAsset(ctx, testActor, rack, &hv)
			if err == nil {
				t.Fatal("reparenting into a descendant succeeded; that would create a cycle")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid so the handler returns 422", err)
			}

			// Moving an asset under itself.
			if err := s.ReparentAsset(ctx, testActor, rack, &rack); err == nil {
				t.Fatal("reparenting an asset under itself succeeded")
			}

			// And the tree is untouched after the refusals.
			ancestors, err := s.Ancestors(ctx, hv)
			if err != nil {
				t.Fatalf("loading ancestors: %v", err)
			}
			if len(ancestors) != 2 {
				t.Errorf("hv-1 has %d ancestors after the failed moves, want 2", len(ancestors))
			}
		})
	}
}

// TestEveryMutationWritesChangeLog is the audit rule, checked at the store
// level rather than trusted. A handler that mutates without logging is
// incomplete, and this is what makes that detectable.
func TestEveryMutationWritesChangeLog(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-1", nil, envID)

			t.Run("create writes a snapshot", func(t *testing.T) {
				changes, err := s.ListChangesForEntity(ctx, "asset", assetID, 10)
				if err != nil {
					t.Fatalf("listing changes: %v", err)
				}
				if len(changes) != 1 {
					t.Fatalf("got %d change entries after create, want 1", len(changes))
				}
				if changes[0].Action != domain.ActionCreate {
					t.Errorf("action = %s, want create", changes[0].Action)
				}
				if changes[0].Actor != testActor.Name {
					t.Errorf("actor = %s, want %s", changes[0].Actor, testActor.Name)
				}

				var snapshot map[string]map[string]any
				if err := json.Unmarshal([]byte(changes[0].Diff), &snapshot); err != nil {
					t.Fatalf("decoding create diff %q: %v", changes[0].Diff, err)
				}
				if snapshot["new"]["name"] != "srv-1" {
					t.Errorf("create snapshot = %v, want it to record the name", snapshot)
				}
			})

			t.Run("update writes a field-level diff", func(t *testing.T) {
				asset, err := s.GetAsset(ctx, assetID)
				if err != nil {
					t.Fatalf("getting asset: %v", err)
				}
				updated := asset.Asset
				updated.Name = "srv-1-renamed"
				vendor := "Dell"
				updated.Vendor = &vendor

				if err := s.UpdateAsset(ctx, testActor, &updated, nil); err != nil {
					t.Fatalf("updating asset: %v", err)
				}

				changes, err := s.ListChangesForEntity(ctx, "asset", assetID, 10)
				if err != nil {
					t.Fatalf("listing changes: %v", err)
				}
				if len(changes) != 2 {
					t.Fatalf("got %d change entries after update, want 2", len(changes))
				}

				var diff map[string]struct {
					Old any `json:"old"`
					New any `json:"new"`
				}
				if err := json.Unmarshal([]byte(changes[0].Diff), &diff); err != nil {
					t.Fatalf("decoding update diff %q: %v", changes[0].Diff, err)
				}
				if diff["name"].Old != "srv-1" || diff["name"].New != "srv-1-renamed" {
					t.Errorf("name diff = %+v, want the old and new values", diff["name"])
				}
				if diff["vendor"].Old != nil || diff["vendor"].New != "Dell" {
					t.Errorf("vendor diff = %+v, want nil -> Dell", diff["vendor"])
				}
				// updated_at moves on every write and would drown the change
				// that actually matters.
				if _, present := diff["updated_at"]; present {
					t.Errorf("diff includes updated_at: %v", diff)
				}
			})

			t.Run("a no-op update writes nothing", func(t *testing.T) {
				before, err := s.ListChangesForEntity(ctx, "asset", assetID, 50)
				if err != nil {
					t.Fatalf("listing changes: %v", err)
				}
				asset, err := s.GetAsset(ctx, assetID)
				if err != nil {
					t.Fatalf("getting asset: %v", err)
				}
				unchanged := asset.Asset
				if err := s.UpdateAsset(ctx, testActor, &unchanged, nil); err != nil {
					t.Fatalf("updating asset: %v", err)
				}
				after, err := s.ListChangesForEntity(ctx, "asset", assetID, 50)
				if err != nil {
					t.Fatalf("listing changes: %v", err)
				}
				if len(after) != len(before) {
					t.Errorf("a no-op update added %d entries; an audit trail full of "+
						"empty entries is worse than none", len(after)-len(before))
				}
			})

			t.Run("retire writes a retire entry", func(t *testing.T) {
				if err := s.RetireAsset(ctx, testActor, assetID); err != nil {
					t.Fatalf("retiring asset: %v", err)
				}
				changes, err := s.ListChangesForEntity(ctx, "asset", assetID, 10)
				if err != nil {
					t.Fatalf("listing changes: %v", err)
				}
				if changes[0].Action != domain.ActionRetire {
					t.Errorf("action = %s, want retire", changes[0].Action)
				}
			})
		})
	}
}

// TestSoftDeleteKeepsTheRow: retirement is a lifecycle transition, because the
// decommissioned server is exactly what an auditor asks about later.
func TestSoftDeleteKeepsTheRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-old", nil)

			if err := s.RetireAsset(ctx, testActor, assetID); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			asset, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("the row is gone after retirement: %v", err)
			}
			if asset.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %s, want retired", asset.Lifecycle)
			}

			// Retired rows are out of the default list but still reachable.
			active, err := s.ListAssets(ctx, AssetFilter{})
			if err != nil {
				t.Fatalf("listing assets: %v", err)
			}
			if len(active) != 0 {
				t.Errorf("retired asset still appears in the default list")
			}
			all, err := s.ListAssets(ctx, AssetFilter{IncludeRetired: true})
			if err != nil {
				t.Fatalf("listing assets: %v", err)
			}
			if len(all) != 1 {
				t.Errorf("retired asset is not reachable with IncludeRetired")
			}

			// Retiring twice is a no-op, not an error or a second audit row.
			if err := s.RetireAsset(ctx, testActor, assetID); err != nil {
				t.Errorf("retiring twice: %v", err)
			}
		})
	}
}

// TestTransactionRollsBackBothWrites: the change log entry lives in the same
// transaction as the mutation, so neither can survive without the other.
func TestTransactionRollsBackBothWrites(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// An asset whose parent does not exist violates the foreign key,
			// so the insert fails after the transaction has begun.
			missing := "no-such-parent"
			a, err := domain.NewAsset(NewID(), domain.KindVM, "orphan", &missing, s.Now())
			if err != nil {
				t.Fatalf("building asset: %v", err)
			}
			if err := s.CreateAsset(ctx, testActor, a, nil); err == nil {
				t.Fatal("creating an asset with a dangling parent succeeded")
			}

			if _, err := s.GetAsset(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("the asset row survived a failed transaction: %v", err)
			}
			changes, err := s.ListChangesForEntity(ctx, "asset", a.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 0 {
				t.Errorf("a change log entry survived a failed transaction: %v", changes)
			}
		})
	}
}

// TestSpanningAssetsExcludesTransit is the segmentation report's definition of
// a false positive: a transit zone spanning environments is doing its job.
func TestSpanningAssetsExcludesTransit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			prod := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			dev := mustEnvironment(t, s, ctx, "dev", domain.EnvRoleDev)
			transit := mustEnvironment(t, s, ctx, "transit", domain.EnvRoleTransit)

			mustAsset(t, s, ctx, domain.KindSwitch, "sw-shared", nil, prod, dev)
			mustAsset(t, s, ctx, domain.KindFirewall, "fw-edge", nil, prod, transit)
			mustAsset(t, s, ctx, domain.KindServer, "srv-prod", nil, prod)

			spanning, err := s.SpanningAssets(ctx)
			if err != nil {
				t.Fatalf("finding spanning assets: %v", err)
			}
			if len(spanning) != 1 {
				names := make([]string, len(spanning))
				for i, a := range spanning {
					names[i] = a.Name
				}
				t.Fatalf("spanning = %v, want only sw-shared", names)
			}
			if spanning[0].Name != "sw-shared" {
				t.Errorf("spanning = %s, want sw-shared", spanning[0].Name)
			}
		})
	}
}

// TestInstanceRejectsNonHostableAsset: a workload on a patch panel is a
// data-entry mistake the impact engine would inherit silently.
func TestInstanceRejectsNonHostableAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			rack := mustAsset(t, s, ctx, domain.KindRack, "rack-a", nil, envID)

			svc, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "svc", Name: "Service", Kind: domain.SvcAPI,
				EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 3,
			}, s.Now())
			if err != nil {
				t.Fatalf("building service: %v", err)
			}
			if err := s.CreateService(ctx, testActor, svc); err != nil {
				t.Fatalf("creating service: %v", err)
			}

			si, err := domain.NewServiceInstance(NewID(), svc.ID, rack, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			err = s.CreateInstance(ctx, testActor, si)
			if err == nil {
				t.Fatal("placing a service instance on a rack succeeded")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// TestConflictsAreTranslated: both engines word a uniqueness violation
// differently, and neither wording should reach a handler.
func TestConflictsAreTranslated(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

			dup, err := domain.NewEnvironment(NewID(), "prod", "Production again",
				domain.EnvRoleProduction, true, 1, s.Now())
			if err != nil {
				t.Fatalf("building environment: %v", err)
			}
			err = s.CreateEnvironment(ctx, testActor, dup)
			if err == nil {
				t.Fatal("creating a duplicate environment code succeeded")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict so the handler returns 409", err)
			}
		})
	}
}

// TestNotFoundIsTranslated: a missing row is a domain sentinel, never a driver
// error.
func TestNotFoundIsTranslated(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			if _, err := s.GetAsset(ctx, "no-such-asset"); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("GetAsset error = %v, want ErrNotFound", err)
			}
			if _, err := s.GetService(ctx, "no-such-service"); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("GetService error = %v, want ErrNotFound", err)
			}
			if _, err := s.GetUserByUsername(ctx, "nobody"); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("GetUserByUsername error = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestTimestampsComeFromGoNotTheDatabase: a DB clock default would make
// ordering depend on the engine's timezone handling.
func TestTimestampsComeFromGoNotTheDatabase(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			fixed := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
			s := New(e.Open(t)).WithClock(func() time.Time { return fixed })
			ctx := context.Background()

			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-1", nil)

			asset, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("getting asset: %v", err)
			}
			want := domain.FormatTime(fixed)
			if asset.CreatedAt != want {
				t.Errorf("created_at = %q, want %q from the injected clock", asset.CreatedAt, want)
			}

			changes, err := s.ListChangesForEntity(ctx, "asset", assetID, 1)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if changes[0].At != want {
				t.Errorf("change log at = %q, want %q", changes[0].At, want)
			}
		})
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestUpsertLDAPUserRefusesToAdoptALocalAccount. Two directories of truth can
// produce the same username. Handing a directory principal the existing local
// row would merge the identities: the LDAP user would inherit the local
// account's id, and every change_log row it wrote would name the local
// operator. The collision has to be refused, not resolved by guessing.
func TestUpsertLDAPUserRefusesToAdoptALocalAccount(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			hash := "$argon2id$fake"
			local, err := domain.NewAppUser(NewID(), "admin", domain.UserSourceLocal, s.Now())
			if err != nil {
				t.Fatalf("building user: %v", err)
			}
			local.PasswordHash = &hash
			if err := s.CreateUser(ctx, testActor, local); err != nil {
				t.Fatalf("creating local user: %v", err)
			}

			_, err = s.UpsertLDAPUser(ctx, "admin", "", "")
			if err == nil {
				t.Fatal("an LDAP bind adopted the local admin account")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict", err)
			}

			// A genuinely new directory user is still fine.
			ldapUser, err := s.UpsertLDAPUser(ctx, "nikolaj", "", "")
			if err != nil {
				t.Fatalf("creating an ldap user: %v", err)
			}
			if ldapUser.Source != domain.UserSourceLDAP {
				t.Errorf("source = %s, want ldap", ldapUser.Source)
			}
			if ldapUser.PasswordHash != nil {
				t.Error("an LDAP user was given a password hash")
			}

			// And signing in again returns the same row rather than colliding.
			again, err := s.UpsertLDAPUser(ctx, "nikolaj", "", "")
			if err != nil {
				t.Fatalf("second ldap sign-in: %v", err)
			}
			if again.ID != ldapUser.ID {
				t.Errorf("second sign-in created a new account: %s vs %s", again.ID, ldapUser.ID)
			}
		})
	}
}

// TestAssetEnvironmentsCanBeCleared. Passing an empty slice means "this asset
// is in no environments"; nil means "not managing membership here". Conflating
// them made it impossible to remove an asset's last environment.
func TestAssetEnvironmentsCanBeCleared(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			prod := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-1", nil, prod)

			asset, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("getting asset: %v", err)
			}
			if len(asset.Environments) != 1 {
				t.Fatalf("setup: got %d environments, want 1", len(asset.Environments))
			}

			// Empty slice: remove all membership.
			updated := asset.Asset
			if err := s.UpdateAsset(ctx, testActor, &updated, []string{}); err != nil {
				t.Fatalf("updating asset: %v", err)
			}
			cleared, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("getting asset: %v", err)
			}
			if len(cleared.Environments) != 0 {
				t.Errorf("got %d environments after clearing, want 0", len(cleared.Environments))
			}

			// nil: leave membership alone.
			restored := cleared.Asset
			if err := s.UpdateAsset(ctx, testActor, &restored, []string{prod}); err != nil {
				t.Fatalf("restoring membership: %v", err)
			}
			again := restored
			again.Name = "srv-1-renamed"
			if err := s.UpdateAsset(ctx, testActor, &again, nil); err != nil {
				t.Fatalf("updating without managing membership: %v", err)
			}
			final, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("getting asset: %v", err)
			}
			if len(final.Environments) != 1 {
				t.Errorf("nil membership wiped the environments: got %d, want 1", len(final.Environments))
			}
		})
	}
}

// TestSpanningAssetsExcludesRetired. A decommissioned server that used to
// bridge two environments is history, not an open segmentation finding.
func TestSpanningAssetsExcludesRetired(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			prod := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			dev := mustEnvironment(t, s, ctx, "dev", domain.EnvRoleDev)
			assetID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-old", nil, prod, dev)

			spanning, err := s.SpanningAssets(ctx)
			if err != nil {
				t.Fatalf("finding spanning assets: %v", err)
			}
			if len(spanning) != 1 {
				t.Fatalf("setup: got %d spanning assets, want 1", len(spanning))
			}

			if err := s.RetireAsset(ctx, testActor, assetID); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			after, err := s.SpanningAssets(ctx)
			if err != nil {
				t.Fatalf("finding spanning assets: %v", err)
			}
			if len(after) != 0 {
				t.Errorf("a retired asset is still reported as spanning environments")
			}
		})
	}
}
