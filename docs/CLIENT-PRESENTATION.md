# invctl — client presentation source

Source material for a client demonstration. Two audiences:

1. **You**, preparing — the narrative, the order, and what to say at each point.
2. **A slide-making tool** (Claude Desktop or similar) — hand it this whole file
   and ask for a deck. Every slide below has a title, on-slide content kept
   short enough to read from the back of a room, and a speaker note that is
   *not* meant to appear on the slide.

Demo cues marked **▶ LIVE** are where you stop presenting and drive the app.
Everything here is checked against the code as of this writing; the appendix
lists the numbers you can safely quote.

---

## The through-line

Everything in this product answers one question, asked at three in the morning:

> **Something is broken. What else does this break, and who do I call?**

A monitoring dashboard tells you what is *down*. It cannot tell you what
*depends* on what is down, what will fail to come back when you restart it, or
whose phone to pick up. That gap is the product.

Keep returning to that sentence. It is the reason for every feature.

---

# Part 1 — Framing (4 slides)

### Slide 1 — Title

**invctl — know what breaks before it breaks**
Infrastructure inventory and impact analysis
*[client name] · [date]*

> **Speaker note:** Set expectation in one line: "This is a working system, not
> a mock-up — everything I show you is live and you can break it while I watch."

---

### Slide 2 — The problem

- The estate is documented in **three spreadsheets, two wikis and one person's head**
- Monitoring says *what is down* — never *what that costs you*
- The dependency map exists only in the memory of whoever built it
- When that person is on holiday, an incident takes hours instead of minutes

> **Speaker note:** Ask the client which of these they recognise. They will
> recognise all four. Do not rush this slide — the rest of the demo only lands
> if they have agreed the problem is theirs.

---

### Slide 3 — What invctl is

- A **CMDB that answers questions**, not a form to fill in
- Records what somebody *declares* is true, and what the estate *reports* about itself — and never confuses the two
- Answers impact, reachability, ownership, expiry and cost from the same graph
- **Read-only against your infrastructure.** It presents state. It never changes it.

> **Speaker note:** That last bullet is the one that gets it approved. Say it
> plainly: "This tool has no credentials to your servers and no code path that
> could restart, reconfigure or firewall anything. It is safe to put in a
> segmented environment because the worst it can do is be wrong on a screen."

---

### Slide 4 — What it is *not*

- Not a monitoring system — it consumes health, it does not measure it
- Not a configuration manager — no pushes, no remediation, no drift correction
- Not an ITSM ticketing tool
- Not a replacement for your CMDB *process* — it is the place the process lands

> **Speaker note:** Naming non-goals early buys credibility for everything you
> claim afterwards. It also pre-empts the "does it do X too?" derail.

---

# Part 2 — The model (5 slides)

### Slide 5 — Three kinds of fact

| | What it is | Who writes it | How often it changes |
|---|---|---|---|
| **Declared** | What somebody asserts *should* be true | A person | Rarely, always deliberately |
| **Observed** | What the estate reports about itself | A monitoring agent | Constantly |
| **Provenance** | Where a fact came from, and how sure we are | The system | With the fact |

> **Speaker note:** This is the intellectual core. Most CMDBs have one column
> called "status" and lose the distinction. Here, a monitor reporting `down`
> can never retire an asset, change intent, or delete a placement — only a
> person can. Conversely, a person's opinion never masquerades as a measurement.

---

### Slide 6 — Why that separation is worth the trouble

- **"Down since when"** is the incident question — kept as a separate timestamp from "last reported"
- Observed state is logged only when it **changes**, not on every heartbeat — the audit trail stays readable
- Reported `down` never sets lifecycle. **Only a person retires something.**
- A monitoring credential is not a user account: it cannot reach any write route

> **Speaker note:** If they run any monitoring today, ask what happens when a
> probe flaps at 2am. In most tools it writes a thousand rows. Here it writes
> one, on the transition.

---

### Slide 7 — The graph

**Assets** contain assets (site → rack → host → VM)
**Services** run on assets as **placements**
**Services** expose **endpoints**; endpoints carry **dependencies**
**Networks, prefixes, interfaces and addresses** decide what can reach what

> **Speaker note:** Keep this slide short and move to the live view fast — the
> map explains itself far better than a bullet list.
>
> **▶ LIVE:** Open a service → show the instances table, the endpoints table,
> the two dependency panels (upstream and downstream).

---

### Slide 8 — Ownership without personal data

- Every asset, service, certificate and project can carry a **team** and a **role**
- **No individuals are stored.** Teams and roles, never people
- Contact is a *reference* — a group address, a rota link — never a person's details
- The audit **row** stores an opaque id; the **screen** resolves it to a display name when it renders

> **Speaker note:** This is a GDPR answer, and worth saying in those words if
> the client is EU-based. "Who do I call" is answered by a team and a role, so
> the record survives somebody leaving the company.
>
> Be precise about the audit trail, because a reviewer will open the screen and
> see a name: the append-only row holds an **opaque id**, and the name is
> resolved by joining the live user record at render time. That is what makes
> erasure work — scrub the user record and the log keeps every entry and its
> integrity, and simply stops resolving to a person. Saying "the log contains no
> names" while the screen shows one is the version that loses you the room.

---

### Slide 9 — Everything is versioned and audited

- Every change to declared state writes an audit row **in the same transaction** — a change cannot escape without one
- The log is **append-only**: no edit, no delete. A wrong entry is corrected by writing another
- Nothing is ever hard-deleted. Retiring keeps the row and its history
- Two people editing the same record: the second is **refused, not silently overwritten**

> **Speaker note:** The last point demos beautifully — see the live script. Most
> tools lose one of the two edits and nobody finds out for weeks.
>
> **▶ LIVE:** Open the Change log, filter by entity type, show a field-level diff.

---

# Part 3 — What it answers (7 slides)

### Slide 10 — Impact: "what else does this break?"

Simulate losing any asset and get:

- **Affected services**, worst first, with tier
- **Won't restart** — services running *now* with an unmet startup dependency
- **Dependency cycles** — reported as findings, not hidden
- **A safe shutdown order** — leaf-first

> **Speaker note:** *Won't restart* is the killer output and the one no status
> dashboard can produce. A service can be perfectly healthy and still be unable
> to come back, because the thing it needs at boot is gone. That is the outage
> that turns a 20-minute maintenance window into a 6-hour one.
>
> **▶ LIVE:** Asset detail → "Simulate losing this". Walk the affected list,
> then stop on Won't restart and explain it slowly.

---

### Slide 11 — Reachability: "can it actually get there?"

- Impact is not only "is the provider up" — it is also **"can the consumer reach it"**
- Network groups, uplinks and attachments model the paths
- Reports assets **isolated** from their network, edges **partitioned** by a network cut, and **redundancy lost** where a group survived but with no spare

> **Speaker note:** This is the difference between a dependency list and a model.
> Two services can both be up and still be unable to talk, because the path
> between them went through the switch you just lost.
>
> **▶ LIVE:** Paths view, or an environment map with the layers toggled.

---

### Slide 12 — Search that understands what you typed

Type any of these and land on the thing:

- an **IP address** — resolved by range, not text match
- a **CIDR** — the network and everything in it
- a **MAC address** — however it was pasted
- a **port** — every service listening on it
- a **serial number**, a **hostname**, a **service code**

> **Speaker note:** Structured lookups run *before* free-text search, so an IP
> gives you the interface and its asset, not thirty documents mentioning it.
>
> **▶ LIVE:** Search an IP from the seeded estate. Then a port. Then a MAC in a
> different format from the one stored, to show normalisation.

---

### Slide 13 — What expires

One report over **hardware support, service licences and TLS certificates**:

- Everything with an end date inside the horizon, plus everything already past it
- Who owns it, and **what rides on it**
- Certificates included — the thing that actually pages people
- **Counts what it cannot see**: how many things have no date recorded at all

> **Speaker note:** That last bullet is the honest one and clients notice it.
> "An estate where nothing appears to expire is usually an estate where nobody
> wrote the dates down." Nothing here *acts* when a date passes — it changes
> what the report says, and a human decides.
>
> **▶ LIVE:** Expiry report, change the horizon, point at the "what this report
> cannot see" callout.

---

### Slide 14 — Certificates

- A certificate is an **entity**, not a field — one wildcard is genuinely deployed in several places
- Deployed onto **assets and services**; the reverse view answers "what TLS is on this box"
- Names it covers are searchable, so a hostname resolves straight to the certificate
- **Never the private key.** A path or vault reference only, and the form refuses a pasted key

> **Speaker note:** The refusal is a real control, not a policy: paste a PEM
> block into the names field and it is rejected. It matters because anything
> accepted there would become searchable *and* permanent in an append-only log.

---

### Slide 15 — Money

- Optional per thing: **one-off acquisition, yearly support, monthly running cost**
- Costs carry a **validity window**, so last year's price is still answerable for
- **Amortisation**: spread a purchase from the day it was paid to the day support ends
- Corrections **amend** the line rather than replacing it — a typo does not become a price change

> **Speaker note:** The distinction that sells this: a run rate is what leaves
> the bank this month; capital is what was spent once. Adding them together is
> the mistake every spreadsheet makes.

---

### Slide 16 — Money at project level

Three buckets, **never added together**:

- **Own** — what this project pays for
- **Elsewhere** — inside its footprint, but another project's bill
- **Shared** — things it *uses* and somebody else pays for

Plus: *"12 of 19 things in this footprint carry a price"* — so the total is a **floor, not a budget**

> **Speaker note:** A project that runs on a hypervisor it does not own must not
> absorb that hypervisor's cost, and must not pretend the cost does not exist.
> Three buckets is the honest answer. The unpriced count is the second honest
> answer: the tool tells you how much of the estate it cannot cost.
>
> **▶ LIVE:** A project page → the cost summary and the three buckets.

---

# Part 4 — Trust and operations (4 slides)

### Slide 17 — It cannot touch your infrastructure

- **No outbound credentials.** No SSH, no API keys to your estate, no agents it controls
- Observed health arrives by a **narrow, scoped token** that can only report on entities that already exist
- An observation for something unknown is **rejected, never created**
- Runs happily in a **segmented network with no internet** — no CDN, no external fonts, no phone-home

> **Speaker note:** Say this to the security reviewer in the room, not the
> sponsor. The narrow-token point matters: a leaked monitoring token cannot be
> used to insert inventory.

---

### Slide 18 — Deployment

- **One static binary.** No runtime, no interpreter, no container required
- **SQLite** for a single box, **PostgreSQL** when it grows — *the same build, the same queries*
- Migrations run at startup and are verified on both engines
- Login: local accounts today, **LDAP bind** against your directory

> **Speaker note:** The dual-engine point is worth dwelling on for a POC
> conversation: start on SQLite with zero infrastructure, move to Postgres
> later without a rewrite or a migration project.

---

### Slide 19 — Built to be checked

- **576 automated tests**, every one run against **both** database engines
- The impact engine is tested against a real fixture estate, not mocks
- Rules the codebase enforces on itself: audit coverage, no personal data in the log, no hard deletes, portability between engines
- Reviewed for security and correctness, repeatedly, with findings acted on

> **Speaker note:** Use this if the client is technical or is buying a POC they
> intend to extend. The tests are the argument that the model holds, not the
> screenshots.

---

### Slide 20 — Where it is today, honestly

**Working now:** everything demonstrated above
**Deliberately not built yet:** discovery agents, automated linting, firewall
reconciliation, a read-only inventory API for Ansible

- This is a **proof of concept with production discipline** — not a prototype to be thrown away
- The data model and the audit rules are the part that is expensive to change later, and they are the part that is finished

> **Speaker note:** Do not overclaim. A client who is told the boundary honestly
> will trust the rest. If they ask for discovery, the answer is "the model is
> ready for it; the agents are the next milestone", which is true.

---

# Part 5 — Close

### Slide 21 — Back to the question

> **Something is broken. What else does this break, and who do I call?**

- **What else** — the impact simulation, including what will not come back
- **Who** — the team and the role, with no personal data
- **What it costs** — with capital and run rate kept apart
- **What it proves** — an append-only record of who changed what, and when

> **Speaker note:** Land on the same sentence you opened with. Then stop talking
> and offer the keyboard: "What would you like to try breaking?"

---

# The live demo script

Roughly 12 minutes. Each step assumes you are logged in as an admin.

| # | Do this | Say this |
|---|---|---|
| 1 | **Overview** | "Everything is one estate. Counts, not dashboards." |
| 2 | **Search an IP** from the seed | "It resolved the address to an interface and its host — that is a range lookup, not a text match." |
| 3 | Open the **asset** it found | "Ports, addresses, what runs here, what it costs, when support ends." |
| 4 | **Simulate losing this** | "Affected services, worst first." Then: "and *this* list is what a status dashboard cannot show you — these are running right now and will not come back if anything restarts them." |
| 5 | Back → **Edit** the asset | "Correcting what a thing *is*. Notice what is not offered: I cannot move it to another rack from here, because that rewrites the containment graph and it has its own flow." |
| 6 | **Open the same record in a second tab**, save in one, then save in the other | "Refused — somebody else changed it while I had it open. It kept what I typed and tells me to go and look. Most tools silently lose one of those two edits." |
| 7 | **Add a cost** to the asset, then correct it | "Amending, not replacing. Last year's price is still answerable for." |
| 8 | Open a **project** | "Three buckets that are never added together, and it tells me how much of the footprint it cannot price." |
| 9 | **What expires** | "Hardware, licences and TLS in one place — and a count of what has no date at all." |
| 10 | **Change log** | "Every change, field by field, in the same transaction as the change. Append-only." |
| 11 | *(If asked about scale)* mention Postgres | "Same binary, same queries, different DSN." |

**Two things to have ready before you start:**

- a second browser tab open on the same record, for step 6
- an IP address from the seeded estate on a sticky note, for step 2

**If something goes wrong live:** the audit trail is your friend — open the
change log and show what just happened. A tool that can explain its own
behaviour under pressure is a better demo than one that never stumbles.

---

# Appendix — facts you can quote

| Claim | Figure |
|---|---|
| Automated tests | **576**, run against both SQLite and PostgreSQL |
| Go source files | 99 source, 76 test |
| Database tables | 60 |
| Schema migrations | 29, each verified on both engines |
| Screens | 26 read views, 62 write actions |
| Search types resolved structurally | IP, CIDR, MAC, port, serial, hostname, service code |
| Runtime dependencies | none — one static binary |
| Outbound network calls | none |
| Licence | AGPL-3.0-only |

**Phrases that are accurate, and worth reusing:**

- "It presents state. It does not act on the estate."
- "Only a person retires something."
- "A run rate is what leaves the bank this month."
- "An estate where nothing appears to expire is usually an estate where nobody wrote the dates down."
- "Teams and roles, never people."
- "The total is a floor, not a budget."

**Do not claim:** automatic discovery, configuration drift detection, firewall
management, ticket integration, or an inventory API. Those are named as
post-POC in the project's own roadmap, and a client will find out.

---

# Notes for the slide-making tool

If you are generating a deck from this file:

- Keep on-slide text to what is written under each heading. The `> Speaker note`
  blocks are notes, never slide content.
- The tables on slides 5 and 16 should stay tables — they carry the distinction
  that matters.
- Slide 10 (`Won't restart`) and slide 16 (three buckets) are the two moments
  the audience should remember. Give them visual weight.
- The demo script is a presenter handout, not slides.
- Dark, dense, technical. This is an operator's tool shown to people who run
  infrastructure — not a consumer product. Avoid stock imagery entirely;
  a screenshot of the real thing beats any illustration.
