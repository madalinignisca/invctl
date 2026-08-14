# Cost attribution

What the estate costs, and whose cost it is.

Written 2026-08-14 from a conversation with the first adopting company's CEO.
This is a **specification for review**, not a description of anything that
exists. Nothing in here is implemented.

---

## 1. The questions this has to answer

In her words, reordered but not softened:

1. **What did it cost, and what does it cost now** — available on any asset.
2. **When something is replaced, what did the old one cost?** So a new price can
   be compared against the one it succeeds.
3. **Which suppliers raise prices beyond inflation?** The company would rather
   leave a supplier that abuses pricing than absorb it, and would rather hire
   people than renew a licence that has stopped being reasonable — the worked
   example being VMware to Proxmox plus two good Linux engineers.
4. **What slice of shared infrastructure does a project or client use?** Starting
   with the virtualisation platform, later combined with network, rack, power and
   transit.

The fourth is the hard one and the reason this document exists.

## 2. What already exists

More than expected, and it changes the shape of the work.

- **Cost lines attach to assets, services, projects and circuits**, with
  `once` / `monthly` / `yearly` periods and amortisation.
- **Money is integer minor units.** Never a float. This is settled and is not
  reopened here.
- **Cost lines carry validity windows** (`valid_from` / `valid_until`), and
  `cost.go` says why: *"without it a renewal at a new price overwrites its
  predecessor and the history is gone"*. **Price history is therefore already
  being recorded** — every renewal is a separate line, retained.
- **`cluster.min_hosts`** is documented as *"CAPACITY, not quorum: how many
  members must survive for the guests to fit"*.
- A **`provider`** table exists, referenced by circuits only.

Question 1 is largely already satisfied. Question 2 needs a small addition.
Questions 3 and 4 need real modelling.

## 3. What is missing

**Compute capacity is not modelled at all.** Assets carry rack units, weight and
depth — physical facts. Nothing records cores, memory or storage. A cluster knows
how many hosts it has and how many it needs, not how big they are.

This is the prerequisite for everything in §5, and the honest bulk of the work.
The arithmetic afterwards is small by comparison.

Also missing: **replacement lineage** between assets, **provider as a dimension
on anything but circuits**, and an **inflation reference series**.

---

## 4. The two cheap wins, which do not depend on the rest

### 4.1 Replacement lineage

One nullable self-reference on `asset` — *this replaces that* — plus the UI to
set it. Both boxes already carry their own cost history, vendor and dates, so
the comparison is a join and a page:

> `srv-app-03` replaces `srv-app-01`. Acquisition €4,200 in 2021 versus €6,850
> in 2026 — **+63% over five years**.

Soft delete applies as everywhere else: the predecessor is retired, not deleted,
which is exactly why the comparison remains possible.

### 4.2 Price movement

A view over cost lines already stored. Per asset, per service, per circuit:
what it cost, what it costs, when it changed, by how much. No schema change.

Against an **inflation series** — a hand-maintained table of year to rate — the
same view answers question 3 for a single item. Answering it *per supplier across
the estate* requires promoting `vendor` from free text to a real reference, which
is a migration over data already typed by hand, and is deliberately deferred.

---

## 5. Attribution: the slice of a shared platform

### 5.1 Derive the unit prices, never type them

```
    cost of the cluster (hosts amortised + power + licences + rack share)
  ÷ usable capacity
  = price per core, per GB memory, per GB storage of a kind
```

Typed prices — "€5 per vCPU" — do not reconcile: the slices will not sum to what
was actually paid, and the first person to check finds the number decorative.
Derived prices always reconcile and self-correct when a node is bought or a
licence renews.

### 5.2 Availability is a division, not a multiplier

The premium paid for surviving a node loss falls out of dividing by capacity
that **survives**, rather than raw capacity. No separate factor to maintain:

| cluster | hosts | needs | effective |
|---|---|---|---|
| 3 hosts, needs 2 | 3 | 2 | **1.5×** raw cost per unit |
| `win-hyperv` | 3 | 3 | 1.0× — and already reported *"not survivable"* |

The input is `cluster.min_hosts`, which exists and is audited.

**Storage is the same shape.** Ceph at 3× replication means a usable terabyte
costs three raw; a RAID6 SAN is a different ratio; local disk is 1:1. One
raw-to-usable ratio per storage kind, and the same division handles it. No
special-case logic per technology.

### 5.3 It must add up

**100% of a cluster's cost lands somewhere.** Whatever is not allocated to a
project is *idle capacity* and is shown as its own slice, never dropped.

> €4,200 last month — €2,900 reached projects, €1,300 was headroom.

This is the same honesty the rest of the system practises: a gap reported rather
than hidden. A report whose slices do not sum to the invoice is worse than no
report, because somebody will put it in a board pack.

### 5.4 Shared occupancy

For estates that pack several tenants into one VM to save on licensing: a list
of occupants with a **percentage each**, declared by a person, never inferred.

When the percentages do not total 100, that is a finding, not a silent rounding.

---

## 5.5 Three numbers, not one

*Added 2026-08-14 from the operator who will run this for the first client. It
replaces an earlier draft that carried a single "allocated" figure, which could
not have answered any of the questions below.*

Each resource dimension carries **three declared numbers**:

| | what it is | what it answers |
|---|---|---|
| **Contracted** | what was promised commercially | are we solvent against our own agreements |
| **Provisioned** | the hard limit actually configured | what can this workload really take |
| **Allocated (soft)** | the figure money is computed on | what does this cost |

All three are **declared** — a commercial assertion, a configuration assertion
and a financial one. None requires telemetry, each is somebody's decision, and
each carries an audit entry. This is why the model does not strain against
`docs/AUDIT.md`: adding two more numbers adds no new *class* of fact.

### The three findings that follow

1. **Provisioned above contracted** — giving away more than was sold. The action
   is commercial: renegotiate, or amend the terms to match what is already being
   delivered. It appears as the infrastructure team responds to demand, which is
   exactly when nobody thinks to tell an account manager.
2. **Provisioned above what is safe to run** — the cluster is oversubscribed
   beyond its declared overcommit ratio. The action is operational.
3. **Contracted above physical capacity** — *more has been promised than exists*.

The third is the one that motivated this section, and it is invisible on every
utilisation dashboard. Clusters routinely sit at 30–40% CPU and 60–70% memory
and look entirely healthy while being abusively overcommitted, because
utilisation measures what is being taken, not what could be claimed. **Whether
every contract could be honoured at once is a solvency question, and it is
answerable only from declared figures — which is precisely what this system
holds.**

### The overcommit ratio is declared, per cluster

CPU is routinely overcommitted, memory rarely, storage differently again. The
safe ratio is a property of the cluster and a judgement its operator makes, so
it is declared rather than derived, with an estate default. It is the divisor in
finding 2 and it must never be inferred from observed load — a quiet cluster
would raise the apparent safe ratio and licence the very overcommit this is
meant to catch.

### Cost of generosity, kept separate from headroom

Money is attributed on **soft allocation**. Where provisioning exceeds it, that
capacity costs real money that nobody is paying for, and it is not the same as
headroom held on purpose. §5.3's reconciliation therefore has three parts, not
two:

```
attributed to projects   (soft allocated)
provisioned beyond it    (unfunded — a decision somebody made without pricing it)
headroom                 (deliberate, and the reason the cluster survives a node)
```

---

## 6. Allocated, not used — and how the door stays open

**Decided: allocation is the basis.** See `DECISIONS.md` for the full reasoning.

The short form: allocated is **declared** — a person agreed a VM gets 8 cores.
Used is **observed** — telemetry, changing constantly, nobody decided it. The
project already separates those absolutely (`docs/AUDIT.md`), so a used-basis
implementation could never be a flag that repoints the same column; it would
arrive through the observation path with its own audit obligations.

Therefore the seam is **not** a plugin framework. It is one function taking a
capacity figure and returning a slice, indifferent to which class of fact
produced the figure. If keeping even that costs more than slight indirection,
collapse it — the second basis does not exist yet.

**Every computed slice records its basis.** Without that, a later switch gives
two months of one meaning, a flag flip, and a discontinuity nobody can explain a
year later. Cheap now, impossible to retrofit honestly.

---

## 7. Non-goals

- **No live metering.** Nothing here polls a hypervisor. Capacity is declared.
- **No invoicing.** This produces figures a person uses, not a bill.
- **No currency conversion.** One currency per deployment, as today.
- **No forecasting.** Trend lines over recorded history, never projections.
- **The system still never acts on the estate.** Invariant 9 is untouched: no
  discovery, no reconciliation, no credentials.

---

## 8. Open questions

1. **Attribution requires modelling.** Ten clients' databases in one VM cannot be
   sliced, because there is nothing to slice. *Proposal, to discuss:* invctl
   reports where attribution is impossible — "this hypervisor carries workloads
   from four projects" — the same shape as the environment-spans report, at `gap`
   severity. That turns a discipline problem into a number that falls as the
   estate improves.
2. **How far up does this go?** The virtualisation platform first. Network, rack,
   power and transit are shared differently and each needs its own rule. Not yet
   designed.
3. **When does `vendor` become a real reference?** Needed for per-supplier
   analysis, and it is a migration over hand-typed data.
4. **Does a project always map to one client?** The bucket is deliberately
   general — it holds the work done for a client *or* one of the company's own
   products, and the arithmetic does not care which. Worth confirming that
   nothing downstream starts assuming the first.

## 9. Who this is for, and why that is not an access-control problem

**One company owns the whole estate and every user is its employee.** A project
is an internal bucket: sometimes the work done for a client, sometimes one of the
company's own SaaS products. **Clients do not sign in.** This was corrected on
2026-08-14 after an earlier draft of this document assumed multi-tenancy.

It follows that cost attribution needs **no new authorisation work**. Anyone who
can already read a rack's acquisition cost and a circuit's monthly rate can read
these figures; attribution rearranges numbers those people are already entitled
to, and adds no class of data the system did not already hold.

So **WP-G1 is not a dependency of J4.** Its case stands where the roadmap already
makes it — multi-team and MSP deployments — and this work neither strengthens nor
weakens it.

The one condition that would change the answer: if a client is ever given a
read-only login to see the project run for them, then per-project visibility
becomes a real constraint and G1 becomes a prerequisite. That is a product
decision, not a technical one, and nobody has taken it.

---

## 10. Staging

1. **Capacity model** — hosts get cores, memory, storage; VMs get their
   allocation; storage kinds get a raw-to-usable ratio. Useful before any money:
   *"is this cluster oversubscribed?"*
2. **Derived unit prices** — cluster cost over usable capacity.
3. **Attribution** — slices per project, idle shown, everything summing to 100%.
4. **Shared occupancy** — occupant percentages, with the sum-to-100 finding.

Replacement lineage (§4.1) and price movement (§4.2) are independent of all four
and can ship first.
