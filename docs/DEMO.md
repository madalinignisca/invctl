# Presenting invctl

`make demo` throws away the database, reseeds, and serves on `0.0.0.0:8088`.
Sign in as `admin` / `demo-password`.

Everything below is real output from the seeded estate. Nothing on screen is a
mock-up, and none of the telemetry is written directly into a table — the demo
observations go through `RecordObservation`, the same function the webhook
calls, so what a visitor sees is what the production path produces.

Budget about ten minutes for the whole thing, or take the first two sections if
that is all the time there is. The reachability story is the one people react
to.

---

## The one-sentence version

> An inventory that can answer "if I take this away, what breaks?" — including
> the network, and including the difference between what somebody declared and
> what a machine reported.

---

## 1. The question that started it (3 min)

Open **Assets → fw-edge-1 → Impact**.

This is a border firewall. Ask what happens if it goes away.

> **No service changes status — but the network does.**
> haproxy-edge · degraded · *https requires a external anchor; the only path runs through fw-edge*
> partner-gateway · degraded · *https requires a cross_env anchor*
> **fw-edge: 1 of 2 members surviving; no redundancy remains until a standby is promoted**

Two things worth saying out loud:

- Nothing *internal* breaks. Services behind the firewall keep serving each
  other, because reachability is a **relation**, not a property of a box. That
  is the false alarm most tools generate here.
- The pair is `active_passive` with **manual** failover, so it is degraded
  rather than fine. A human has to promote the standby.

**Then take both halves.** Use *"Take something else away as well"* → `fw-edge-2`.

> haproxy-edge · **down** · partner-gateway · **down**
> **fw-edge: 0 of 2 members surviving; the whole group is down**

This is the demo's sharpest moment, and it is worth admitting why: for most of
this project's life the page said **"Nothing breaks."** here. A strictly larger
outage produced a strictly quieter answer. Two independent bugs did it, and both
were found by an adversarial review rather than by the test suite.

## 2. Group health and host reachability are different questions (2 min)

Open **Assets → sw-core-2 → Impact**. This is one chassis of an MC-LAG core pair.

> 7 services affected · 6 assets isolated on `data`
> sso · **down** · *lost 1 of 1 instances (running, but network-isolated — not powered off)*
> vault · degraded · *soft dependency on sso/https is down*

The MC-LAG group is still **healthy** — one of two chassis is plenty. And yet
`hv-03` is cut off, because it is single-homed to *this* chassis. `hv-01` and
`hv-02` have a second cable and are fine.

Group health and host reachability are different questions with different
answers in the same run. No MC-LAG-specific code produces that; the second
attachment-member row does all the work.

Note the wording: *running, but network-isolated — not powered off*. During an
incident those call for completely different responses.

Contrast with **sw-oob-1 → Impact**: 15 assets unmanageable on the **management**
plane, and **no service affected at all**. Both planes are evaluated in the same
run and only one decides status.

## 3. Declared versus observed (3 min)

Back to the **dashboard**.

**Reporters** — one line per monitoring credential, not one per entity:

| | |
|---|---|
| `mon-prod` *agent* | just now · reporting |
| `mon-oob` *agent* | 6h ago · **silent since 15:50** |

A collector that dies is **one** alertable event. Without this, a dead collector
and a healthy estate look identical forever — and an intruder's first act is
killing the collector.

Open **hv-03** and read its health panel:

> **unknown** · mon-oob · *stale since 15:50*
> **down** · *since 21:10* · mon-prod · *polled just now*

Three separate facts that must never collapse into one:

- `state_since` — **down since 21:10**. The 03:00 question.
- `last_report_at` — polled just now. Still being watched.
- The management reading shows **unknown**, never its last value, because its
  reporter went quiet. Silence is not health.

Everything a machine wrote is labelled `agent` beside the value. No machine
credential can write `source = 'declared'`, sign off `verified_by`, or grade its
own confidence.

**Active overrides** on the dashboard — an operator has silenced `vm-queue-1`:

> vm-queue-1 · up · admin *user* · until … · *probe checks the wrong port; the queue is serving. INC-4102*

It **shadows** the reading, never mutates it. The reporter keeps recording the
truth underneath, which is what you need afterwards. Reason and expiry are
mandatory, capped at 24h — a permanent override is how a real outage stays
invisible for six weeks.

**Unrecognised entities** — `hv-04-rack-b2` reported and the inventory has no
record of it. Refused with a 404 and queued as drift rather than created: an
observation must never be able to create inventory, or a narrow token becomes a
write vector. An asset the estate has and the inventory does not is a finding.

## 4. The audit trail (2 min)

Open **vm-queue-1** and scroll to its timeline. It is a merged view of declared
changes and observed transitions, and it shows a **flap episode**: the entity
oscillated enough to trip compression, and the closing row says how much it hid.
Compression a reader cannot size is just suppression.

Then **Changes** in the nav. Every declared mutation, with `actor` and
`actor_kind` side by side. The actor column holds an opaque id, never a username
— so the trail carries no personal data, can be kept indefinitely, and scrubbing
an account answers an erasure request while the log keeps its integrity.

---

## If somebody asks

**"Is any of this faked for the demo?"** No. The inventory is a seeded fixture.
The telemetry is staged through the real recorder with real agent credentials
and a real environment scope, subject to the same validation, monotonicity,
transition-only logging and flap arithmetic as a live collector. Nothing here
can produce a state the production path could not. `INV_SEED_OBSERVATIONS`
controls it and is off by default — a real deployment shows the honest empty
state until something actually reports.

**"Can it fix things?"** No, deliberately and permanently. It presents state.
Nothing in the codebase can trigger a change outside it. Observed health may
inform what is *displayed*, always labelled with its reporter and age, because
showing is not acting.

**"What's the weakest part?"** The model describes *forwarding paths*, not
permitted traffic. A firewall that is up with a deleted rule, a VLAN that is not
trunked, an MTU mismatch — the model says the path exists and is silent about
all of them. Those cause most real network outages. `docs/reachability-design.md`
lists the known limits, including one place the model is knowingly optimistic.

**"How is it tested?"** Every query runs unmodified on SQLite and PostgreSQL and
the suite runs against both. The reachability work has a fourteen-scenario table
asserting the design's guarantees by number. Where a bug was found, the fix was
verified by reverting it and watching the test fail.

---

## Running it

```bash
make demo     # fresh database, seeded, observations staged, serves on :8088
```

Ports and credentials live at the top of the `Makefile`. The demo tokens in
`INV_AGENT_TOKENS` are throwaway and public by construction — they are in a
Makefile in the repository, which is fine for a laptop and a credential leak
anywhere else. There is no default for that variable in `config`.

**Restart before presenting.** A long-running instance is serving whatever code
it started with, and `go run` leaves a binary under `/tmp` that a `pkill -f
bin/invctl` will not match. Check what is actually listening:

```bash
ss -ltnp 'sport = :8088'
```
