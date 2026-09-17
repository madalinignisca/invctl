<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Breakout cables

**Status:** decided 2026-09-17. Implementation not started.

`docs/panel-breakout-design.md` §5 deferred this and said why: the fix needs a
schema change, and one of the two candidate shapes is the one migration `00028`
already rejected. *"That decision deserves its own document, not a paragraph in
this one."* This is that document.

---

## 1. The problem

A QSFP-to-4×SFP+ DAC is **one cable with one end on one side and four on the
other**. No panel, no pass-through: a single moulded assembly that plugs one
100G port into four 25G ports.

`link` cannot express it:

```sql
CREATE TABLE link (
  id             TEXT PRIMARY KEY,
  a_interface_id TEXT NOT NULL REFERENCES interface(id),
  b_interface_id TEXT NOT NULL REFERENCES interface(id),
  medium         TEXT,
  length_m       INTEGER,
  lifecycle      TEXT NOT NULL DEFAULT 'active'
);
```

Two foreign-key columns. Five terminations. There is no arrangement of this
table that holds them.

**This is a real gap, not a formality.** Today an operator either records four
unrelated cables — losing the fact that one connector failing takes all four —
or records one and loses three ports. Both are wrong, and the second is wrong
silently.

## 2. Why the panel case did not settle it

`port_pass_through` (migration `00028`) already handles a twelve-fibre MPO trunk
breaking out to twelve LC front ports, and does it with `position`. That works
because a panel puts a *thing in the middle*: the trunk is a cable to the rear
port, the strands are cables from the front ports, and the pass-through rows
join them. Every cable still has exactly two ends.

A breakout DAC has nothing in the middle. There is no port to hang the fan-out
on, because the fan-out is the cable.

## 3. The two shapes

### Option A — a termination table

Follows `circuit_termination`, which already solves parent-plus-sides:

```sql
CREATE TABLE cable (              CREATE TABLE cable_termination (
  id         TEXT PRIMARY KEY,     id           TEXT PRIMARY KEY,
  medium     TEXT,                 cable_id     REFERENCES cable(id),
  length_m   INTEGER,              side         CHECK (side IN ('a','z')),
  lifecycle  TEXT                  position     INTEGER NOT NULL DEFAULT 1,
);                                 interface_id REFERENCES interface(id),
                                   lifecycle    TEXT
                                 );
```

One `cable` row, five `cable_termination` rows: side `a` position 1, side `z`
positions 1–4.

### Option B — *n* `link` rows sharing a breakout id

```sql
ALTER TABLE link ADD COLUMN breakout_id       TEXT;
ALTER TABLE link ADD COLUMN breakout_position INTEGER;
```

Four `link` rows, one `breakout_id`, positions 1–4, all four carrying the same
a-end interface.

## 4. Decision: Option B

### D1. It agrees with `00028` rather than overturning it — **decided**

The obvious reading of `00028` is "never add a table". That is not what it says.
What it says is:

> "The alternative was NetBox's shape: front_port and rear_port tables, and a
> cable whose ends point at 'some kind of port'. That needs a polymorphic
> reference — a type column plus an id — which is the one join shape this
> codebase has avoided everywhere, and it would put a second cable model beside
> `link` for the two to disagree over."

Two objections, and they are separable: **polymorphic references**, and **a
second cable model**. What `00028` actually did was add `position` to one small
table rather than introduce a parallel port model.

Option B is that same move: two columns on the existing edge type. Option A is
the shape `00028` refused — `cable` beside `link`, two models of the same
physical object, for the two to disagree over.

So this is not a precedent being overturned. It is the precedent being applied
to the next case.

### D2. No constraint migration under live data — **decided, and checked**

This was assumed the wrong way round first, which is why it is written down.

One-patch-per-port looks like it must be a unique index on `link`. It is not.
It is a Go predicate:

```
internal/store/network.go:464
    "cabling %s to %s: one of those ports is already patched"
```

There is no unique index on `link(a_interface_id)` or `link(b_interface_id)`.
So Option B relaxes **one predicate**, for the breakout a-end only — not an
index dropped underneath existing rows, which is the expensive and risky
operation this was feared to be.

### D3. Every existing `link` consumer keeps working — **decided**

`link` is read by the impact walker, the breakout tracer, `BundleCutEffect`,
the unpatch control and the cable list. Under Option A each of those is wrong by
omission from the moment `cable` exists until it learns the new table, and
`TestEveryConnectiveTableIsAccountedForInTheImpactGraph` would fire on day one —
correctly, and with no cheap way to answer it.

Under Option B a breakout reads as four cables that share an id. That is not a
modelling compromise; it is what the object is. Four strands, four pairs of
ends, one assembly.

### D4. The drift guard ships with the columns — **decided**

The real cost of Option B: `medium` and `length_m` are duplicated across the
member rows and can drift apart. One cable's rows must agree, or the breakout is
lying about itself.

The guard goes in **the same commit as the columns**, and is proven against a
deliberately-drifted row rather than against the general shape. This is the
WP-J9 rule, earned four times on one branch: *a fix for a silent-failure class
needs its guard extended in the same commit, and the guard proven against the
specific bug.*

Rules:

- Every live `link` sharing a `breakout_id` agrees on `medium` and `length_m`.
- `breakout_position` is unique within a `breakout_id` among live rows, and
  `> 0` — the `port_pass_through.position` rule, which already has the
  partial-unique-index-scoped-to-live idiom.
- `breakout_id` and `breakout_position` are both set or both null. A position
  with no group, or a group with no position, is a half-written breakout.

### D5. Bundle membership groups in the DISPLAY, not the schema — **decided**

`cable_bundle_member` keys on `link_id`:

```sql
CREATE TABLE cable_bundle_member (
  bundle_id TEXT NOT NULL REFERENCES cable_bundle(id),
  link_id   TEXT NOT NULL REFERENCES link(id),
  PRIMARY KEY (bundle_id, link_id)
);
```

So one physical breakout pulled through a duct is four member rows, and
`/bundles/{id}/impact` would report *"4 cables go dark"* where a backhoe cut
**one** cable with one connector.

The set of things going dark is identical and correct either way — only the
count is overstated. **The bundle and impact pages group by `breakout_id`** and
render "1 breakout cable (4 strands)". The store keeps four rows.

**This was the question that could have flipped the ruling.** If bundle
membership had needed to name one physical cable *in the schema* — if the count
had to be right in the data rather than in the view — a real parent row would
have been the honest place for it, and Option A would have won. It does not:
membership is about which cables share a fate, and four strands of one assembly
share a fate whether they are one row or four.

The grouping gets its own guard. A bundle page that silently stopped grouping
would overstate every breakout in it, and nothing else would notice.

### D6. What is NOT a reason to revisit this — **recorded**

**A breakout needing its own serial or warranty.** This was raised while
deciding and checked: `link` carries `id`, `a_interface_id`, `b_interface_id`,
`medium`, `length_m`, `lifecycle`, `row_version` and nothing else. There are no
per-cable identity attributes in the schema at all, for any cable, and nothing
in the roadmap asks for them. If that ever changes it changes for every cable,
not for breakouts specifically, and it is a question about `link` rather than a
reason to build a parallel model.

## 5. What this does not do

- **Panel breakout.** Already delivered (`docs/panel-breakout-design.md`). A
  breakout *cable* and a breakout *through a panel* are different physical
  arrangements and stay different in the model.
- **"Cable profiles."** Named in WP-B4's title and never specified anywhere. In
  `panel-breakout-design.md` §5 it appears as one *implementation option* for
  breakout cables — "*n* `link` rows sharing a profile id" — which is this
  design, under a different name. There is no separate deliverable.
- **Asymmetric breakouts beyond one-to-many.** A cable with two ends on one side
  and three on the other is not a thing that exists; one `a` interface shared
  across the group is the whole shape.
