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
	"path/filepath"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The three properties an import surface lives or dies by: it creates and never
// updates, it applies the whole file or none of it, and a dry run tells the
// truth about what a real run would do.
//
// Each of those is the kind of guarantee that passes for the wrong reason. "No
// assets were created" is what a correct refusal and a completely broken
// importer look like from the outside, so every refusal test below also proves
// the same file WOULD have worked without the fault under test.

const importHeader = "parent,name,kind\n"

func importCSV(t *testing.T, body string) []AssetImportRow {
	t.Helper()
	rows, problems := ParseAssetCSV(strings.NewReader(body))
	if len(problems) > 0 {
		t.Fatalf("parsing the fixture file failed: %+v", problems)
	}
	return rows
}

func countAssets(t *testing.T, s *SQLStore, ctx context.Context) int {
	t.Helper()
	all, err := s.ListAssets(ctx, AssetFilter{IncludeRetired: true})
	if err != nil {
		t.Fatalf("listing assets: %v", err)
	}
	return len(all)
}

func problemsAbout(r *ImportReport, needle string) []ImportProblem {
	var out []ImportProblem
	for _, p := range r.Problems {
		if strings.Contains(p.Message, needle) {
			out = append(out, p)
		}
	}
	return out
}

func TestAnImportCreatesTheWholeFileOrNoneOfIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			t.Run("a good file lands, parents before children", func(t *testing.T) {
				s, ctx := newStore(t, e)
				before := countAssets(t, s, ctx)

				// Children BEFORE their parents on purpose. A spreadsheet is
				// sorted by whatever column somebody clicked, and refusing a
				// file for its row order would be refusing every real file.
				rows := importCSV(t, importHeader+
					"dc-a/rack-1,esx-01,hypervisor\n"+
					"dc-a,rack-1,rack\n"+
					",dc-a,site\n")

				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("a valid file was refused: %+v", report.Problems)
				}
				if got := countAssets(t, s, ctx) - before; got != 3 {
					t.Errorf("created %d assets, want 3", got)
				}
				if !report.Applied() {
					t.Error("report.Applied() is false after a successful run")
				}
			})

			t.Run("one bad row refuses the whole file", func(t *testing.T) {
				s, ctx := newStore(t, e)
				before := countAssets(t, s, ctx)

				// Identical to the file above except for one unknown kind on
				// the LAST row, so the two preceding rows are ones the importer
				// has already written into the transaction by the time it
				// fails. If anything survives, the rollback is not happening.
				rows := importCSV(t, importHeader+
					",dc-a,site\n"+
					"dc-a,rack-1,rack\n"+
					"dc-a,rack-2,teleporter\n")

				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) == 0 {
					t.Fatal("a file containing an unknown kind was accepted")
				}
				if got := countAssets(t, s, ctx) - before; got != 0 {
					t.Errorf("%d assets survived a refused file. A partially applied import "+
						"is the worst outcome available: the operator cannot tell what "+
						"landed, and re-running collides with its own successful half.", got)
				}
				if len(report.Created) != 0 {
					t.Errorf("report lists %d created paths for a file that was rolled back; "+
						"the report would be describing a transaction that no longer exists",
						len(report.Created))
				}
			})

			t.Run("the refusal names the line and the field", func(t *testing.T) {
				s, ctx := newStore(t, e)
				rows := importCSV(t, importHeader+
					",dc-a,site\n"+
					"dc-a,rack-2,teleporter\n")

				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) != 1 {
					t.Fatalf("problems = %+v, want exactly one", report.Problems)
				}
				p := report.Problems[0]
				if p.Line != 3 {
					t.Errorf("problem is on line %d, want 3. A report that cannot point at "+
						"the row is a report somebody has to search a file with.", p.Line)
				}
				if p.Field != "kind" {
					t.Errorf("problem field = %q, want \"kind\"", p.Field)
				}
			})
		})
	}
}

func TestAnImportCreatesAndNeverUpdates(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			rack := mustAsset(t, s, ctx, domain.KindRack, "rack-1", &site)

			// A row for something that already exists, carrying a DIFFERENT
			// vendor. If import silently updated, this would be the field that
			// changed.
			rows, problems := ParseAssetCSV(strings.NewReader(
				"parent,name,kind,vendor\ndc-a,rack-1,rack,Acme\n"))
			if len(problems) > 0 {
				t.Fatalf("parsing: %+v", problems)
			}

			report, err := s.ImportAssets(ctx, testActor, rows, false)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if len(problemsAbout(report, "already exists")) != 1 {
				t.Fatalf("problems = %+v, want one saying the asset already exists", report.Problems)
			}

			got, err := s.GetAsset(ctx, rack)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if got.Vendor != nil {
				t.Errorf("vendor = %q after an import that should have refused. Import "+
					"creates; it does not update. A file that rewrites four hundred assets "+
					"writes four hundred change_log rows nobody reviewed.", *got.Vendor)
			}
		})
	}
}

// TestADryRunWritesNothingAndStillTellsTheTruth is the property that makes the
// preview worth showing.
//
// A dry run that reports a different answer from the real run is worse than no
// dry run at all, because an operator trusts it. So this asserts both halves on
// ONE file: nothing is written, and the paths it claims it would create are
// exactly the ones a real run then creates.
func TestADryRunWritesNothingAndStillTellsTheTruth(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			before := countAssets(t, s, ctx)

			file := importHeader +
				",dc-a,site\n" +
				"dc-a,rack-1,rack\n" +
				"dc-a/rack-1,esx-01,hypervisor\n"

			dry, err := s.ImportAssets(ctx, testActor, importCSV(t, file), true)
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}
			if len(dry.Problems) > 0 {
				t.Fatalf("dry run refused a valid file: %+v", dry.Problems)
			}
			if got := countAssets(t, s, ctx) - before; got != 0 {
				t.Fatalf("a dry run created %d assets", got)
			}
			if dry.Applied() {
				t.Error("Applied() is true for a dry run, so a rendered result could claim " +
					"to have written what it discarded")
			}
			if len(dry.Created) != 3 {
				t.Fatalf("dry run says it would create %v, want 3 paths", dry.Created)
			}

			real, err := s.ImportAssets(ctx, testActor, importCSV(t, file), false)
			if err != nil {
				t.Fatalf("real run: %v", err)
			}
			if len(real.Problems) > 0 {
				t.Fatalf("the real run refused a file the dry run accepted: %+v", real.Problems)
			}
			if strings.Join(dry.Created, "|") != strings.Join(real.Created, "|") {
				t.Errorf("dry run predicted %v, real run created %v.\n"+
					"The preview is only worth showing if it is the same code path; a "+
					"divergence here means it is simulating rather than running.",
					dry.Created, real.Created)
			}
			if got := countAssets(t, s, ctx) - before; got != 3 {
				t.Errorf("created %d assets on the real run, want 3", got)
			}
		})
	}
}

func TestAnImportResolvesNamesAgainstTheEstateAndItself(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			t.Run("a parent that exists already is found", func(t *testing.T) {
				s, ctx := newStore(t, e)
				mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)

				report, err := s.ImportAssets(ctx, testActor,
					importCSV(t, importHeader+"dc-a,rack-9,rack\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("a row naming an existing parent was refused: %+v", report.Problems)
				}
			})

			t.Run("a parent nothing creates is named, not swallowed", func(t *testing.T) {
				s, ctx := newStore(t, e)
				report, err := s.ImportAssets(ctx, testActor,
					importCSV(t, importHeader+"dc-nowhere,rack-9,rack\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				found := problemsAbout(report, "dc-nowhere")
				if len(found) != 1 {
					t.Fatalf("problems = %+v, want one naming the missing parent path", report.Problems)
				}
				if found[0].Line != 2 {
					t.Errorf("problem line = %d, want 2", found[0].Line)
				}
			})

			t.Run("two rows claiming one path are refused", func(t *testing.T) {
				s, ctx := newStore(t, e)
				report, err := s.ImportAssets(ctx, testActor,
					importCSV(t, importHeader+",dc-a,site\n,dc-a,site\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				// Reported as a duplicate WITHIN the file. Leaving it to the
				// unique index would be true but would read as "the estate
				// already contains this", sending the operator to look for a
				// row that is in their own file.
				if len(problemsAbout(report, "already claims this path")) != 1 {
					t.Fatalf("problems = %+v, want one about a duplicate path in the file",
						report.Problems)
				}
			})

			t.Run("the same name under two different parents is fine", func(t *testing.T) {
				s, ctx := newStore(t, e)
				report, err := s.ImportAssets(ctx, testActor, importCSV(t, importHeader+
					",dc-a,site\n,dc-b,site\n"+
					"dc-a,rack-1,rack\ndc-b,rack-1,rack\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("two racks called rack-1 in different sites were refused: %+v\n"+
						"That is normal in a real estate and is the whole reason the key is "+
						"(parent, name).", report.Problems)
				}
			})

			t.Run("a retired asset does not block its path", func(t *testing.T) {
				s, ctx := newStore(t, e)
				site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
				gone := mustAsset(t, s, ctx, domain.KindRack, "rack-1", &site)
				if err := s.RetireAsset(ctx, testPermit, gone); err != nil {
					t.Fatalf("retiring: %v", err)
				}

				report, err := s.ImportAssets(ctx, testActor,
					importCSV(t, importHeader+"dc-a,rack-1,rack\n"), false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("a retired asset blocked its own path: %+v\n"+
						"The unique indexes exclude retired rows, so refusing here would "+
						"reject a file the database would have accepted.", report.Problems)
				}
			})
		})
	}
}

func TestAnImportedAssetIsAuditedLikeAnyOtherCreation(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			var before int
			if err := s.db.Reader.GetContext(ctx, &before,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = 'asset'`); err != nil {
				t.Fatalf("counting: %v", err)
			}

			if _, err := s.ImportAssets(ctx, testActor, importCSV(t, importHeader+
				",dc-a,site\ndc-a,rack-1,rack\n"), false); err != nil {
				t.Fatalf("importing: %v", err)
			}

			var after int
			if err := s.db.Reader.GetContext(ctx, &after,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = 'asset'`); err != nil {
				t.Fatalf("counting: %v", err)
			}
			// ONE ROW PER ASSET, not one per file. An import is a declared-state
			// mutation and the audit obligation does not soften because several
			// arrived together -- "who put this box in the inventory" has to stay
			// answerable per box.
			if got := after - before; got != 2 {
				t.Errorf("import wrote %d asset audit rows, want 2 (one per asset)", got)
			}
		})
	}
}

func TestADryRunLeavesNoAuditTrailEither(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			var before int
			if err := s.db.Reader.GetContext(ctx, &before,
				`SELECT COUNT(*) FROM change_log`); err != nil {
				t.Fatalf("counting: %v", err)
			}
			if _, err := s.ImportAssets(ctx, testActor, importCSV(t, importHeader+
				",dc-a,site\ndc-a,rack-1,rack\n"), true); err != nil {
				t.Fatalf("dry run: %v", err)
			}
			var after int
			if err := s.db.Reader.GetContext(ctx, &after,
				`SELECT COUNT(*) FROM change_log`); err != nil {
				t.Fatalf("counting: %v", err)
			}
			if after != before {
				t.Errorf("a dry run left %d change_log rows behind. The audit trail is "+
					"append-only and permanent; a preview must not write into it.", after-before)
			}
		})
	}
}

func TestTheFileFormatRefusesWhatItCannotHonour(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// The silent-fallback shape, in file form: a misspelled header
			// would otherwise be dropped and the column it meant left unset,
			// with a cheerful "42 assets created".
			name: "a misspelled column is named rather than ignored",
			body: "parent,name,kind,lifecyle\n,dc-a,site,active\n",
			want: "no asset column called",
		},
		{
			name: "a missing name column is refused",
			body: "parent,kind\n,site\n",
			want: `no "name" column`,
		},
		{
			name: "a missing kind column is refused",
			body: "parent,name\n,dc-a\n",
			want: `no "kind" column`,
		},
		{
			name: "a duplicated column is refused",
			body: "name,name,kind\ndc-a,dc-b,site\n",
			want: "appears twice",
		},
		{
			name: "an empty file is refused",
			body: "",
			want: "empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, problems := ParseAssetCSV(strings.NewReader(tc.body))
			if len(problems) == 0 {
				t.Fatalf("the file was accepted; want a problem mentioning %q", tc.want)
			}
			var joined []string
			for _, p := range problems {
				joined = append(joined, p.Message)
			}
			if !strings.Contains(strings.Join(joined, " | "), tc.want) {
				t.Errorf("problems = %v, want one mentioning %q", joined, tc.want)
			}
		})
	}

	t.Run("a header-only file is not silently a success", func(t *testing.T) {
		rows, problems := ParseAssetCSV(strings.NewReader(importHeader))
		if len(problems) > 0 {
			t.Fatalf("the header itself was refused: %+v", problems)
		}
		if len(rows) != 0 {
			t.Fatalf("rows = %d, want 0", len(rows))
		}
	})
}

func TestImportingOwnershipNamesATeamAndNeverAPerson(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			team, err := domain.NewTeam(NewID(), domain.TeamSpec{Code: "platform", Name: "Platform"}, s.Now())
			if err != nil {
				t.Fatalf("building the team: %v", err)
			}
			if err := s.CreateTeam(ctx, testActor, team); err != nil {
				t.Fatalf("creating the team: %v", err)
			}

			t.Run("a team code and a role are resolved and stored", func(t *testing.T) {
				rows, problems := ParseAssetCSV(strings.NewReader(
					"name,kind,team,manager_role\nowned-01,server,platform,owner\n"))
				if len(problems) > 0 {
					t.Fatalf("parsing: %+v", problems)
				}
				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("a row naming a real team was refused: %+v", report.Problems)
				}

				list, err := s.ListAssets(ctx, AssetFilter{Query: "owned-01"})
				if err != nil || len(list) != 1 {
					t.Fatalf("reading back: %v (%d rows)", err, len(list))
				}
				// Read the ID back rather than trusting "no error": an
				// unresolved code silently left as nil is the exact shape that
				// reports success and stores nothing.
				if list[0].TeamID == nil || *list[0].TeamID != team.ID {
					t.Errorf("team_id = %v, want the platform team's id", list[0].TeamID)
				}
				if list[0].ManagerRole == nil || *list[0].ManagerRole != "owner" {
					t.Errorf("manager_role = %v, want \"owner\"", list[0].ManagerRole)
				}
			})

			t.Run("an unknown team code is named, not dropped", func(t *testing.T) {
				rows, _ := ParseAssetCSV(strings.NewReader(
					"name,kind,team\nghost-01,server,no-such-team\n"))
				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(problemsAbout(report, "no-such-team")) != 1 {
					t.Fatalf("problems = %+v, want one quoting the unknown code.\n"+
						"Leaving it unset would import the asset with no owner and "+
						"report success -- nobody to call at 03:00, and nothing said so.",
						report.Problems)
				}
			})

			t.Run("a role without a team is refused", func(t *testing.T) {
				// The rule already exists in the domain; this proves the import
				// path goes through it rather than around it.
				rows, _ := ParseAssetCSV(strings.NewReader(
					"name,kind,manager_role\norphan-01,server,owner\n"))
				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) == 0 {
					t.Fatal("an asset was imported with a responsibility role and no team " +
						"to hold it")
				}
			})
		})
	}
}

// TestTheDocumentedExampleActuallyImports reads the CSV block out of
// docs/IMPORT.md and runs it.
//
// A worked example is the first thing anybody copies, and a broken one costs
// more trust than no example at all -- they assume the tool is wrong before they
// assume the documentation is. Reading the real file rather than a copy is the
// point: a copy drifts silently, which is the failure this is guarding against.
func TestTheDocumentedExampleActuallyImports(t *testing.T) {
	doc := readFile(t, filepath.Join(repoRoot(t), "docs", "IMPORT.md"))

	// EVERY csv block, not the first one. The document grew a second example
	// when the catalogue importer landed, and a guard that checked only block
	// one would have gone on passing while the new example rotted -- which is
	// precisely the failure it exists to prevent.
	var examples []string
	rest := doc
	for {
		_, after, found := strings.Cut(rest, "```csv\n")
		if !found {
			break
		}
		block, remainder, closed := strings.Cut(after, "```")
		if !closed {
			t.Fatal("a csv block in docs/IMPORT.md is not closed")
		}
		examples = append(examples, block)
		rest = remainder
	}
	if len(examples) < 2 {
		t.Fatalf("docs/IMPORT.md has %d csv examples, expected at least 2 (assets and "+
			"device types). If one moved, point this test at it; do not delete it.",
			len(examples))
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for i, example := range examples {
				s, ctx := newStore(t, e)

				// The estate the examples reference. Set up per example so one
				// cannot depend on another having run first.
				team, err := domain.NewTeam(NewID(), domain.TeamSpec{Code: "platform", Name: "Platform"}, s.Now())
				if err != nil {
					t.Fatalf("building the team: %v", err)
				}
				if err := s.CreateTeam(ctx, testActor, team); err != nil {
					t.Fatalf("creating the team: %v", err)
				}
				mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
				mustEnvironment(t, s, ctx, "dr", domain.EnvRoleProduction)
				dell := mustManufacturer(t, s, ctx, "dell", "Dell")
				mustManufacturer(t, s, ctx, "hpe", "HPE")

				// Dispatched on the header, so the test does not have to be told
				// which block is which -- and a third example is picked up by
				// whichever importer its columns name.
				header, _, _ := strings.Cut(example, "\n")
				catalogueExample := strings.HasPrefix(header, "manufacturer,")

				// The asset example POINTS AT a catalogued model; the catalogue
				// example CREATES one. Seeding it for both would make the second
				// collide with the fixture rather than be judged on the file, so
				// the estate is built to suit whichever example is running.
				if !catalogueExample {
					mustDeviceType(t, s, ctx, dell, "R650", ptr("2029-03-31"))
				}

				var report *ImportReport
				var problems []ImportProblem
				if catalogueExample {
					var rows []DeviceTypeImportRow
					rows, problems = ParseDeviceTypeCSV(strings.NewReader(example))
					if len(problems) == 0 {
						report, err = s.ImportDeviceTypes(ctx, testActor, rows, false)
					}
				} else {
					var rows []AssetImportRow
					rows, problems = ParseAssetCSV(strings.NewReader(example))
					if len(problems) == 0 {
						report, err = s.ImportAssets(ctx, testActor, rows, false)
					}
				}

				if len(problems) > 0 {
					t.Fatalf("example %d does not parse: %+v", i+1, problems)
				}
				if err != nil {
					t.Fatalf("importing example %d: %v", i+1, err)
				}
				if len(report.Problems) > 0 {
					t.Errorf("example %d was refused: %+v\n"+
						"Somebody will copy this block first. Fix the example or fix the "+
						"importer, but they cannot disagree.", i+1, report.Problems)
				}
				if len(report.Created) == 0 {
					t.Errorf("example %d created nothing", i+1)
				}
			}
		})
	}
}

// TestImportingAnAssetPointsItAtACataloguedModel closes the gap that made the
// import story only look complete: assets could be imported and models could be
// imported, but nothing linked them without opening a form per box.
func TestImportingAnAssetPointsItAtACataloguedModel(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dell := mustManufacturer(t, s, ctx, "dell", "Dell")
			r650 := mustDeviceType(t, s, ctx, dell, "PowerEdge R650", ptr("2029-03-31"))

			t.Run("the model is resolved and the date is inherited", func(t *testing.T) {
				rows, problems := ParseAssetCSV(strings.NewReader(
					"name,kind,device_type\ninherits-01,server,dell/PowerEdge R650\n"))
				if len(problems) > 0 {
					t.Fatalf("parsing: %+v", problems)
				}
				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("refused: %+v", report.Problems)
				}

				list, err := s.ListAssets(ctx, AssetFilter{Query: "inherits-01"})
				if err != nil || len(list) != 1 {
					t.Fatalf("reading back: %v (%d rows)", err, len(list))
				}
				// The ID read back, not "no error": an unresolved path left as nil
				// is the shape that reports success and stores nothing.
				if list[0].DeviceTypeID == nil || *list[0].DeviceTypeID != r650 {
					t.Fatalf("device_type_id = %v, want the R650's id", list[0].DeviceTypeID)
				}
				// And the point of linking it at all.
				if got := list[0].ResolvedEOL(); got == nil || *got != "2029-03-31" {
					t.Errorf("resolved EOL = %v, want the model's 2029-03-31", got)
				}
				if !list[0].InheritedEOL() {
					t.Error("the date is not marked as inherited")
				}
			})

			t.Run("case is folded, so a lower-cased file still resolves", func(t *testing.T) {
				rows, _ := ParseAssetCSV(strings.NewReader(
					"name,kind,device_type\nlowercased-01,server,dell/poweredge r650\n"))
				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) > 0 {
					t.Fatalf("a lower-cased model path was refused: %+v\n"+
						"Nobody transcribing a model name from a screen preserves its "+
						"capitalisation.", report.Problems)
				}
			})

			t.Run("an unknown model is named, not silently left unset", func(t *testing.T) {
				rows, _ := ParseAssetCSV(strings.NewReader(
					"name,kind,device_type\nghost-01,server,dell/NoSuchModel\n"))
				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(problemsAbout(report, "dell/NoSuchModel")) != 1 {
					t.Fatalf("problems = %+v, want one quoting the path.\n"+
						"Leaving it unset would import the asset with no model and report "+
						"success -- and the expiry date it was supposed to inherit would "+
						"simply never appear.", report.Problems)
				}
			})

			t.Run("an asset's own date still beats the model's", func(t *testing.T) {
				rows, _ := ParseAssetCSV(strings.NewReader(
					"name,kind,device_type,eol_date\ncontracted-01,server,dell/PowerEdge R650,2031-12-31\n"))
				if report, err := s.ImportAssets(ctx, testActor, rows, false); err != nil {
					t.Fatalf("importing: %v", err)
				} else if len(report.Problems) > 0 {
					t.Fatalf("refused: %+v", report.Problems)
				}
				list, err := s.ListAssets(ctx, AssetFilter{Query: "contracted-01"})
				if err != nil || len(list) != 1 {
					t.Fatalf("reading back: %v", err)
				}
				if got := list[0].ResolvedEOL(); got == nil || *got != "2031-12-31" {
					t.Errorf("resolved EOL = %v, want the asset's own 2031-12-31", got)
				}
				if list[0].InheritedEOL() {
					t.Error("a date stated in the file is reported as inherited")
				}
			})

			t.Run("a retired model is not adopted from a file", func(t *testing.T) {
				gone := mustDeviceType(t, s, ctx, dell, "PowerEdge R500", nil)
				if err := s.RetireDeviceType(ctx, testPermit, gone); err != nil {
					t.Fatalf("retiring: %v", err)
				}
				rows, _ := ParseAssetCSV(strings.NewReader(
					"name,kind,device_type\nold-01,server,dell/PowerEdge R500\n"))
				report, err := s.ImportAssets(ctx, testActor, rows, false)
				if err != nil {
					t.Fatalf("importing: %v", err)
				}
				if len(report.Problems) == 0 {
					t.Error("a file re-adopted a model the estate has stopped buying. " +
						"A retired model is not offered in the picker either.")
				}
			})
		})
	}
}

// TestAnAmbiguousModelPathIsRefusedRatherThanGuessed covers the cost of folding
// case.
//
// The unique index on device_type is case-sensitive, so two models differing
// only in capitalisation can both exist. Folding case to be forgiving means
// admitting that, and the only honest answer is to refuse -- picking whichever
// came back first would resolve silently and wrongly half the time.
func TestAnAmbiguousModelPathIsRefusedRatherThanGuessed(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			dell := mustManufacturer(t, s, ctx, "dell", "Dell")
			mustDeviceType(t, s, ctx, dell, "PowerEdge R650", ptr("2029-03-31"))
			mustDeviceType(t, s, ctx, dell, "poweredge r650", ptr("2020-01-01"))

			rows, _ := ParseAssetCSV(strings.NewReader(
				"name,kind,device_type\nwhich-01,server,dell/PowerEdge R650\n"))
			report, err := s.ImportAssets(ctx, testActor, rows, false)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if len(problemsAbout(report, "more than one")) != 1 {
				t.Fatalf("problems = %+v, want one saying the path is ambiguous.\n"+
					"The two models carry different end-of-support dates, so guessing "+
					"picks the wrong answer half the time and says nothing.", report.Problems)
			}
		})
	}
}

func TestABatchedImportWritesTheWholeFile(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			rows := importCSV(t, importHeader+
				"dc-a/rack-1,esx-01,hypervisor\n"+
				"dc-a,rack-1,rack\n"+
				",dc-a,site\n")

			report, err := s.ImportAssetsBatched(ctx, domain.AdministratorPermit(testActor), rows, nil)
			if err != nil {
				t.Fatalf("importing: %v", err)
			}
			if len(report.Problems) > 0 {
				t.Fatalf("a valid file was refused: %+v", report.Problems)
			}
			if len(report.Created) != 3 {
				t.Fatalf("created %v, want 3 paths", report.Created)
			}
			if report.PartialRows != 0 {
				t.Errorf("PartialRows = %d, want 0", report.PartialRows)
			}
			// Parents before children, across batches: the child must have found
			// its parent's id.
			list, err := s.ListAssets(ctx, AssetFilter{Query: "esx-01"})
			if err != nil || len(list) != 1 {
				t.Fatalf("reading back: %v", err)
			}
			if list[0].ParentID == nil {
				t.Error("the child was written with no parent; depth ordering is what " +
					"guarantees a parent is committed before its child")
			}

			// CREATE ONLY, on this path too. The rule was tested against the
			// preview path and not against the one that actually writes -- caught
			// by mutating the batched check and watching everything stay green.
			again, err := s.ImportAssetsBatched(ctx, domain.AdministratorPermit(testActor),
				importCSV(t, importHeader+"dc-a,rack-1,rack\n"), nil)
			if err != nil {
				t.Fatalf("re-importing: %v", err)
			}
			if len(problemsAbout(again, "already exists")) != 1 {
				t.Fatalf("problems = %+v, want one saying it already exists", again.Problems)
			}
			if again.PartialRows != 0 {
				t.Errorf("PartialRows = %d; a file refused before any write leaves nothing "+
					"half-done", again.PartialRows)
			}

			// And re-running a file whose rows ALL already exist is how somebody
			// recovers from a partial import, so it must refuse rather than break.
			list2, err := s.ListAssets(ctx, AssetFilter{})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(list2) != 3 {
				t.Errorf("estate has %d assets after a refused re-run, want 3", len(list2))
			}
		})
	}
}
