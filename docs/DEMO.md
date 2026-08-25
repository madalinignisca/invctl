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

> 7 services affected · 7 assets isolated on `data`
> sso · **down** · *lost 1 of 1 instances (running, but network-isolated — not powered off)*
> vault · degraded · *soft dependency on sso/https is down*

The MC-LAG group is still **healthy** — one of two chassis is plenty. And yet
`hv-03` is cut off, because it is single-homed to *this* chassis. `hv-01` and
`hv-02` have a second cable and are fine.

The seven are `hv-03`, its five guests, and `hv-03-br0` — the Linux bridge the
guests are cabled to, which is an asset in its own right since the virtual layer
landed. Everything inside a cut-off host is cut off with it, and nothing here is
special-cased: all seven inherit the hypervisor's attachment through
`asset_closure`.

Group health and host reachability are different questions with different
answers in the same run. No MC-LAG-specific code produces that; the second
attachment-member row does all the work.

Note the wording: *running, but network-isolated — not powered off*. During an
incident those call for completely different responses.

Contrast with **sw-oob-1 → Impact**: 19 assets unmanageable on the **management**
plane, and **no service affected at all**. Both planes are evaluated in the same
run and only one decides status.

## 3. The picture of all of it (2 min)

Open **Assets → hv-01 → Neighbourhood**.

Everything sections 1 and 2 described in words is one drawing here: services on
top, the virtual layer (bridges, bonds) in the middle, physical hardware at the
bottom. It is server-rendered SVG — no JavaScript graph library, the same
`html/template` as every other page — and the whole state of the picture lives
in the URL, so pasting the link to a colleague on an incident call shows them
*exactly* what you see, layers and hop count included.

Things worth pointing at:

- **The layer toggles.** Turn the virtual layer off and the bridges drop out;
  the counts on each toggle say what it would add back. The subject's own layer
  is locked on — a picture of hv-01 that hides hv-01 is not a picture of it.
- **The backup agent's line.** `backup-agent` runs directly on the hypervisor —
  bare metal, no guest in between — so its line spans from the service band to
  the physical band and threads the *gap between* the virtual-band boxes rather
  than drawing through them. Every line in the picture obeys that rule, and the
  test suite measures it on coordinates, not on intent.
- **Dependency arrows.** The arrowhead sits at the thing depended *on*:
  `orders-api ⟶ pgsql-core` reads in the direction of the dependency. Cables
  have no arrows because a wire has no direction.
- **Same-band connections are rails at distinct depths.** Two cables between
  the same pair of switches — the MC-LAG peer bond — are two separate lines
  with two separate hover texts naming their ports, not one line pretending.
- **The box edges are observed health**, same colour code as everywhere else:
  the diagram shows what the estate reports, labelled as observed, and acts on
  nothing.

The hover text on every line names the exact ports or endpoints it asserts, and
the table below the picture carries the same facts for anyone who prefers rows.

Then open **Paths** in the rail (or *"Where does it sit — path diagram"* on any
service page) and ask *orders-api → pgsql-core*. The picture is the chain and
nothing else: both database replicas, both hypervisors, both cores — the union
of every equally short data-plane route, because traffic from the app can land
on either replica and each host is dual-homed. Two things worth saying out
loud: **management cabling is never a path** (the OOB switch reaches
everything, and a naive shortest-path search would happily route production
traffic through the console network), and a placement with **no** route stays
in the picture as a disconnected box — a stranded instance is the finding, not
clutter. Leave the far end empty and the same page answers *"where does this
service actually sit"*, down to the chassis its attachment lands on.

**Then ask *log-shipper → pgsql-core*, which is the sharpest thing on the
page.** Two shippers are declared; one of them runs on `srv-backup-proxy-1`, a
box that was racked last week with its console cable patched and its data
uplink still waiting on a remote-hands ticket. The cabled instance gets a
chain. The other is drawn as **a box connected to nothing**, and the page says
so in words: *"No data-plane route from: srv-backup-proxy-1."* Nothing is
tidied away, because a placement that cannot reach what it is supposed to
reach is the finding.

It also demonstrates the data-plane rule on real data rather than asserting
it: that box **has** a cable — to the console switch — and still has no path.
The same asset is the one box with no lines on the prod environment map, and
`sw-oob-1 → Impact` counts it among the unmanageable while making no
data-plane claim about it at all.

Finally, **Environments → prod → Map** is the whole environment at once — the
densest of the three diagrams, which is exactly why the layer toggles exist.
Neighbourhood for one asset, Paths for a pair, the map for the walkthrough.

## 4. Declared versus observed (3 min)

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

## 5. The audit trail (2 min)

Open **vm-queue-1** and scroll to its timeline. It is a merged view of declared
changes and observed transitions, and it shows a **flap episode**: the entity
oscillated enough to trip compression, and the closing row says how much it hid.
Compression a reader cannot size is just suppression.

Then **Changes** in the nav. Every declared mutation, with `actor` and
`actor_kind` side by side. The actor column holds an opaque id, never a username
— so the trail carries no personal data, can be kept indefinitely, and scrubbing
an account answers an erasure request while the log keeps its integrity.

**One caveat if it comes up:** custom fields are free text an administrator
defines the meaning of, folded into the same audited entry as a plain change
counter rather than the text itself — so a change shows that a value moved,
which field, and how many times it has changed, never what it changed to.
That is not the same as saying the value is gone: it still lives on the
entity's own page, unencrypted, for as long as anybody keeps it there, so the
product still warns against a field like "Owner email" at the point of
typing. The counter closes the audit-trail exposure, not the row.

**The fix is forward-only, and this instance shows it, twice over**: the log
is append-only, so any change-log entry written before this fix shipped
still holds the plaintext value, unchanged, and an entry written in the
handful of days this feature briefly used a keyed HMAC digest instead still
holds that digest — do not claim otherwise against a live demo. The counter
that replaced the digest is not a cryptographic primitive at all: there is no
key, nothing to generate, and nothing to configure — the number is not
personal data under any reading, because it carries no information about the
value.

---

## 6. Getting an estate in without typing it (3 min)

Two things nobody demos and everybody asks about: how the data gets in, and how
you avoid typing an end-of-support date forty times.

**Catalogue → Import a CSV.** Upload a hardware list. Tick *Preview only* first
— it runs the real import against the real constraints and then throws the
transaction away, so what it lists is exactly what a real run creates. Nothing
is written, not even an audit row.

Then upload it for real, and upload it **again**. The second attempt is refused:
import creates, it never updates. A file that quietly rewrote four hundred
assets would write four hundred audit entries nobody reviewed.

**Now open `hv-03`.** It has no end-of-support date of its own, so it reads:

> **2029-03-31** · in 2 years
> *inherited from Dell PowerEdge R650 — this asset has no date of its own*

**Then open `hv-01`.** Same model, but somebody recorded a date on the box:

> **2026-10-03** · in 2 months
> *recorded on this asset*

That second line is the point, and it is worth pausing on. A manufacturer's
claim about a MODEL and somebody's claim about THIS BOX are different kinds of
fact. A private support contract can carry one unit years past what its model
promises; a second-hand unit can fall short of it. Showing the two identically
would merge a fact with an assumption — so the date never appears without its
source, exactly as `actor` never appears without `actor_kind`.

**Search `P30721-B21`.** A part number resolves to the model and says how many
boxes are of it, because "do we have any of these" is the question behind
pasting one. Serials work too, in whatever case they were read off the sticker.

**Assets → Import a CSV** does the same for the estate itself: a path per row
(`dc-a/rack-1/esx-01`), rows in any order, whole file or nothing.

---

## 7. The one nobody can find by looking (3 min)

This is the part to slow down for.

**Power → Findings**, or `/reports/power`. Three findings on the demo estate:

> **hv-01** — inputs A and B look redundant but every one traces to panel
> **DB-A** — one failure takes all of them

Stop there. Two power leads, two feeds, two separate circuits — everything a
spreadsheet records, and everything an operator believes. Both circuits come off
the same distribution board. It is not redundancy, it is two cables to one point
of failure, and nobody finds it during normal running. They find it during that
panel's first and only failure.

**hv-02 is not in the list**, and that matters as much. Its A and B are on
different panels, so it is genuinely redundant and the report says nothing about
it. A finding that fired on every asset with two inputs would be worthless.

The other two: **hv-03** is single-fed and *five services ride on it* — reported
because something depends on it, not merely because it has one lead. And feed
**A1** is over its derating, stated as "3600 VA allocated against 2944 VA usable
(80% of 3680 VA)" rather than as a percentage nobody can check.

**Then click "If it fails" on A1.** It resolves and lands on the ordinary impact
page — the same screen, the same window control — showing what actually goes
dark. **hv-01 is not in it**, because it still has its B lead. Redundancy that is
modelled and then ignored is worse than not modelling it.

**Now click "If it fails" on A2.** It goes nowhere and says:

> Nothing loses power if DB-A / A2 fails: every asset on it has another live input.

"Nothing breaks" and "nothing loses power in the first place" are different
answers, and showing an empty impact page for the second is the most dangerous
thing an inventory can do.

Scroll to the bottom of the findings page for the coverage counts — how many
sites have no panel modelled at all. Three findings over four modelled assets is
not a healthy estate; it is an unmodelled one, and the page says so.

---

## 8. The read-only API (3 min)

The rest of this walkthrough is a person looking at pages. `/api/v1` is the
other consumer: Ansible's dynamic inventory, and a metrics system resolving a
label back to a name and a placement. `docs/API.md` is the full reference;
this is enough to show it live.

**Not staged by default.** `INV_API_TOKENS` has no default anywhere in
`config` — same reasoning as `INV_AGENT_TOKENS` — so the public demo, as
handed over, does not mount `/api/v1` at all. Set it before this section:

```bash
export INV_API_TOKENS=ansible:demo-api-token-000000000000000000
export INV_API_SCOPES=ansible:prod|dev|staging|dr|transit
```

Restart with those set (`make demo` picks up whatever is exported, or add
them to the systemd unit's `Environment=` lines for the public instance) and
the routes answer. Every misconfiguration here is a startup failure, not a
silent gap — an id in one variable with no matching entry in the other
refuses to start rather than serving half a scope.

**One asset, to show scope is real:**

```bash
curl -s -H "Authorization: Bearer demo-api-token-000000000000000000" \
  https://invctl.madalin.me/api/v1/assets?kind=hypervisor | jq
```

Three hypervisors come back — `hv-01`, `hv-02`, `hv-03` — each with its
`site`, `rack` and the services placed on it. Nothing about observed health,
nothing about cost, nothing about a team's contact details: this is declared
state only, and `docs/API.md` explains why on purpose.

**Then the composed view Ansible actually consumes:**

```bash
curl -s -H "Authorization: Bearer demo-api-token-000000000000000000" \
  https://invctl.madalin.me/api/v1/ansible | jq '.env_prod.hosts, .svc_vault'
```

`env_prod` lists every asset in the production environment; `svc_vault`
lists `vm-vault-1`, `vm-vault-2`, `vm-vault-3` — the same placement the
Neighbourhood diagram in section 3 drew as a picture. Point
`ansible-playbook -i` at the same URL and it needs no adapter: `_meta.hostvars`
plus one `{"hosts": [...]}` object per group is the native shape.

**Worth saying if it comes up:** custom fields (section 5's caveat, section
on WP-A4) never reach this surface, on any route, by design —
`TestCustomFieldsNeverReachTheAPI` pins it. An estate's own attributes are
administrator-defined and administrator-retirable; an automation pipeline
must not come to depend on a field somebody else can delete tomorrow.

---

## If somebody asks

**"Is any of this faked for the demo?"** No. The inventory is a seeded fixture,
plus whatever visitors and the hardware-catalogue import have added since.
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

The virtual layer makes one of those concrete and it is worth showing rather
than hiding: `hv-03-br0` appears in the segmentation-span report beside the two
core switches, because the production guests and the development guest on
`hv-03` are cabled to the same bridge. In a real estate you would separate them
onto `bond0.30` and `bond0.40`, and `interface` has no VLAN column to say so —
so the fixture declares the bridge in both environments and lets the report find
it, which is the honest version of a gap.

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

### The public instance

`invctl.madalin.me` terminates TLS in **haproxy-edge on whitebox** — the
parent host of this VM — which forwards to this VM's IP on **8088**. The app
itself is a **user systemd unit** here, so it survives crashes and reboots,
which a background process demonstrably did not:

```bash
systemctl --user status invctl-demo        # 0.0.0.0:8088, TLS on whitebox
```

Unit: `~/.config/systemd/user/invctl-demo.service`; binary and database in
`~/apps/invctl-demo/`. The seeder only runs against an empty database, so a
restart alone keeps visitor changes. To redeploy and/or reset the estate:

```bash
make build && install bin/invctl ~/apps/invctl-demo/invctl   # redeploy
systemctl --user stop invctl-demo
rm ~/apps/invctl-demo/invctl.db*                             # reset (optional)
systemctl --user start invctl-demo
```

Reset before presenting — the estate is writable and holds whatever visitors
left, and the staged telemetry reads "just now" only near seed time.

---

## What the app needs behind a TLS-terminating proxy

The deployment itself is handled separately. These are the application-side
requirements, established by running the app behind a simulated proxy rather
than by reading the middleware — two of the three are not hardening, and getting
them wrong produces a 400 with no useful message.

| What reaches the app | Result |
|---|---|
| original `Host` + `X-Forwarded-Proto: https` + browser Referer | **303 — works** |
| Referer from another origin | 400 — CSRF correctly refuses |
| **`X-Forwarded-Proto` missing** | **400 — every form post rejected** |

1. **`INV_SECURE_COOKIES=true`.** One switch, three jobs: `Secure` on the
   session cookie, `Secure` on the CSRF cookie, and — the part that surprises —
   it is the *only* thing that makes the app trust `X-Forwarded-Proto`. Without
   it the app believes it is serving plaintext while the browser believes
   otherwise, and nosurf refuses the mismatch.
2. **`X-Forwarded-Proto: https` must reach the app.** Without it, logging in
   fails with a 400 and no explanation. A broken deployment, not a weakened one.
3. **The original `Host` must be preserved.** The CSRF check compares the
   Referer against `r.Host`; forward the upstream address instead and every post
   is rejected as cross-origin.

Also worth setting: `INV_SESSION_KEY`, or a random one is generated per start and
everyone is signed out whenever the process restarts. And `INV_LISTEN` bound to
loopback, so the plaintext port is not reachable directly.

HSTS belongs on the proxy — the app cannot know whether every hostname it is
served under is HTTPS-only.

### The public instance is writable, and that is a decision

`admin` / `demo-password` is in this file and in the `Makefile`, and so are the
two agent tokens. On a public URL that means **anyone can sign in and write**:
retire assets, create overrides, run the network derive, POST observations that
change what the dashboard says.

Accepted deliberately for a short-lived demo. What it costs:

- The estate is whatever visitors leave behind. Re-run `make demo` before
  showing it to anyone.
- Someone can make the dashboard say something false while you are presenting.

What it does **not** cost, because the M6 boundaries hold regardless of who
holds the token: an agent credential still cannot create inventory (unknown
entities are 404 and queued as drift, capped per reporter), cannot write
`source = 'declared'`, cannot sign off `verified_by`, cannot set its own
confidence, cannot reach any route but `POST /observations`, and cannot exceed
its environment scope.

To close it later, cheapest first:

- Change `INV_ADMIN_PASSWORD` and drop `INV_AGENT_TOKENS` — visitors browse, and
  nothing writes.
- Or set `INV_ADMIN_USERS=` empty: every account becomes read-only,
  `authz.CanWrite` returns false for everyone, and the write routes 403. The
  closest thing to a safe public demo, at the cost of not being able to
  demonstrate the override flow live.
