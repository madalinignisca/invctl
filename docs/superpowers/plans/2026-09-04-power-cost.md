# WP-I2 Power cost — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put an *estimated* electricity figure on `/reports/cost` — declared nameplate draw times a configured tariff — labelled as an estimate everywhere it appears, and impossible to mistake for the estate's priced total.

**Spec:** `docs/power-cost-design.md` (REVISED 2026-09-04, approved). §2 is the reasoning; it must survive into code comments. §4 is the six items. Nothing here re-opens a decision that document already took.

**Architecture:** No migration, no new entity, no new route. One config value, one read-only store query, the arithmetic in `internal/domain`, and one new partial rendered inside the existing `/reports/cost` page. The store returns the *raw* declared draw and its coverage counts; the tariff never enters `internal/store`, so the query is a fact about the estate and the money is applied one layer up, next to the config that supplies the rate.

**Tech stack:** Go (`go.mod` toolchain), `jmoiron/sqlx` with hand-written SQL, `html/template`, HTMX. No new dependency. No migration — this feature adds no table and no column, deliberately (D1).

---

## Global Constraints

These are the ones this feature can get wrong quietly. Read them before Task 1.

- **Every query runs unmodified on SQLite AND PostgreSQL.** `?` placeholders only, `sqlx.Rebind` via `s.read`/`s.readOne` before execution, never `$1`. No `SERIAL`, `ENUM`, native arrays, `jsonb` in `WHERE`, `NOW()`, `generate_series()`, multi-row `RETURNING`. The one derived table in Task 3 **must** carry an alias (`) per_asset`) — SQLite tolerates its absence, PostgreSQL does not.
- **`MAX(draw_va)` per asset, never `SUM`.** §2.1. `draw_va` is an ALLOCATION figure: a dual-fed server records the *whole* load on each side, because that is what feed sizing needs (`internal/store/power.go:179`). Summing per asset doubles every properly-redundant server in the estate. This is a decided constraint with a documented cost (a chassis with two genuinely independent rails reads low) — **do not "improve" it**.
- **Containment exclusion goes through `asset_closure`, `depth > 0`.** §2.2. An asset contributes only if no ancestor of it also declares a live draw. Never a recursive `parent_id` walk. The self-row is `depth = 0` (`internal/store/assets.go:1113`), so the predicate must exclude it or every drawing asset excludes itself.
- **Sum raw VA across the estate first, divide once at the end.** §4.3. Per-asset integer division truncates downward every time and erodes the figure silently — the same reason `domain.CostTotals` avoids per-line rounding. One `divRound` at the end, on the estate total.
- **The figure is NEVER added into `EstateCosts.Totals`.** §2.4. `store.EstateCostReport` is not touched by this work package at all — no new field, no new surface entry. Task 5's `TestThePowerFigureIsNotInTheEstateTotals` pins it at store level so a later refactor cannot fold it in.
- **Wording, from §2.3 and D5 — these are requirements, not copy suggestions:**
  - The word **"ceiling" must not appear**, in code, comment, template or test. It promises the real bill cannot exceed the figure, and it can (typed input, unmodelled UPS and distribution loss, excluded facility overhead).
  - The section must state, **beside the number and not in a footnote**, that it is **not comparable to an all-in hosting quote** without adding UPS/distribution loss and cooling/facility overhead.
  - The assumptions render beside the figure: declared load, power factor 1.0, 730 h/month, the tariff in force.
  - With no tariff configured, render the heading and the line **"No electricity figure: no tariff is configured."** — never nothing (D5).
  - Own heading tier, no shared grand-total styling with the estate totals. The power stat's labels must not collide with the estate stat's labels ("Per month"), and Task 5's test depends on that.
- **Lifecycle gating is decided, not copied** (§4.4): the **asset** and the **input** must be live. The feed and panel lifecycles do **not** gate it — follow `AssetsLosingPower`, not `PowerFindings`.
- **Nothing here mutates declared state**, so no `change_log` obligation and no `domain.Permit` parameter. This is a read-only report. It also touches no observed state.
- Every new file opens with the AGPL-3.0-only notice; in Go, a blank line before `package` (`internal/license` fails otherwise).
- **Gates:** `make lint` and `make test`, foreground, one at a time, exit status read directly. Never pipe either through `tail`/`head`/`grep`. `go test ./...` on its own is NOT the gate — with `INV_TEST_POSTGRES_DSN` unset the Postgres half is silently skipped.

### One thing to watch, not to fix

D3's denominator is "assets that could plausibly carry a draw" = live assets **not** contained inside a drawing asset. That includes sites and racks, which will never carry an input. The spec defines the denominator in exactly those words, so the plan implements exactly that; the coverage sentence should therefore say "assets", not "servers". If the resulting count reads as noise on a real estate, narrowing it by `asset.kind` is a **spec change** and goes back to the main conversation — not a tweak in the query.

---

## Task 1: The tariff in config

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.PowerTariffMinorPerKWh int64`. Task 5 consumes it.

- [ ] **Step 1: Add the field**

Beside `Currency`, because it follows that field's convention and is meaningless without it:

```go
	// PowerTariffMinorPerKWh is the electricity rate, in the same minor units
	// as every other amount here (docs/power-cost-design.md D1). Zero means no
	// tariff is configured and the cost report says so, rather than rendering
	// nothing -- an administrator who sees a blank section cannot tell "not
	// configured" from "nothing to show" or "I lack the permission" (D5).
	//
	// ONE RATE, IN CONFIG, and the alternatives were considered: a column on
	// power_source is per-supply rather than per-contract and would give
	// PARTIAL coverage, which for a cost figure is worse than none; a tariff
	// entity is CRUD, audit and UI for a number most estates have one of.
	// A second rate becomes a real requirement the day a second site is on a
	// different contract, and that is a work package, not a column.
	//
	// IT CARRIES NO CURRENCY OF ITS OWN. Currency above is estate-wide; a
	// second currency on one page is a bug, not a feature.
	PowerTariffMinorPerKWh int64
```

In `Load()`, beside `Currency`:

```go
		PowerTariffMinorPerKWh:      envInt64("INV_POWER_TARIFF_MINOR_PER_KWH", 0, &badInts),
```

- [ ] **Step 2: Parse it the way this file parses everything else — refusing to start on a value it cannot read**

`envBool` already collects unparseable values into `badBools` and refuses to start, on the argument that a fallback is the permissive one. The same argument applies here for a different reason: a mistyped tariff that silently falls back to zero produces a *page that says no tariff is configured* while the operator is looking at the variable they set.

```go
// envInt64 parses an integer, recording anything it could not parse.
//
// Same posture as envBool: a value somebody typed and got wrong must not
// degrade into the default. "0.28" is exactly the value an operator reaches
// for here -- the variable is named MINOR units, so the answer is 28 -- and
// silently taking 0 would render "no tariff is configured" on a page they had
// just configured.
func envInt64(key string, fallback int64, bad *[]string) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		*bad = append(*bad, fmt.Sprintf("%s=%q", key, v))
		return fallback
	}
	return parsed
}
```

Declare `var badInts []string` beside `badBools`, and extend the existing refusal block with a second, separately-worded one (do not fold ints into the bool message — the remedy differs):

```go
	if len(badInts) > 0 {
		return nil, fmt.Errorf("validating config: %s is not a whole number of minor "+
			"currency units; a rate of 0.28 is written as 28. Refusing to start rather "+
			"than falling back to a default, because the default renders as "+
			"\"no tariff is configured\" on a page somebody has just configured",
			strings.Join(badInts, ", "))
	}
```

In `validate()`, refuse a negative rate:

```go
	if c.PowerTariffMinorPerKWh < 0 {
		return fmt.Errorf("validating config: INV_POWER_TARIFF_MINOR_PER_KWH is %d; "+
			"a negative tariff would report the estate as earning money by being "+
			"switched on", c.PowerTariffMinorPerKWh)
	}
```

Zero stays legal and means *unset* — record why, next to the field:

> A tariff of zero is treated as unset rather than as free electricity. Nobody has free electricity, and rendering `€0.00` beside "per month" as though it were computed is exactly the measured-looking figure this design refuses.

- [ ] **Step 3: Tests**

Add `"INV_POWER_TARIFF_MINOR_PER_KWH"` to `pristineEnv`'s key list (it is the same trap the list's own comment describes).

```go
// TestThePowerTariffIsUnsetByDefaultAndRefusesRubbish. The default matters:
// an unset tariff is what makes the cost report render D5's explanation
// rather than a figure, and a default of anything else would put a number
// nobody chose in front of a reader.
func TestThePowerTariffIsUnsetByDefaultAndRefusesRubbish(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		pristineEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.PowerTariffMinorPerKWh != 0 {
			t.Errorf("tariff = %d, want 0 (unset)", cfg.PowerTariffMinorPerKWh)
		}
	})

	t.Run("set", func(t *testing.T) {
		pristineEnv(t)
		t.Setenv("INV_POWER_TARIFF_MINOR_PER_KWH", "28")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.PowerTariffMinorPerKWh != 28 {
			t.Errorf("tariff = %d, want 28", cfg.PowerTariffMinorPerKWh)
		}
	})

	// The value an operator actually types when they read "per kWh" and think
	// in major units. It must refuse to start, not quietly become zero and
	// report itself unconfigured on a page they just configured.
	for _, bad := range []string{"0.28", "28 cents", "-"} {
		t.Run("refuses "+bad, func(t *testing.T) {
			pristineEnv(t)
			t.Setenv("INV_POWER_TARIFF_MINOR_PER_KWH", bad)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted INV_POWER_TARIFF_MINOR_PER_KWH=%q", bad)
			}
		})
	}

	t.Run("refuses a negative rate", func(t *testing.T) {
		pristineEnv(t)
		t.Setenv("INV_POWER_TARIFF_MINOR_PER_KWH", "-28")
		if _, err := Load(); err == nil {
			t.Fatal("Load accepted a negative tariff")
		}
	})
}
```

---

## Task 2: The arithmetic, in `domain`

**Files:**
- Create: `internal/domain/power_cost.go`
- Create: `internal/domain/power_cost_test.go`

**Interfaces:**
- Produces: `domain.DeclaredDraw`, `domain.PowerEstimate`, `domain.PowerHoursPerMonth`. Task 3 returns the first; Task 5 builds the second and the template reads it.
- Consumes: nothing. `internal/domain` has zero external dependencies and this file adds none — it uses `strconv` and the package's existing unexported `divRound`.

Why here and not in the store: the store answers "what does the estate declare"; the tariff is configuration and the conversion is a documented set of assumptions. Putting the assumptions in `domain` gives them a home that can be unit-tested with no database and no HTTP, which is where the rounding argument in §4.3 is actually checkable.

- [ ] **Step 1: The types and the conversion**

```go
// Power cost is an ESTIMATE, and this file is where the assumptions live so
// that every one of them can be named on the page (docs/power-cost-design.md).
//
// Nothing here is measured. Nothing in this system touches the estate, and a
// metered draw would be observed state with a reporter and an age -- a
// different contract entirely (docs/AUDIT.md).

// PowerHoursPerMonth is the mean hours in a month: 8,760 / 12 = 730.
//
// A constant, but NOT a hidden one -- the report states it beside the figure
// (§4.3). A reader who cannot see the multiplier cannot check the arithmetic,
// and an estimate nobody can check is an estimate nobody should believe.
const PowerHoursPerMonth = 730

// DeclaredDraw is what the estate SAYS it draws, and how much of the estate
// said anything at all.
//
// The two counts travel with the figure for the same reason EstateCostSurface
// keeps Priced beside Totals: a total over a fifth of the estate and a total
// over all of it are different sentences, and a caller able to take the first
// without the second is a caller who will.
type DeclaredDraw struct {
	// TotalVA is the sum over assets of the MAXIMUM draw declared on any one
	// of an asset's inputs -- never the sum of its inputs.
	//
	// THIS IS THE WHOLE POINT AND THE FIRST DRAFT GOT IT BACKWARDS. draw_va is
	// an ALLOCATION figure, not a demand figure: each side of a dual-fed
	// server records the WHOLE load, because a feed is correctly sized only if
	// it can carry its partner's entire load when the other side dies. Summing
	// per asset returns 1,800 VA for a 900 VA server. MAX is right for a
	// redundantly-fed asset (the norm in this estate -- the false-redundancy
	// report exists because it is) and trivially right for a single-fed one.
	//
	// It is wrong for a chassis with two genuinely independent rails feeding
	// different components, where the real draw is the sum; that reads low.
	// Accepted, and the smaller error: rarer than redundant feeding, and
	// understating one unusual chassis beats doubling every correct server.
	// A real per-asset demand figure is a NEW DECLARED FIELD with a migration,
	// a form and an audit surface -- not a smarter query over this column.
	TotalVA int64
	// Declaring is how many assets contributed a figure; Qualifying is how
	// many could have. The difference is what nobody has written down.
	//
	// Qualifying excludes an asset contained inside another that declares a
	// draw: its power is already inside its host's wall draw, so counting it
	// as "failed to declare" would make the coverage figure meaningless, and
	// coverage exists to make the rest honest.
	Declaring  int
	Qualifying int
}

// NoDraw is how many qualifying assets declared nothing.
func (d DeclaredDraw) NoDraw() int { return d.Qualifying - d.Declaring }

// PowerEstimate is the declared draw plus the rate, and every assumption in
// between. It carries no currency of its own -- Config.Currency is estate-wide.
type PowerEstimate struct {
	Draw              DeclaredDraw
	TariffMinorPerKWh int64
}

// Configured reports whether a tariff is in force. Zero is unset rather than
// free: nobody has free electricity, and rendering a computed-looking 0.00
// is the measured-looking figure this design refuses.
func (e PowerEstimate) Configured() bool { return e.TariffMinorPerKWh > 0 }

// HoursPerMonth exposes the multiplier to the template, so the page can state
// it rather than imply it.
func (e PowerEstimate) HoursPerMonth() int { return PowerHoursPerMonth }

// MonthlyMinor is the estimated monthly electricity cost.
//
// VA x power factor x hours / 1000 = kWh, and kWh x tariff = money. Power
// factor is assumed 1.0 (VA treated as W), which is conservative FOR THAT STEP
// ONLY -- real power cannot exceed apparent power at the same measurement
// point. It does NOT make the end-to-end figure an upper bound: the input is a
// typed number, the UPS and distribution losses above the declared input are
// unmodelled, and facility overhead is excluded on purpose. Do not call this a
// ceiling; it has not earned the word.
//
// ONE DIVISION, AT THE END, over the estate's summed VA. Dividing per asset
// truncates downward every time and would erode the figure silently -- the
// same reason CostTotals refuses per-line rounding.
//
// Overflow is not a risk at estate scale: a million VA at a EUR 1.00/kWh
// tariff is 7.3e10, eight orders below int64's limit.
func (e PowerEstimate) MonthlyMinor() int64 {
	return divRound(e.Draw.TotalVA*PowerHoursPerMonth*e.TariffMinorPerKWh, 1000)
}

// KWhPerMonthTenths is the energy behind the money, in tenths of a kWh.
//
// Integer tenths rather than a float, for the reason EstateCostSurface.Coverage
// gives: this is a figure for a human to read on a page that is otherwise
// exact, and a float would be the only inexact thing on it.
func (e PowerEstimate) KWhPerMonthTenths() int64 {
	return divRound(e.Draw.TotalVA*PowerHoursPerMonth*10, 1000)
}

// KWhPerMonth renders the energy for the page: "6570.0".
//
// Rendered here rather than in a template helper, and deliberately WITHOUT
// thousands separators -- the money helper in internal/web/render groups its
// output and this does not, which keeps the two visually distinct. That is a
// small win rather than an oversight: the one failure mode this design is
// arranged against is a reader adding a derived figure to a declared one.
func (e PowerEstimate) KWhPerMonth() string {
	tenths := e.KWhPerMonthTenths()
	return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10)
}
```

- [ ] **Step 2: Tests — the rounding claim is the one worth testing**

```go
// TestTheEstimateDividesOnceAtTheEnd is §4.3 as a test. Per-asset division
// truncates downward every time, always in the same direction, which is the
// kind of error that survives review because it looks like rounding.
//
// Five assets of 1 VA each at 28 minor/kWh: per-asset arithmetic gives
// divRound(1*730*28, 1000) = divRound(20440, 1000) = 20, five times = 100.
// Summed first: divRound(5*730*28, 1000) = divRound(102200, 1000) = 102.
// The two differ, which is what makes the test worth having.
func TestTheEstimateDividesOnceAtTheEnd(t *testing.T) {
	summedFirst := PowerEstimate{
		Draw:              DeclaredDraw{TotalVA: 5, Declaring: 5, Qualifying: 5},
		TariffMinorPerKWh: 28,
	}.MonthlyMinor()

	var perAsset int64
	for i := 0; i < 5; i++ {
		perAsset += PowerEstimate{
			Draw:              DeclaredDraw{TotalVA: 1},
			TariffMinorPerKWh: 28,
		}.MonthlyMinor()
	}

	if summedFirst == perAsset {
		t.Fatalf("summed-first and per-asset arithmetic both gave %d; this fixture no "+
			"longer distinguishes them, so it proves nothing about the rule it guards",
			summedFirst)
	}
	if summedFirst != 102 {
		t.Errorf("MonthlyMinor = %d, want 102 -- the estate's VA must be summed raw "+
			"and divided once", summedFirst)
	}
}

func TestThePowerEstimateArithmetic(t *testing.T) {
	tests := []struct {
		name        string
		va          int64
		tariff      int64
		wantMonthly int64
		wantKWh     string
	}{
		// 900 VA, 730 h -> 657 kWh; at 28 minor/kWh -> 18,396 minor.
		{"one dual-fed server", 900, 28, 18396, "657.0"},
		{"nothing declared", 0, 28, 0, "0.0"},
		{"no tariff", 900, 0, 0, "657.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := PowerEstimate{Draw: DeclaredDraw{TotalVA: tc.va}, TariffMinorPerKWh: tc.tariff}
			if got := e.MonthlyMinor(); got != tc.wantMonthly {
				t.Errorf("MonthlyMinor = %d, want %d", got, tc.wantMonthly)
			}
			if got := e.KWhPerMonth(); got != tc.wantKWh {
				t.Errorf("KWhPerMonth = %q, want %q", got, tc.wantKWh)
			}
		})
	}
}

// TestAZeroTariffIsUnsetRatherThanFree. Zero renders D5's explanation, not a
// computed-looking EUR 0.00 beside "per month".
func TestAZeroTariffIsUnsetRatherThanFree(t *testing.T) {
	if (PowerEstimate{Draw: DeclaredDraw{TotalVA: 900}}).Configured() {
		t.Error("a zero tariff reported itself configured")
	}
	if !(PowerEstimate{TariffMinorPerKWh: 1}).Configured() {
		t.Error("a one-minor-unit tariff reported itself unconfigured")
	}
}

func TestNoDrawIsTheGapBetweenDeclaringAndQualifying(t *testing.T) {
	d := DeclaredDraw{TotalVA: 900, Declaring: 3, Qualifying: 11}
	if d.NoDraw() != 8 {
		t.Errorf("NoDraw = %d, want 8", d.NoDraw())
	}
}
```

---

## Task 3: The query

**Files:**
- Create: `internal/store/power_cost.go`

**Interfaces:**
- Produces: `func (s *SQLStore) DeclaredPowerDraw(ctx context.Context) (domain.DeclaredDraw, error)`. Task 5 calls it.
- Consumes: `domain.DeclaredDraw` (Task 2), the existing `asset`, `power_input` and `asset_closure` tables. No migration.

- [ ] **Step 1: Write the file**

```go
// What the estate declares it draws, and how much of the estate said anything.
//
// SEPARATE FROM estate_costs.go ON PURPOSE. That file totals what somebody
// PRICED; this is a figure this system DERIVED from a nameplate and a rate. A
// derived figure entering EstateCosts.Totals would make it part-declared and
// part-derived with no way to tell which, so the two never meet in one struct
// (docs/power-cost-design.md §2.4). Keeping them apart stops the ARITHMETIC
// contamination; the page's own layout is what stops a reader adding them.

// DeclaredPowerDraw sums the estate's declared load and counts what it could
// not see.
//
// THREE THINGS ARE LOAD-BEARING HERE AND EACH ONE FIXES A DOUBLE-COUNT OR A
// LIE:
//
//  1. MAX(draw_va) PER ASSET, NOT SUM. draw_va is an allocation figure: both
//     sides of a dual-fed server record the WHOLE load, because a feed is
//     correctly sized only if it can carry its partner's entire load when the
//     other side dies (power.go's allocated_va, and the fixture says so in as
//     many words at seed_hardware.go:207). SUM would return 1,800 VA for a
//     900 VA server -- a 100% overstatement across every properly-redundant
//     asset in the estate. Nothing in the schema distinguishes "my half" from
//     "the whole load recorded twice", so no aggregation over this column can
//     tell them apart; the information is not there to be recovered.
//
//  2. AN ASSET INSIDE A DRAWING ASSET CONTRIBUTES NOTHING. power_input.asset_id
//     has no kind restriction, so a VM can declare its own input through the
//     same form every asset gets. A hypervisor at 900 and a VM inside it at 100
//     is 1,000 by any naive query, even though the VM's power is virtual and
//     already in the host's wall draw. Closed through asset_closure -- never a
//     recursive parent_id walk -- so it costs one correlated subquery and does
//     not depend on nobody entering the data.
//
//  3. LIFECYCLE GATES THE ASSET AND THE INPUT, AND NOTHING ELSE. Decided, not
//     copied: PowerFindings filters feed and panel too, because a FINDING is
//     about the supply path. A retired feed under a running server is a data
//     inconsistency, not a reason to believe the server stopped drawing power.
//     AssetsLosingPower already filters exactly these two; this follows it.
//
// ONE QUERY, NOT TWO. The numerator and the denominator come out of the same
// scan, so they cannot disagree -- two statements could straddle a concurrent
// write and report more assets declaring a draw than assets that exist.
func (s *SQLStore) DeclaredPowerDraw(ctx context.Context) (domain.DeclaredDraw, error) {
	var row struct {
		TotalVA    int64 `db:"total_va"`
		Declaring  int   `db:"declaring"`
		Qualifying int   `db:"qualifying"`
	}
	// COUNT(per_asset.max_draw) counts the non-NULL ones: an asset whose only
	// live inputs declare nothing has MAX() of NULL, so it lands in qualifying
	// and not in declaring, which is exactly the "unknown, and counted"
	// distinction the rest of this codebase keeps (Rating's nullable fields,
	// EstateCostSurface.Unpriced).
	//
	// The derived table's alias is not decoration: PostgreSQL rejects a
	// subquery in FROM without one, and SQLite accepts it -- which is the
	// shape of every dual-engine defect this repo has had.
	err := s.readOne(ctx, &row, `
		SELECT COALESCE(SUM(per_asset.max_draw), 0) AS total_va,
		       COUNT(per_asset.max_draw)            AS declaring,
		       COUNT(*)                             AS qualifying
		FROM (
			SELECT a.id AS asset_id, MAX(i.draw_va) AS max_draw
			FROM asset a
			LEFT JOIN power_input i
			       ON i.asset_id = a.id AND i.lifecycle <> ?
			WHERE a.lifecycle <> ?
			  AND NOT EXISTS (
			      SELECT 1
			      FROM asset_closure c
			      JOIN power_input pi ON pi.asset_id = c.ancestor_id
			      JOIN asset pa       ON pa.id = c.ancestor_id
			      WHERE c.descendant_id = a.id
			        AND c.depth > 0
			        AND pi.draw_va IS NOT NULL
			        AND pi.lifecycle <> ?
			        AND pa.lifecycle <> ?
			  )
			GROUP BY a.id
		) per_asset`,
		domain.LifecycleRetired, domain.LifecycleRetired,
		domain.LifecycleRetired, domain.LifecycleRetired)
	if err != nil {
		return domain.DeclaredDraw{}, fmt.Errorf("summing the estate's declared power draw: %w", err)
	}
	return domain.DeclaredDraw{
		TotalVA:    row.TotalVA,
		Declaring:  row.Declaring,
		Qualifying: row.Qualifying,
	}, nil
}
```

**`c.depth > 0` is the self-row exclusion.** `insertClosureForNewNode` writes `(id, id, 0)` for every asset, so without it every drawing asset would be its own drawing ancestor and the estate total would be zero. A test in Task 4 fails loudly if it is dropped.

- [ ] **Step 2: Confirm both engines accept the statement before writing a line of the handler**

```bash
make test 2>&1 | tail -0; echo "run the store package alone first:"
INV_TEST_POSTGRES_DSN=... go test ./internal/store/ -run TestTheDeclaredDraw -count=1
```
(Use whatever `make test` sets for the Postgres DSN — the point is that this query is proven on PostgreSQL before anything is built on top of it, not after.)

---

## Task 4: Store tests — containment, coverage, lifecycle, and the isolation of the estate total

**Files:**
- Create: `internal/store/power_cost_test.go`

**Interfaces:**
- Consumes: `DeclaredPowerDraw` (Task 3), the existing `mustAsset`/`mustPanel`/`mustFeed`/`mustInput` helpers (`store_test.go`, `power_test.go`) and `intPtr` (`search_test.go`).

Every test loops `Engines(t)` — this is the dual-engine half of the evidence.

- [ ] **Step 1: The containment test, with the arrangement that makes it reachable**

The case is only reachable because `power_input.asset_id` has no kind restriction, so it has to be *built*: a VM parented to a hypervisor (which gives it `asset_closure` rows through `CreateAsset`), each declaring its own input.

```go
// TestAContainedDrawDoesNotAddToTheEstateTotal is §2.2.
//
// THE ARRANGEMENT IS THE TEST. power_input.asset_id is REFERENCES asset(id)
// with no kind restriction (00023_power.sql), and neither NewPowerInput nor
// the handler nor the asset-detail form limits which kinds may carry one -- so
// a VM declaring its own draw is not a hypothetical, it is what the current UI
// permits. A hypervisor at 900 with a VM inside it at 100 is 1,000 by any
// naive query, even though the VM's power is virtual and already inside the
// host's wall draw.
func TestAContainedDrawDoesNotAddToTheEstateTotal(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)

			// The host declares its wall draw.
			host := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", &site)
			mustInput(t, s, ctx, host, feed, "A", intPtr(900))

			// The guest declares one too -- parented to the host, so
			// CreateAsset writes the asset_closure rows the exclusion reads.
			guest := mustAsset(t, s, ctx, domain.KindVM, "vm-01", &host)
			mustInput(t, s, ctx, guest, feed, "A", intPtr(100))

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 900 {
				t.Errorf("TotalVA = %d, want 900 -- the VM's 100 VA is already inside "+
					"the hypervisor's wall draw", draw.TotalVA)
			}
			if draw.Declaring != 1 {
				t.Errorf("Declaring = %d, want 1 -- only the host contributed", draw.Declaring)
			}
			// And the guest is not counted as a GAP either (D3): counting every
			// VM as "failed to declare a draw" would make coverage meaningless,
			// and coverage is what makes the rest of the figure honest. The
			// site qualifies and declares nothing, which is the denominator
			// the spec defines.
			if draw.Qualifying != 2 {
				t.Errorf("Qualifying = %d, want 2 (the site and the host; the guest is "+
					"excluded, not counted as a gap)", draw.Qualifying)
			}
		})
	}
}

// TestADrawingAssetIsNotItsOwnDrawingAncestor is the depth-0 guard. The
// closure table carries a self-row for every asset (assets.go's
// insertClosureForNewNode), so an exclusion written without `depth > 0`
// excludes EVERY drawing asset and reports an estate that draws nothing --
// a failure that looks like an empty estate rather than a broken query.
func TestADrawingAssetIsNotItsOwnDrawingAncestor(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			host := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, host, feed, "A", intPtr(450))

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 450 {
				t.Fatalf("TotalVA = %d, want 450 -- an asset excluded by its own "+
					"closure self-row reports an estate that draws nothing", draw.TotalVA)
			}
		})
	}
}
```

- [ ] **Step 2: MAX per asset, and unknown-but-counted**

```go
// TestTwoInputsOnOneAssetCountOnce is §2.1 at store level -- the seed-fixture
// regression in internal/seed is the primary one, this is the dual-engine half.
func TestTwoInputsOnOneAssetCountOnce(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustPanel(t, s, ctx, site, "DB-A")
			pb := mustPanel(t, s, ctx, site, "DB-B")
			fa := mustFeed(t, s, ctx, pa, "A1", 230, 32)
			fb := mustFeed(t, s, ctx, pb, "B1", 230, 32)

			// Properly 2N: the whole load recorded on each side, because that
			// is what feed sizing needs.
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, srv, fa, "A", intPtr(900))
			mustInput(t, s, ctx, srv, fb, "B", intPtr(900))

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA == 1800 {
				t.Fatalf("TotalVA = 1800: the query is summing a dual-fed asset's inputs, " +
					"which doubles every properly-redundant server in the estate")
			}
			if draw.TotalVA != 900 {
				t.Errorf("TotalVA = %d, want 900", draw.TotalVA)
			}
		})
	}
}

// TestAnUnknownDrawIsExcludedFromTheFigureAndCountedInCoverage is D3.
// "Not recorded" must stay distinguishable from zero -- the same rule
// Rating's nullable fields and EstateCostSurface.Unpriced already keep.
func TestAnUnknownDrawIsExcludedFromTheFigureAndCountedInCoverage(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)

			declared := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, declared, feed, "A", intPtr(450))

			// An input, but nobody typed a figure into it.
			silent := mustAsset(t, s, ctx, domain.KindServer, "srv-2", &site)
			mustInput(t, s, ctx, silent, feed, "A", nil)

			// No input at all -- also a gap, and the commoner one.
			mustAsset(t, s, ctx, domain.KindServer, "srv-3", &site)

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 450 {
				t.Errorf("TotalVA = %d, want 450 -- an unknown draw must not be read "+
					"as a zero one", draw.TotalVA)
			}
			if draw.Declaring != 1 {
				t.Errorf("Declaring = %d, want 1", draw.Declaring)
			}
			// site + three servers.
			if draw.Qualifying != 4 {
				t.Errorf("Qualifying = %d, want 4", draw.Qualifying)
			}
			if draw.NoDraw() != 3 {
				t.Errorf("NoDraw = %d, want 3", draw.NoDraw())
			}
		})
	}
}
```

- [ ] **Step 3: The lifecycle decision (§4.4), including the half that must NOT filter**

```go
// TestLifecycleGatesTheAssetAndTheInputAndNothingElse is §4.4, and the second
// half is the one worth having: a retired FEED under a running server is a
// data inconsistency, not a reason to believe the server stopped drawing
// power. PowerFindings filters all four because a finding is about the supply
// path; this follows AssetsLosingPower instead, which filters exactly two.
func TestLifecycleGatesTheAssetAndTheInputAndNothingElse(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			live := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			doomed := mustFeed(t, s, ctx, panel, "F2", 230, 32)

			keeps := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, keeps, doomed, "A", intPtr(450))

			retiredInput := mustAsset(t, s, ctx, domain.KindServer, "srv-2", &site)
			gone := mustInput(t, s, ctx, retiredInput, live, "A", intPtr(700))

			// Retire the FEED the first server hangs off. Its draw must stay.
			if err := s.RetirePowerFeed(ctx, testPermit, doomed); err != nil {
				t.Fatalf("retiring the feed: %v", err)
			}
			// Retire the second server's INPUT. Its draw must go.
			if err := s.RetirePowerInput(ctx, testPermit, gone); err != nil {
				t.Fatalf("retiring the input: %v", err)
			}

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 450 {
				t.Errorf("TotalVA = %d, want 450 -- a retired input drops out, a retired "+
					"feed under a running server does not", draw.TotalVA)
			}
		})
	}
}
```

**Verify before writing:** `RetirePowerFeed` may refuse while the feed still carries live inputs (`RetirePowerPanel` does, for feeds). If it does, use `UpdatePowerFeed` with `Lifecycle = domain.LifecycleRetired` instead, or retire a feed with no inputs and move the input onto it — do not weaken the assertion. Check `internal/store/power.go` before writing this test body.

- [ ] **Step 4: The estate total stays clean (§2.4), pinned at store level**

```go
// TestThePowerFigureIsNotInTheEstateTotals is §2.4 where it cannot be reworded
// away. The page's layout is what stops a HUMAN adding the two figures; this
// is what stops the CODE doing it -- a later refactor that folds power into
// EstateCosts would make that total part-declared and part-derived with no way
// for a reader to tell which half they were quoting.
func TestThePowerFigureIsNotInTheEstateTotals(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)

			before, err := s.EstateCosts(ctx, s.Now())
			if err != nil {
				t.Fatalf("estate costs before: %v", err)
			}

			mustInput(t, s, ctx, srv, feed, "A", intPtr(900))

			after, err := s.EstateCosts(ctx, s.Now())
			if err != nil {
				t.Fatalf("estate costs after: %v", err)
			}
			if before.Totals != after.Totals {
				t.Errorf("declaring a power draw moved the estate totals from %+v to %+v; "+
					"an estimate must never enter a total of what somebody priced",
					before.Totals, after.Totals)
			}

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 900 {
				t.Fatalf("TotalVA = %d, want 900 -- without this the comparison above "+
					"passes because nothing was declared at all", draw.TotalVA)
			}
		})
	}
}
```

---

## Task 5: The seed-fixture regression — the reason this spec was rewritten

**Files:**
- Create: `internal/seed/power_cost_test.go`

**Interfaces:**
- Consumes: `eachEngine`/`fixture` (`internal/seed/seed_test.go`), `seed.Permit`, `store.PowerInputsFor`, `store.RetirePowerInput`, `DeclaredPowerDraw` (Task 3).

`internal/seed`'s suite is SQLite-only by deliberate scoping (see `seed_test.go`'s comment: the seed writes no SQL of its own). The dual-engine proof of the same rule is Task 4 Step 2; this one exists because §4.6 requires the regression to run against **the real fixture**, and package `store`'s tests cannot import `seed` without a cycle.

- [ ] **Step 1: The test, written as a delta so it cannot rot**

```go
// TestTheFixtureCountsADualFedServerOnce is the regression the power-cost
// spec was rewritten for, and it is asserted against the SEEDED estate rather
// than a hypothetical one because the fixture is where the claim was checked:
//
//	// PROPERLY 2N: one lead per side. Converges only at the generator, which
//	// is the design and must not read as a fault.
//	{"hv-02", "DB-A/A2", "A", 900},
//	{"hv-02", "DB-B/B1", "B", 900},
//
// A ~900 VA server with 900 declared on EACH side, because each side must be
// able to carry the whole load when the other dies. The first draft of the
// design summed per asset and would report 1,800 for this box -- the identical
// 100% overstatement it existed to prevent, moved from feed scope to asset
// scope.
//
// WRITTEN AS A DELTA, not as a hardcoded estate total. A total pinned to
// 3,170 VA would fail the next time somebody adds a row to the fixture, and
// would be "fixed" by editing the number -- which is how a regression test
// stops guarding anything. Retiring one of hv-02's two inputs must change
// NOTHING (that is MAX); retiring both must remove exactly 900 (that is the
// contribution).
func TestTheFixtureCountsADualFedServerOnce(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		hv2 := f.refs.Assets["hv-02"]
		if hv2 == "" {
			t.Fatal("the fixture has no hv-02; it is the properly-2N box this test is about")
		}
		inputs, err := s.PowerInputsFor(ctx, hv2)
		if err != nil {
			t.Fatalf("reading hv-02's inputs: %v", err)
		}
		if len(inputs) != 2 {
			t.Fatalf("hv-02 has %d inputs, want 2 -- the fixture no longer demonstrates "+
				"a dual-fed asset, so this test proves nothing about MAX vs SUM", len(inputs))
		}
		for _, in := range inputs {
			if in.DrawVA == nil || *in.DrawVA != 900 {
				t.Fatalf("hv-02 input %s declares %v, want 900 on each side -- the whole "+
					"point is that BOTH sides carry the whole load", in.Name, in.DrawVA)
			}
		}

		before, err := s.DeclaredPowerDraw(ctx)
		if err != nil {
			t.Fatalf("summing declared draw: %v", err)
		}

		// Retire the B side. A SUM would drop 900 here; MAX drops nothing.
		if err := s.RetirePowerInput(ctx, seed.Permit, inputs[1].ID); err != nil {
			t.Fatalf("retiring hv-02's second input: %v", err)
		}
		oneSide, err := s.DeclaredPowerDraw(ctx)
		if err != nil {
			t.Fatalf("summing declared draw after retiring one side: %v", err)
		}
		if oneSide.TotalVA != before.TotalVA {
			t.Fatalf("retiring one of hv-02's two 900 VA inputs moved the estate total "+
				"from %d to %d VA; the query is SUMMING a dual-fed asset's inputs, which "+
				"doubles every properly-redundant server in the estate",
				before.TotalVA, oneSide.TotalVA)
		}

		// Retire the A side too. Now hv-02 contributes nothing, and the drop
		// is exactly what it was contributing: 900, not 1,800.
		if err := s.RetirePowerInput(ctx, seed.Permit, inputs[0].ID); err != nil {
			t.Fatalf("retiring hv-02's first input: %v", err)
		}
		none, err := s.DeclaredPowerDraw(ctx)
		if err != nil {
			t.Fatalf("summing declared draw after retiring both sides: %v", err)
		}
		if got := before.TotalVA - none.TotalVA; got != 900 {
			t.Errorf("hv-02 contributed %d VA to the estate total, want 900 -- it is a "+
				"900 VA server whose whole load is recorded on each of two sides", got)
		}
		// Coverage moves with it: the box stops declaring, it does not stop existing.
		if none.Declaring != before.Declaring-1 {
			t.Errorf("Declaring went from %d to %d, want a drop of exactly 1",
				before.Declaring, none.Declaring)
		}
		if none.Qualifying != before.Qualifying {
			t.Errorf("Qualifying moved from %d to %d; retiring an input does not retire "+
				"the asset, and the coverage denominator must say so",
				before.Qualifying, none.Qualifying)
		}
	})
}
```

**Verify before writing:** `store.PowerInputRow`'s field for the declared draw (`DrawVA *int` via the embedded `domain.PowerInput`) and its ordering guarantee. If `PowerInputsFor` does not order deterministically, sort by `Name` in the test rather than relying on insertion order.

- [ ] **Step 2: Prove the test can fail**

Temporarily change `MAX(i.draw_va)` to `SUM(i.draw_va)` in Task 3's query, run this test, watch it fail on the "retiring one of hv-02's two 900 VA inputs" branch, restore. A regression test that has never been seen red is a claim, not a check.

---

## Task 6: The handler and the section

**Files:**
- Modify: `internal/web/handlers/cost_report.go`
- Create: `web/templates/partials/power_cost.html`
- Modify: `web/templates/pages/cost_report.html`

**Interfaces:**
- Produces: `costReportPage.Power domain.PowerEstimate`, rendered by the new partial.
- Consumes: `config.Config.PowerTariffMinorPerKWh` (Task 1), `domain.PowerEstimate` (Task 2), `SQLStore.DeclaredPowerDraw` (Task 3).

- [ ] **Step 1: The handler**

```go
// powerTariff is the configured electricity rate, or zero.
//
// Guarded against a nil Config because the failure mode matters: App.Config is
// set by every real construction and by both test harnesses, so nil is a
// programming error -- but the page it would take down is otherwise fine, and
// "no tariff configured" is a truthful answer to give while somebody fixes the
// wiring. A panic here would lose the estate totals as well.
func (a *App) powerTariff() int64 {
	if a.Config == nil {
		return 0
	}
	return a.Config.PowerTariffMinorPerKWh
}
```

In `CostReport`:

```go
	var report *store.EstateCostReport
	var power domain.PowerEstimate
	if base.CanSeeCosts {
		var err error
		report, err = a.Store.EstateCosts(r.Context(), a.Store.Now())
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		// THE DRAW IS ONLY QUERIED WHEN THERE IS A RATE TO APPLY. With no
		// tariff the section renders one sentence saying so (D5) and no
		// figures at all, so scanning the estate for a draw nobody can price
		// is the same waste as computing EstateCosts for a viewer who may not
		// see it -- which is why that fetch is already conditional above.
		if tariff := a.powerTariff(); tariff > 0 {
			draw, err := a.Store.DeclaredPowerDraw(r.Context())
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			power = domain.PowerEstimate{Draw: draw, TariffMinorPerKWh: tariff}
		}
	}
	a.Render.Respond(w, r, http.StatusOK, "cost_report", "cost_report", costReportPage{
		Base:   base,
		Report: report,
		Power:  power,
	})
```

```go
type costReportPage struct {
	Base
	Report *store.EstateCostReport
	// Power is the ESTIMATED electricity figure, and it is a separate field
	// rather than a surface inside Report on purpose: EstateCostReport totals
	// what somebody PRICED, and a derived figure inside it would make that
	// total part-declared and part-derived with no way to tell which
	// (docs/power-cost-design.md §2.4). The zero value is the unconfigured
	// state and renders D5's explanation, never nothing.
	Power domain.PowerEstimate
}
```

- [ ] **Step 2: The partial**

`web/templates/partials/power_cost.html`, with the licence comment block, defining `{{define "power_cost"}}`. It renders from a `domain.PowerEstimate` (`{{template "power_cost" .Power}}`), so it is standalone — the house rule for partials.

Required content, all of it load-bearing:

```html
<div class="panel" id="power-cost">
  <div class="panel-head">
    <h3>Electricity, estimated</h3>
    <span class="panel-note">declared load — nothing here is measured</span>
  </div>
  {{if not .Configured}}
  <div class="empty">
    <strong>No electricity figure: no tariff is configured.</strong>
    Set <code>INV_POWER_TARIFF_MINOR_PER_KWH</code> to the rate in minor units
    per kWh — 28 for 0.28 — and this section shows what the declared load costs.
  </div>
  {{else}}
  <span data-money-marker="partials/power_cost.html" hidden></span>
  <div class="stat-row">
    <div class="stat">
      <div class="stat-label">Electricity, estimated monthly</div>
      <div class="stat-value">{{money .MonthlyMinor}}</div>
      <div class="stat-note">
        {{.KWhPerMonth}} kWh at {{money .TariffMinorPerKWh}} per kWh
      </div>
    </div>
  </div>
  <div class="panel-body">
    <p>
      An estimate from <strong>declared</strong> nameplate draw. Nothing in this
      system measures power: {{.Draw.Declaring}} of {{.Draw.Qualifying}} assets
      declare a draw and {{.Draw.NoDraw}} declare none, so whatever those draw
      is not in the figure. Power factor is assumed 1.0 (volt-amps treated as
      watts) over {{.HoursPerMonth}} hours a month.
    </p>
    <p>
      <strong>Not comparable to an all-in hosting quote.</strong> It excludes
      loss in the UPS and distribution above each declared input, and excludes
      cooling and other facility overhead — a hosting price bundles all of
      those in. Add them before comparing.
    </p>
  </div>
  {{end}}
</div>
```

Rules this markup is obeying, and which a reviewer should check line by line:

- `<h3>`, not `<h2>`: **its own heading tier**, below the estate's panels (§2.4).
- **No `stat-row` shared with the estate totals**, and the label is "Electricity, estimated monthly" — never "Per month". Two figures under identical labels on one page is the human half of the contamination this design is arranged against, and Task 7's test reads the estate's "Per month" label by name.
- The assumptions and the not-comparable line are **in the section, beside the number** — not a footnote, not a tooltip, not a help page.
- The word "ceiling" appears nowhere.
- The coverage sentence names both counts, the way `EstateCostSurface` does.
- **Item 3 marker** (`data-money-marker`) sits in the branch that actually renders money, per `money_visibility_test.go`'s convention. A hidden `<span>`, not an HTML comment — `html/template` strips static comments.

- [ ] **Step 3: Wire it into the page**

In `web/templates/pages/cost_report.html`, inside the `{{define "cost_report"}}` block's `CanSeeCosts` branch, **after** the "What dominates the run rate" panel and separated from it:

```html
{{/* THE ESTIMATE, LAST AND SEPARATE. Everything above is money somebody
     typed against a thing they bought; this is a figure this system derived
     from a nameplate and a rate, and it is never added to the totals above
     (docs/power-cost-design.md §2.4). Its own heading tier and its own
     assumptions, rendered beside it rather than footnoted, because a reader
     who takes the number without the caveat is the failure mode. */}}
<hr class="section-break">
{{template "power_cost" .Power}}
```

Check `web/src/app.css` for an existing separator class before inventing `section-break`; reuse what the codebase has rather than adding a rule. If nothing suits, adding one small class to `web/src/app.css` is in scope (note: `web/static/app.css` is generated output — never edit it).

---

## Task 7: Web tests, and joining the money-census guard

**Files:**
- Modify: `internal/web/web_test.go` (a harness that configures a tariff)
- Modify: `internal/web/money_visibility_test.go` (the census table)
- Create: `internal/web/power_cost_test.go`

**Interfaces:**
- Consumes: the route from Task 6, the census guard's `moneyRouteCoverage`/`moneyRenderingTemplates`.

`TestEveryMoneyTemplateHasABehaviouralRoute` asserts **equality** between the templates that call `money` and the templates the coverage table claims. `partials/power_cost.html` calls `money`, so it must be added — and the route's fixture must actually render it *with money*, or the marker check fails. That needs a harness with a tariff, which the default one deliberately does not have.

- [ ] **Step 1: A harness that has a tariff**

In `web_test.go`, rename the body of `newHarnessSecure` to `newHarnessTuned(t, creds, readerCreds, secure bool, tariffMinorPerKWh int64)`, set the field in the cfg literal, and keep the existing entry points as thin wrappers:

```go
func newHarnessSecure(t *testing.T, creds []config.AgentCredential, readerCreds []config.ReaderCredential, secure bool) *harness {
	t.Helper()
	return newHarnessTuned(t, creds, readerCreds, secure, 0)
}

// newHarnessWithTariff is the same fixture deployment with an electricity
// rate configured. THE DEFAULT HARNESS DELIBERATELY HAS NONE: an unset tariff
// is the state most deployments start in, and it is the state D5's "say so,
// do not render nothing" rule exists for -- so it stays the default here and
// every existing test keeps describing the deployment it was written against.
func newHarnessWithTariff(t *testing.T, minorPerKWh int64) *harness {
	t.Helper()
	return newHarnessTuned(t, testAgentCredentials(), nil, false, minorPerKWh)
}
```

Inside: `cfg := &config.Config{AdminUsers: []string{"admin"}, AuthLocal: true, SecureCookies: secure, PowerTariffMinorPerKWh: tariffMinorPerKWh}`.

- [ ] **Step 2: The census table gains a deployment hook**

Add one field to `moneyRouteCoverage`'s anonymous struct:

```go
	// deployment builds the harness this case needs, for a route whose money
	// only renders under a particular configuration. nil means newHarness --
	// the ordinary case, and the one every other entry uses.
	deployment func(t *testing.T) *harness
```

and in `TestNoMoneySurfaceLeaksToAnUngrantedObserver`:

```go
			build := tc.deployment
			if build == nil {
				build = newHarness
			}
			h := build(t)
```

Then update the "cost report" entry:

```go
	{
		name: "cost report",
		// WITH A TARIFF CONFIGURED, because partials/power_cost.html only
		// renders an amount when there is a rate to apply -- and a case that
		// claims to cover a money template while rendering its "no tariff is
		// configured" branch proves nothing about the CanSeeCosts gate on the
		// figure. Exactly the hole price_movement.html sat in for three
		// commits, which is why this table checks markers at all.
		deployment: func(t *testing.T) *harness { return newHarnessWithTariff(t, 28) },
		path:       func(t *testing.T, h *harness) string { return "/reports/cost" },
		templates:  []string{"pages/cost_report.html", "partials/power_cost.html"},
	},
```

- [ ] **Step 3: The behavioural tests**

```go
// TestThePowerSectionSaysWhyItIsEmptyWithNoTariff is D5. The draft had the
// section silently absent when no tariff is configured, by analogy with the
// read-only API staying unmounted. That analogy is wrong: those are surfaces
// that do not exist, this is a section of a page the reader is already
// looking at -- and an administrator who sees nothing cannot tell "not
// configured" from "nothing to show", "I lack the permission" or "this build
// predates the feature".
func TestThePowerSectionSaysWhyItIsEmptyWithNoTariff(t *testing.T) {
	h := newHarness(t) // no tariff, which is the default deployment
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(page, "No electricity figure: no tariff is configured.") {
		t.Error("with no tariff configured the cost report does not say so; a reader " +
			"cannot tell an unconfigured rate from a missing permission")
	}
	if !strings.Contains(page, "Electricity") {
		t.Error("the section heading is absent entirely, so there is nothing for the " +
			"explanation to explain")
	}
}

// TestThePowerFigureAppearsWhenATariffIsConfigured is the positive control,
// and it also pins the wording §2.3 requires. 28 minor units per kWh against
// the seeded estate's declared draw.
func TestThePowerFigureAppearsWhenATariffIsConfigured(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if strings.Contains(page, "No electricity figure") {
		t.Fatal("a tariff is configured and the report still says none is")
	}
	if !strings.Contains(page, "kWh") {
		t.Error("the report shows no energy figure, so the money beside it cannot be checked")
	}
	// §2.3: the wording is not decoration. A figure correctly scoped to
	// declared IT load WILL be read as "what it costs to keep this on-prem",
	// and understates that by the site's PUE.
	if !strings.Contains(page, "Not comparable to an all-in hosting quote") {
		t.Error("the estimate does not say it is not comparable to a hosting quote, " +
			"which is the exact comparison the whole feature exists to inform")
	}
	if !strings.Contains(page, "1.0") || !strings.Contains(page, "730") {
		t.Error("the assumed power factor and the hours per month are not stated " +
			"beside the figure; an estimate nobody can check is one nobody should believe")
	}
	// "Ceiling" promises the real bill cannot exceed the figure. It can.
	if strings.Contains(strings.ToLower(page), "ceiling") {
		t.Error(`the report calls the estimate a "ceiling"; it has not earned the word ` +
			"(§2.3 -- typed inputs, unmodelled UPS and distribution loss, excluded " +
			"facility overhead)")
	}
}

// TestThePowerFigureIsHiddenFromAnUngrantedObserver. It is money, so it lives
// behind the same grant as every other money surface -- and unlike the
// estate totals it would otherwise leak through a section nobody thought of
// as a cost page. The page still renders 200 with the money withheld.
func TestThePowerFigureIsHiddenFromAnUngrantedObserver(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("viewer", "viewer-password")

	resp := h.get("/reports/cost", false)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("an ungranted Observer got %d, want 200 with the money withheld", resp.StatusCode)
	}
	page := body(t, resp)

	if strings.Contains(page, "kWh") {
		t.Error("an ungranted Observer was shown the estimated energy, which is one " +
			"multiplication away from the money it is derived from")
	}
	if strings.Contains(page, "Electricity, estimated monthly") {
		t.Error("the power figure's label leaked to an ungranted Observer even if the " +
			"amount did not")
	}
}

// TestTheEstateTotalIsUnchangedByThePowerFigure is the page-level half of
// §2.4 -- the store-level half is TestThePowerFigureIsNotInTheEstateTotals.
// Keeping the structs separate stops the ARITHMETIC contamination; this
// checks the page did not quietly do the addition the struct refused to.
func TestTheEstateTotalIsUnchangedByThePowerFigure(t *testing.T) {
	withoutTariff := newHarness(t)
	withoutTariff.login("admin", "admin-password")
	plain := body(t, withoutTariff.get("/reports/cost", false))

	withTariff := newHarnessWithTariff(t, 28)
	withTariff.login("admin", "admin-password")
	priced := body(t, withTariff.get("/reports/cost", false))

	for _, label := range []string{"Capital, spent", "Per month", "Per year"} {
		before, ok := statValue(plain, label)
		if !ok {
			t.Fatalf("no %q figure on the report with no tariff; this test cannot "+
				"compare what it cannot find", label)
		}
		after, ok := statValue(priced, label)
		if !ok {
			t.Fatalf("no %q figure on the report with a tariff configured", label)
		}
		if before != after {
			t.Errorf("the estate's %q total moved from %s to %s when a power tariff was "+
				"configured; the estimate has entered a total of what somebody priced",
				label, before, after)
		}
	}
}

// statValue reads one figure out of the estate's stat row by its LABEL, which
// is also why the power section must not reuse those labels: two figures under
// one label on one page is the misreading this design is arranged against, and
// this helper would silently read the wrong one.
func statValue(page, label string) (string, bool) {
	re := regexp.MustCompile(`stat-label">` + regexp.QuoteMeta(label) +
		`</div>\s*<div class="stat-value">([^<]+)</div>`)
	m := re.FindStringSubmatch(page)
	if m == nil {
		return "", false
	}
	return m[1], true
}
```

- [ ] **Step 4: Prove two of these can fail**

- Delete the `{{if not .Configured}}` branch from the partial → `TestThePowerSectionSaysWhyItIsEmptyWithNoTariff` must go red.
- Move `{{template "power_cost" .Power}}` outside the `CanSeeCosts` branch → `TestThePowerFigureIsHiddenFromAnUngrantedObserver` and the census guard must both go red.

Restore both.

---

## Task 8: Gates, docs and the demo

- [ ] **Step 1: `make lint`, then `make test`** — foreground, one at a time, exit status read directly. `make test` runs both engines and takes ~13 minutes; do not background it and assume. `gofmt`, `go vet`, `staticcheck` clean.

- [ ] **Step 2: The manual's staleness checker will now name two fragments**

`internal/config/config.go` is in `depends_on` for both `installation` (`docs/manual/parts/10-installation.md`) and `directory` (`parts/12-directory.md`). Run `make manual-stale` and regenerate what it names, per `docs/manual/REGENERATING.md` — do not edit `depends_on` to make a fragment look current.

The concrete content change is one row in the installation env-var table, beside `INV_CURRENCY`:

| `INV_POWER_TARIFF_MINOR_PER_KWH` | — | electricity rate in minor units per kWh (28 for 0.28). Unset means the cost report says no tariff is configured rather than showing an estimate. It is an **estimate** from declared nameplate draw — nothing meters anything |

The `directory` fragment describes LDAP and is unaffected in content; regenerating it is bookkeeping, and `generated_at` must move honestly rather than the dependency being trimmed.

- [ ] **Step 3: Consider adding the new files to the `money` manual fragment's `depends_on`**

`docs/manual/MANIFEST.yaml`'s `money` fragment already tracks every other money surface but does not list `/reports/cost` at all. Adding `internal/domain/power_cost.go`, `internal/store/power_cost.go` and `web/templates/partials/power_cost.html` makes the manual track this section from now on. This is a judgement call for the implementer to state either way — an unlisted path is a section the checker will never call stale.

- [ ] **Step 4: The demo**

`docs/DEMO.md` and the `invctl-demo.service` unit need `INV_POWER_TARIFF_MINOR_PER_KWH` set for the section to show a figure rather than D5's message. Without it, Task 9's E2E exercises the unconfigured branch — which is a real state and a legitimate thing to assert, but it is not the one worth a browser.

- [ ] **Step 5: `docs/ROADMAP.md`**

WP-I2's entry says "there is no `/reports/cost` page at all" and describes power cost as decided-but-unbuilt. Update it to record what was built, and keep the 2026-08-14 decision text — it is the reasoning, not a status line.

---

## Task 9: E2E — one spec, on the state that matters

**Files:**
- Create: `tests/e2e/specs/power-cost-section.spec.js`

Per `docs/E2E.md` and the testing policy: a small number of browser tests on critical paths. The one thing no Go test can prove is that this section actually reaches a browser on the real deployment — the router serves it, the template renders inside the real layout, and nothing throws.

- [ ] **Step 1: Read `docs/E2E.md` first.** The suite reports itself skipped without `INV_E2E_BASE_URL` (an explicitly declared precondition, which is the only legitimate runtime skip). It signs in as the demo admin.

- [ ] **Step 2: The spec**

Assert, on `/reports/cost`:
1. The "Electricity, estimated" heading is present. **Unconditional** — D5 guarantees the section renders in both states, so there is no branch to skip on.
2. Exactly one of the two states is present: either the "No electricity figure: no tariff is configured." line, or a money amount. Both present, or neither, is a failure.
3. **In the configured state**, the "Not comparable to an all-in hosting quote" sentence is in the same section element as the amount — that is the assertion a browser adds over a Go string check, because "beside the number" is a DOM-containment claim, not a substring one.
4. No console errors on the page.

This is a content assertion in both branches, not a runtime skip. If the demo unit has a tariff (Task 8 Step 4), branch 3 is the one that runs.

---

## Risks and edge cases

- **The mechanism is the risk, and it is already decided.** Anyone reading `draw_va` will conclude `SUM` is obviously right. Every store-level comment in Task 3 exists to stop the next person "fixing" it, and Task 5's delta test is what fails if they do.
- **`depth > 0`.** Dropping it makes the estate draw zero, which looks like an empty estate rather than a broken query. Task 4 Step 1 has a dedicated test.
- **The denominator includes sites and racks** (see "One thing to watch" above). It is the spec's own definition; narrowing it is a spec change.
- **Concurrency:** none. One read-only statement, one scan, no transaction, no write. The single-query design is what stops the numerator and denominator disagreeing under a concurrent write.
- **Performance:** the correlated `NOT EXISTS` over `asset_closure` runs once per live asset. `idx_closure_desc` covers `descendant_id`. At the perf fixture's 4,000 assets this is well within the estate-findings budget; if `make test`'s perf suite shows otherwise, say so rather than adding a cache — the page is a report an operator opens, not a hot path.
- **No migration, so no live-DB risk.** A deployment that upgrades and sets nothing sees the D5 message; a deployment that sets a rate sees a figure. Nothing changes for existing data.
- **GDPR/data residency:** nothing personal is involved. `change_log` is untouched — this feature writes nothing.
- **The one behaviour change to existing tests** is the `newHarnessSecure` → `newHarnessTuned` rename and the census table's new field. Both are mechanical; if any existing test breaks on them, that is a signal the refactor was not mechanical after all — stop and report rather than adjusting the test.

## Test plan summary

| Level | Test | What it guards |
|---|---|---|
| unit (`domain`) | `TestTheEstimateDividesOnceAtTheEnd` | §4.3 — summed raw, divided once, and the fixture is proven to distinguish the two |
| unit (`domain`) | `TestThePowerEstimateArithmetic`, `TestAZeroTariffIsUnsetRatherThanFree`, `TestNoDrawIsTheGapBetweenDeclaringAndQualifying` | the conversion, and zero-as-unset |
| unit (`config`) | `TestThePowerTariffIsUnsetByDefaultAndRefusesRubbish` | default unset; `0.28` refuses to start |
| integration, **both engines** | `TestTwoInputsOnOneAssetCountOnce` | §2.1 MAX, dual-engine |
| integration, **both engines** | `TestAContainedDrawDoesNotAddToTheEstateTotal` | §2.2, with the VM-inside-hypervisor arrangement built |
| integration, **both engines** | `TestADrawingAssetIsNotItsOwnDrawingAncestor` | the `depth > 0` self-row trap |
| integration, **both engines** | `TestAnUnknownDrawIsExcludedFromTheFigureAndCountedInCoverage` | D3 |
| integration, **both engines** | `TestLifecycleGatesTheAssetAndTheInputAndNothingElse` | §4.4, including the half that must not filter |
| integration, **both engines** | `TestThePowerFigureIsNotInTheEstateTotals` | §2.4 at store level |
| integration (seed fixture, SQLite) | `TestTheFixtureCountsADualFedServerOnce` | **the regression this spec was rewritten for** |
| functional (real router) | `TestThePowerSectionSaysWhyItIsEmptyWithNoTariff` | D5 |
| functional (real router) | `TestThePowerFigureAppearsWhenATariffIsConfigured` | §2.3 wording, incl. no "ceiling" |
| functional (real router) | `TestThePowerFigureIsHiddenFromAnUngrantedObserver` | D4 / `CanSeeCosts` |
| functional (real router) | `TestTheEstateTotalIsUnchangedByThePowerFigure` | §2.4 on the page |
| static guard | `TestEveryMoneyTemplateHasABehaviouralRoute` (existing, extended) | the new partial joins the money census |
| E2E | `power-cost-section.spec.js` | the section reaches a real browser; the caveat sits beside the number in the DOM |

**Deliberately not tested:** the exact markup of the panel, the CSS separator, and the `powerTariff()` nil guard (a branch reachable only from a construction error the compiler-adjacent harnesses already rule out). Stated here so an unstated skip is not mistaken for an oversight.

## Evidence gate (fill this in before any reviewer is invoked)

*What would be true if this were broken?* The estate total would double for every properly-redundant server (`SUM` regression); or a VM's declared draw would inflate it (containment); or the section would silently vanish on a deployment with no tariff; or the estimate would be added into the priced total; or an ungranted Observer would read the figure.

*What was run to show it isn't?* The five tests above named against those five failures, each observed failing under a deliberate mutation (Task 5 Step 2 and Task 7 Step 4 at minimum), then restored — plus `make test` green on **both** engines, not `go test ./...`.
