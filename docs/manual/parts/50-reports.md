# Reports — what expires, power, impact

> Covers: `/reports/expiry`, `/reports/power`, `/reports/spanning`, `/assets/{id}/impact`, `/changes`
> Regenerated when: the expiry report, the power findings, the impact engine or
> the change log changes.

The pages that answer "so what".

## Outage simulation

The most important page in the software, and the one to open first when
something is on fire — or, better, long before.

![The impact page for hv-01. It says "If hv-01 goes away", then "Nothing breaks. No service loses enough capacity to matter, and no dependency propagates from here." Below, a table shows RELOCATED / prod-pve / 4 guests / "4 guest(s) restarted on the 2 surviving host(s); they were not serving during the restart".](../img/reports-3-impact.png)

Pick an asset and the engine takes it away, along with everything it contains,
then propagates failure along the dependency edges until the answer stops
changing.

**It applies availability policy first.** Losing one node of a three-node quorum
service reports what it should: nothing. A report that treated every instance as
critical would be wrong about most well-built estates, and you would learn to
ignore it.

Read the screenshot carefully, because it shows the two halves that must appear
together. *"Nothing breaks"* is the conclusion — and immediately beneath it, the
reason: **four guests restarted on the two surviving hosts**. Without that
second line, an operator would be told an outage was free, when in fact four
machines went through a restart. A report that silently turns an outage into
nothing is worse than one that is merely pessimistic.

Alongside the services, the page reports:

- **What was relocated**, or could not be — a cluster whose HA cannot help is
  named, with the arithmetic that says why.
- **What the outage empties**: a VLAN whose last port went, a redundancy group
  whose last router went, an overlay left terminating in one place.
- **What became isolated or partitioned** on the declared network.
- **What will not restart** — services running now with an unmet startup
  dependency. This is the highest-value line and the one a status-only view
  hides completely.
- **A safe shutdown order**, leaf-first.

Add more than one asset to the outage before running it. A redundant pair only
tells the truth when both halves can go at once.

The page also states how much of the estate it has an opinion about — how many
hosts have a modelled network attachment and how many do not. An unmodelled host
is never reported as isolated, so without that count a quiet report could mean
either "all fine" or "nothing modelled", and those are different.

## What expires

One list, ordered by date, of everything with a date: hardware support,
certificates, service end-of-life, and circuit contract ends.

Each row says **so what**. An asset carries how many services run on or inside
it and the most important among them — a switch with nothing behind it and a
hypervisor carrying a tier-1 database expire the same way and are not the same
problem. A certificate carries how many places it is deployed.

Where a date is inherited from a device type rather than recorded on the asset,
the row says so and names the model. *"Out of support in March"* and *"its model
is out of support in March, and nobody has checked this box against the
contract"* send you to different people.

A separate section lists things with **no date at all**, which is the honest
counterpart: a report of what expires is worth much less without a count of what
has never been dated.

## Power findings

Three findings, and the separation between them is the point:

**False redundancy** — two supposedly independent feeds trace to the same UPS or
transfer switch. One failure takes both, and nothing at the rack shows it.

**Single-fed** — one input, so no pretence of redundancy. Reported with what
runs on it, because a single-fed switch with nothing behind it and a single-fed
hypervisor carrying a database are not the same problem.

**Expected convergence** — two supplies meeting at a generator. This is the
design, not a fault, and it is counted separately so it never inflates the
alarming number.

The page also reports how much is **not modelled**: sites with no board at all,
inputs with no draw recorded, feeds whose capacity cannot be computed. Three
findings across four modelled assets is not a healthy estate; it is an
unmodelled one.

## Environment spans

Assets that belong to more than one environment — a hypervisor carrying both
production and development guests, a switch trunking both. Each is a place where
a boundary somebody drew on a diagram is not a boundary in the estate.

## Change log

Every change to declared state, for ever, with who made it and what changed.

Attribution is an opaque account id rather than a name or an address, so the log
carries no personal data and can be kept indefinitely. The interface resolves it
to a display name when it can; scrubbing an account answers an erasure request
while the log keeps its integrity and simply stops resolving that person.

Every entry shows **what kind of actor** made the change beside who: a person, a
system process, or an agent. The log is append-only — a wrong entry is corrected
by writing a new one, never by editing the old.

Reported health from monitoring does **not** appear here. It has its own trail,
because a 30-second poll would bury the configuration change that caused an
incident under millions of rows saying nothing changed.
