<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Power cost — design

**Status: APPROVED 2026-09-04, not yet built.** All four decisions in §3 were accepted as recommended. WP-I2 asked for "cost (circuits, power draw)".
Circuits are done. Power draw is not: there is no tariff, no kWh and no energy
figure anywhere in the codebase, and the power report carries no money at all.

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

## 2. The three things that make this hard

### 2.1 A dual-fed asset would be counted twice

`power_input.draw_va` is declared **per input**, and a redundantly-fed server
has two — A and B. `powerUtilisation` already sums draw per feed, and that is
right for **capacity**: a feed must be sized to carry its partner's whole load
when the other side dies, so allocation is deliberately conservative and
deliberately double-counts across a pair.

Cost asks a different question. A dual-fed server does not consume twice; it
consumes once, split across two paths. **Summing feed allocations to get an
estate energy figure would overstate every redundant asset by 100%** — and
redundancy is the norm in exactly the estates that would use this.

So cost sums **per asset**, never per feed, and the two figures will differ.
That difference is not a bug and the report must say so, or someone will
reconcile them and find a defect that is not there.

### 2.2 VA is not watts, and neither is kWh

`draw_va` is apparent power in volt-amps. Energy is kilowatt-hours. Getting
from one to the other needs a power factor and a duration, and a nameplate VA
is an upper bound on what the equipment can draw, not what it does draw.

Every step of that chain widens the error. The estimate is therefore a
**ceiling**, and the wording must say ceiling rather than "estimated cost",
which reads as a best guess rather than a worst case.

### 2.3 It must not contaminate the estate total

`EstateCosts` carries a deliberate property, argued in its own comment: the
totals are *what the estate costs **that somebody has priced***, reported with
coverage figures so a reader cannot mistake a partial total for a whole one.

An estimate entering that total would make it part-declared and part-derived
with no way to tell which. **Power cost is a separate surface with its own
total**, never folded into `EstateCosts.Totals`.

## 3. Open decisions — these need answering before implementation

### D1. Where does the tariff live? — **DECIDED: (a), one rate in config**

| Option | For | Against |
|---|---|---|
| **a. One rate, in config** (`INV_POWER_TARIFF_MINOR_PER_KWH`) | Simplest thing that answers the question. No schema, no UI, no audit obligation. | One estate, one rate. A second site on a different contract cannot be modelled. |
| **b. On `power_source`** | A source *is* the utility connection; a rate is a fact about it. Already an audited entity. | Sources are per-supply, not per-contract; two sources on one tariff means typing it twice. |
| **c. New `tariff` entity, referenced by source** | Correct if rates ever vary by time of day, season, or contract term. | A whole entity, CRUD, audit and UI for a number most estates have one of. |

**Recommendation: (a) now, (b) when a second rate is actually needed.** The
question this feature exists to answer is answerable with one rate, and a
config value is the only option with no migration and no audit surface. It is
also the only one that cannot be half-filled: a nullable column on `power_source`
would give partial coverage, which for a *cost* figure is worse than none.

### D2. Power factor — **DECIDED: assume 1.0, stated in the report**

VA × power factor = watts. Modern server PSUs run ~0.95–0.99; the conservative
assumption is **1.0**, which treats VA as W and overstates — consistent with
"ceiling", and it means no new field.

**Recommendation: assume 1.0 and say so in the report.** A declared per-asset
power factor is a second number nobody has, and a wrong one silently narrows a
figure whose whole value is being an upper bound.

### D3. An asset with no declared draw — **DECIDED: unknown, and counted**

The rest of this codebase is unambiguous here — `Rating`'s fields are nullable
"because not recorded must stay distinguishable from zero", and `EstateCosts`
reports coverage precisely so a partial total is not read as a whole one.

**Recommendation: unknown, and counted.** The report gives the estimate over
assets that declare a draw, plus *"N of M assets declare no draw"* beside it,
exactly as `EstateCosts` does for pricing and the power report does for
unrated feeds.

### D4. Where does it appear? — **DECIDED: a section on `/reports/cost`**

**Recommendation: a section on the existing `/reports/cost` page**, below the
declared surfaces and visually separated, with its own heading and the ceiling
wording. Not a new page: the question is "what does the estate cost", and
sending someone to two pages to answer it is how the two figures get compared
without their captions.

It is gated by `CanSeeCosts` like every other money surface, and its templates
join the money-census guard added on 2026-09-03.

## 4. What gets built, if this is approved

1. `INV_POWER_TARIFF_MINOR_PER_KWH` in config, unset by default. **Unset means
   the section does not render at all** — the same posture as the read-only API
   and the monitoring webhook, which stay unmounted until configured.
2. A store function summing `draw_va` **per asset** over live inputs, with the
   count of assets declaring none.
3. `kWh/month = VA × 1.0 × 730 ÷ 1000`, and cost = kWh × tariff. 730 is the
   mean hours in a month; it is stated in the report, not hidden in a constant.
4. A section on `/reports/cost` carrying: the ceiling figure, the coverage
   count, the assumed power factor, the tariff in force, and one sentence
   saying this is declared nameplate draw and nothing measures it.
5. Tests: the per-asset sum does not double-count a dual-fed asset (the
   defect this design exists to avoid); unknown draw is excluded and counted;
   the section is absent when no tariff is configured; an ungranted viewer
   sees no figure.

## 5. What this explicitly does not do

- No metering, no PDU polling, no observed draw. That is observed state and a
  different contract.
- No time-of-day or seasonal rates.
- No per-project attribution of power. Power attaches to an asset; a project's
  share of a shared host is the same problem `WP-J5 · Shared occupancy` already
  solves for money, and reusing it is a later decision, not this one.
- No cooling multiplier or PUE. A datacentre's PUE is a fact about the
  building, not the estate, and inventing one would be exactly the
  measured-looking figure this design refuses.
