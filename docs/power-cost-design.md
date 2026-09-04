<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Power cost — design

**Status: REVISED 2026-09-04 after review, D3 amended after planning, not yet built.**

The first version of this spec was wrong in its central mechanism and was
rejected before any code was written. §2.1 now records what it got wrong and
why, because the mistake is the most useful thing in this document: it is
available to anyone who reads `draw_va` and assumes they know what it means.

WP-I2 asked for "cost (circuits, power draw)". Circuits are done. Power draw is
not: there is no tariff, no kWh and no energy figure anywhere in the codebase,
and the power report carries no money at all.

The decision this argues from was taken on 2026-08-14 and is already in
`docs/ROADMAP.md`: **power cost is an estimate**, declared nameplate draw times
a tariff, and it must be labelled as one everywhere it appears.

> "A figure that looks measured and is not is worse than no figure."

## 1. What this is for

One question, and the roadmap already names it: *keep this platform or move to
another*. An estate comparing its own racks against a hosting quote needs the
electricity in the comparison, and today it has to do that arithmetic outside
the tool with numbers it exports by hand.

It is **not** for billing, chargeback, or anything an invoice is reconciled
against. That would need metering, metering is observed state with a reporter
and an age, and this system never touches the estate.

**It is also not a like-for-like hosting comparison**, and §2.3 says what is
missing from it. The report must say the same thing to its reader.

## 2. The things that make this hard

### 2.1 `draw_va` is an ALLOCATION figure, not a DEMAND figure

This is the one that matters, and the first draft of this spec got it exactly
backwards.

That draft argued: capacity sums per *feed* and deliberately double-counts a
redundant pair, therefore cost should sum per *asset*, because "a dual-fed
server does not consume twice; it consumes once, split across two paths."

**The premise is false for this schema.** The draw is not split across the two
paths. Each side records the *whole* load, because that is what the capacity
report needs — `internal/store/power.go:179` sums `draw_va` per feed into
`allocated_va` and checks it against the feed's derated capacity, and a feed is
correctly sized only if it can carry its partner's entire load when the other
side dies. The project's own fixture says so in as many words:

```go
// PROPERLY 2N: one lead per side. Converges only at the generator, which
// is the design and must not read as a fault.
{"hv-02", "DB-A/A2", "A", 900},
{"hv-02", "DB-B/B1", "B", 900},
```

That is a ~900 VA server with 900 declared on each side. Summing per asset
returns 1,800 — **the identical 100% overstatement the draft existed to
prevent, moved from feed scope to asset scope.**

The general lesson: an allocation figure and a demand figure are different
quantities that share a unit. Nothing in the schema, the form, `checkDraw`, or
the column comment distinguishes "this is my half" from "this is the full load,
recorded on both sides for failover sizing" — so no aggregation over this
column can tell them apart. The information is not there to be recovered.

**Decision: take `MAX(draw_va)` per asset, not `SUM`.**

Two inputs on one asset in this estate mean two paths to one load; that is the
premise of the entire false-redundancy report, which exists precisely because
redundant feeding is the norm. `MAX` is right for that case and is right for a
single-fed asset trivially.

It is wrong for one shape: a chassis with two genuinely independent rails
feeding different components, where the real draw is the sum. That reads low.
This is the accepted cost of the decision, and it is the smaller error — it is
rarer than redundant feeding, and understating one unusual chassis is more
tolerable than doubling every properly-redundant server in the estate.

**If a real per-asset demand figure is ever wanted, it is a new declared field,
not a smarter query over this one.** That is a work package with a migration, a
form, an audit surface and a coverage problem of its own. Do not try to derive
it here.

### 2.2 Containment would double-count a second way

`power_input.asset_id` is `REFERENCES asset(id)` with no kind restriction
(`00023_power.sql:115`), and neither `NewPowerInput` nor the handler nor the
asset-detail form limits which kinds may carry one. A `vm` can therefore
declare its own input today, using the same form every other asset gets.

A hypervisor declaring 900 and a VM inside it declaring 100 is 1,000 by any
naive query, even though the VM's power is virtual and already inside the
host's wall draw.

**Decision: an asset contributes only if no ancestor of it also declares a
draw.** Containment goes through `asset_closure`, per the standing rule — never
a recursive `parent_id` walk. This costs one join and closes the hole
permanently rather than relying on nobody entering the data.

### 2.3 VA is not watts, kWh is neither, and "ceiling" was the wrong word

`draw_va` is apparent power in volt-amps. Energy is kilowatt-hours. Getting
from one to the other needs a power factor and a duration.

Assuming power factor 1.0 is genuinely conservative **for that one step**: real
power cannot exceed apparent power at the same measurement point, so treating
VA as W never understates a correctly-recorded VA figure.

**But the draft called the whole figure a "ceiling", and it has not earned
that.** A ceiling promises the real bill cannot exceed it. It can:

- **The input is a typed number.** `checkDraw` rejects only negatives and
  values over 100,000 — it catches a stray zero, not a PSU's *DC output*
  wattage typed into a field named `draw_va`. Real AC input draw for that PSU,
  after conversion loss, is higher than the figure typed.
- **Everything upstream of the declared input is unmodelled, and some of it is
  already in this schema.** `power_source.kind = 'ups'` exists because a UPS is
  a real, lossy conversion stage between the utility and the declared input.
  The bill is measured upstream of it; the declared draw sits downstream.
- **Cooling and facility overhead are excluded** (§5), correctly — PUE is a
  fact about the building, not the estate. But §1's whole purpose is comparing
  against a hosting quote, and a hosting quote bundles power *and* cooling into
  its price. A figure correctly scoped to declared IT load will be read as
  "what it costs to keep this on-prem" and understates that by the site's PUE.

**Decision: do not call it a ceiling.** Call it what it is — *declared load
electricity, power factor 1.0, 730 h/month, excluding UPS and distribution loss
and facility overhead* — and state in the report that it is **not comparable to
an all-in hosting quote without adding those**.

The exclusions are right. Only the word was wrong, and a wrong word on a money
figure is the failure this whole document is arranged to avoid.

### 2.4 It must not contaminate the estate total

`EstateCosts` carries a deliberate property, argued in its own comment: the
totals are *what the estate costs **that somebody has priced***, reported with
coverage figures so a reader cannot mistake a partial total for a whole one.

A derived figure entering that total would make it part-declared and
part-derived with no way to tell which. **Power cost is a separate surface with
its own total**, never folded into `EstateCosts.Totals`.

Keeping the struct separate stops *arithmetic* contamination. It does not stop
a human reading two currency totals on one page and adding them — which is the
same failure one layer up. So the section also gets its own heading tier, no
shared grand-total styling, and its assumptions rendered immediately beside the
number rather than in a footnote.

## 3. Decisions

### D1. Where does the tariff live? — **one rate in config**

| Option | For | Against |
|---|---|---|
| **a. One rate, in config** (`INV_POWER_TARIFF_MINOR_PER_KWH`) | Simplest thing that answers the question. No schema, no UI, no audit obligation. | One estate, one rate. A second site on a different contract cannot be modelled. |
| **b. On `power_source`** | A source *is* the utility connection; a rate is a fact about it. Already an audited entity. | Sources are per-supply, not per-contract; two sources on one tariff means typing it twice. |
| **c. New `tariff` entity, referenced by source** | Correct if rates ever vary by time of day, season, or contract term. | A whole entity, CRUD, audit and UI for a number most estates have one of. |

**(a) now, (b) when a second rate is actually needed.** The question this
feature exists to answer is answerable with one rate, and a config value is the
only option with no migration and no audit surface. It is also the only one
that cannot be half-filled: a nullable column on `power_source` would give
partial coverage, which for a *cost* figure is worse than none.

It follows the existing single-currency, minor-unit `int64` convention set by
`config.Currency` and `domain.Cost.AmountMinor`. **It carries no currency field
of its own** — a second currency on the page is a bug, not a feature.

### D2. Power factor — **assume 1.0, stated in the report**

VA × power factor = watts. Modern server PSUs run ~0.95–0.99; the conservative
assumption is **1.0**, which treats VA as W and overstates.

A declared per-asset power factor is a second number nobody has, and a wrong
one silently narrows the figure. Note the scope carefully, per §2.3: 1.0 is
conservative for the VA→W step **only**, and does not make the end-to-end
figure an upper bound.

### D3. An asset with no declared draw — **count what was recorded, not what wasn't**

**AMENDED 2026-09-04**, after the plan flagged that the first wording produced
a meaningless denominator. It said the coverage line was *"N of M assets
declare no draw"*, with M being "assets that could plausibly carry one".

There is no honest way to compute M. Narrowing it by `asset.kind` is the
obvious idea and it is a trap: `asset.kind` is a foreign key into the
`asset_kind` lookup table, an **open set that grows by INSERT**, and
`internal/domain/asset.go:114-127` already records what a Go-side hand-listed
subset does to it — `CanHostInstances` and `IsAttachable` "answer false for
anything they have not heard of", so a kind added by INSERT is silently
excluded with no diagnostic. A coverage figure that silently stops counting a
new kind is worse than no coverage figure, and this document exists to refuse
exactly that trade.

Leaving M as *every* live asset is no better: it counts every site, rack,
bridge, cluster and VM as having "failed" to declare a draw, so a fully-modelled
estate reports single-digit coverage and the number reads as noise.

**The adjacent power report already answered this**, and this spec was the thing
out of step. `powerCoverage` in `internal/store/power_findings.go` says so
outright:

> "Not `assets with no input` — almost nothing in a rack has its own input
> modelled and never will."

`PowerReport.Assets` is therefore *live assets with at least one power input* —
a positive count — and `PowerReport.UndeclaredDraw` is *live inputs with no
draw recorded*, which is the gap that is actually actionable.

**Follow that precedent exactly.** The section reports:

- **how many assets contributed** a declared draw to the figure, and
- **how many live inputs declare no draw** — someone recorded the supply path
  but not the number, which is a real gap somebody can go and close.

No ratio against the whole estate, because the denominator would be invented.
If nothing at all declares a draw, say that in words rather than showing a
zero, for the same reason D5 refuses to render nothing.

### D4. Where does it appear? — **a section on `/reports/cost`**

Below the declared surfaces and visually separated, with its own heading and
the §2.3 wording. Not a new page: the question is "what does the estate cost",
and sending someone to two pages to answer it is how the two figures get
compared without their captions.

It is gated by `CanSeeCosts` like every other money surface, and its templates
join the money-census guard added on 2026-09-03.

### D5. No tariff configured — **say so, do not render nothing**

The draft had the section silently absent when `INV_POWER_TARIFF_MINOR_PER_KWH`
is unset, by analogy with the read-only API and the monitoring webhook staying
unmounted until configured.

That analogy is wrong. Those are *surfaces that do not exist*; this is a
section of a page the reader is already looking at. An administrator who sees
nothing cannot tell "no tariff configured" from "nothing to show", "I lack the
permission", or "this build predates the feature" — and everything else in this
design exists to stop a reader guessing at what a number means.

**Render the heading with one line: "No electricity figure: no tariff is
configured."** Consistent with how this app reports unrated feeds and unpriced
assets rather than hiding them.

## 4. What gets built

1. `INV_POWER_TARIFF_MINOR_PER_KWH` in config, `int64` minor units, unset by
   default. Unset renders the section with the D5 message, not nothing.
2. A store function returning, over live assets and live inputs:
   - the **sum over assets of `MAX(draw_va)` per asset** (§2.1),
   - excluding an asset whose `asset_closure` ancestor also declares a draw
     (§2.2),
   - the count of assets that contributed a draw, and the count of live
     inputs declaring none (D3 as amended) — never a ratio against every
     live asset, and never a `kind IN (...)` list.
3. `kWh/month = VA × 1.0 × 730 ÷ 1000`, cost = kWh × tariff. 730 is the mean
   hours in a month, stated in the report rather than hidden in a constant.
   **Sum raw VA across the estate first, divide once at the end** — per-asset
   integer division truncates downward every time and would silently erode the
   figure, the same reason `domain.CostTotals` avoids per-line rounding.
4. **Lifecycle gating, decided rather than copied.** Include an asset only if
   the asset and the input are both live. The feed and panel lifecycles do
   *not* gate it: `PowerFindings` filters all four because a finding is about
   the supply path, but a retired feed under a running server is a data
   inconsistency, not a reason to think the server stopped drawing power.
   `AssetsLosingPower` already filters only the asset and the input; follow it.
5. A section on `/reports/cost` carrying the figure, the coverage count, the
   assumed power factor, the tariff in force, the 730 h/month, and one sentence
   saying this is declared load and nothing measures it — with the §2.3
   not-comparable-to-a-hosting-quote line beside it, not footnoted.
6. Tests, and the first is the regression this spec was rewritten for:
   - **`hv-02` (900 + 900) contributes 900, not 1,800** — using the existing
     seed fixture, not a hypothetical one.
   - a VM declaring a draw inside a hypervisor that also declares one does not
     add to the total.
   - an input with no `draw_va` is excluded from the figure and counted in
     the undeclared-inputs number.
   - the coverage numbers do not vary with the number of sites, racks or VMs
     in the estate — the guard against D3's amended denominator regressing to
     a ratio over everything.
   - the section renders the D5 message when no tariff is configured.
   - an ungranted viewer sees no figure (`CanSeeCosts`).
   - the estate total on the same page is unchanged by any of it.

## 5. What this explicitly does not do

- No metering, no PDU polling, no observed draw. That is observed state and a
  different contract.
- No time-of-day or seasonal rates.
- No per-project attribution of power. Power attaches to an asset; a project's
  share of a shared host is the same problem `WP-J5 · Shared occupancy` already
  solves for money, and reusing it is a later decision, not this one.
- No cooling multiplier or PUE. A datacentre's PUE is a fact about the
  building, not the estate, and inventing one would be exactly the
  measured-looking figure this design refuses. §2.3 requires the report to say
  this rather than leave the reader to assume otherwise.
- No per-asset demand field. §2.1 says why that is a separate work package.

## 6. Review record

Challenged 2026-09-04 by `codex-reviewer` before implementation, per the
pipeline's "challenge the plan" stage. Two findings were rated critical and
both were upheld against the code:

- **§2.1's central claim was false** — verified against
  `internal/seed/seed_hardware.go:204-207` and `internal/store/power.go:179`.
  The mechanism changed from `SUM` to `MAX` per asset.
- **Containment was an unhandled second double-count vector** — verified
  against `00023_power.sql:115`. Added §2.2.

Three further findings were accepted without needing a decision: "ceiling" was
unearned wording (§2.3), silent non-rendering was the wrong failure mode (D5),
and per-asset rounding would erode the figure downward (§4.3). The lifecycle
question (§4.4) came from the same review.

Planning then found one more, and it was a spec defect rather than a plan
problem: D3's denominator was uncomputable, and narrowing it by `asset.kind`
would have inherited the documented open-set gap in `asset.go:114-127`. D3 is
amended above to follow `powerCoverage`'s existing precedent instead. That the
adjacent report had already answered the same question, in a comment, is the
reason to read neighbouring code before specifying against it.

**Nothing in the first draft was a coding error.** Every defect was in the
document, and all of them were found by reading the schema and the fixture the
document claimed to describe.
