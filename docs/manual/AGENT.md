# Regenerating the manual — instructions for an agent

You are reading the contract, not the manual. The manual is in `parts/`, written
for a human operator. This file tells you how to rebuild it, and — more
importantly — **how to rebuild only the part that changed**.

Read this whole file before touching anything. It is short.

---

## The one idea

A manual regenerated from scratch on every change is a manual nobody
regenerates. So each fragment declares **which source paths it describes**, and
the commit it was generated at. Staleness is then a question with an answer:

```
git diff <generated_at>..HEAD -- <depends_on paths>
```

Non-empty means that fragment is stale. Empty means it is current, whatever
else has changed in the repo. `tools/manual-stale.sh` does this for every
fragment and prints the list.

**Regenerate only what that script names.** Rewriting a current fragment costs a
review of prose nobody needed to read again, and loses whatever a human improved
by hand since.

---

## What you must not do

**Do not invent screenshots.** Every image in `img/` must come from an actual
navigation of a running instance. A drawn or described screenshot is worse than
none: the reader trusts it and it silently stops matching.

**Do not write about features you have not opened.** If a page will not load,
say so in the fragment and leave the section stubbed. A manual that describes a
page that 500s is a manual that has stopped being checked.

**Do not change `MANIFEST.yaml`'s `depends_on` to make a fragment look current.**
If the paths are wrong, fix them and regenerate — that is the mechanism working,
not failing.

**Do not describe the demo estate as if it were the reader's.** The demo has
Norwegian sites and a Proxmox cluster. The reader has neither. Screenshots show
the demo; prose describes the software.

---

## Preconditions

Everything below assumes a running instance with the demo estate. The public one
is at `https://invctl.madalin.me` with `admin` / `demo-password` — deliberately
writable, deliberately published.

Prefer a **local instance** if you are going to change data. The public demo is
shown to people, and a manual run that leaves half-built clusters behind is
rude. To run one:

```
sqlite3 ~/apps/invctl-demo/invctl.db ".backup '/tmp/manual.db'"
INV_LISTEN=127.0.0.1:8099 INV_DB_DRIVER=sqlite INV_DB_DSN="file:/tmp/manual.db" \
  INV_ADMIN_USERS=admin INV_ADMIN_PASSWORD=demo-password INV_SECURE_COOKIES=false \
  ./bin/invctl
```

Check the port is free first — this machine has other services, and binding a
used port silently sends your requests to somebody else's application. That has
happened: a run against port 8090 spent ten minutes debugging 405s that were a
mail admin app answering.

---

## Capture rules

These are not preferences. A manual whose screenshots drift in size, theme and
crop looks unmaintained, and the differences read as meaning.

| Rule | Value | Why |
|---|---|---|
| Viewport | **1440 × 900** | fits the rail plus a wide table without a horizontal scrollbar |
| Theme | **light** | headless Chromium's default, and it prints. The app is dark-first, so this is a deliberate choice, not an accident — do not mix the two |
| Scale | **css** | device scale doubles the file size for no legibility on a manual page |
| Format | **png** | screenshots of text; jpeg artefacts on hairlines |
| Full page | **only where named** in `MANIFEST.yaml` | a full-page shot of a long table is unreadable at manual width |
| Path | `docs/manual/img/<fragment-id>-<n>-<slug>.png` | sorts with the fragment, and the slug says what it shows |

**The nav rail ships collapsed.** Every group except the one holding the current
page is shut. If a screenshot is meant to show navigation, open the groups you
are describing first; otherwise leave them as they are, because that is what the
reader sees.

**Log in before capturing anything.** An unauthenticated request redirects to
`/login`, and a screenshot of the login page filed as `assets-list` is the kind
of error nobody notices for a year.

---

## Procedure

1. Run `tools/manual-stale.sh`. It prints the fragment ids needing work.
2. For each one, read its `MANIFEST.yaml` entry: the pages, the screenshots and
   the **demo preconditions**.
3. Check the preconditions hold. They exist because a screenshot of a page in
   its boring state teaches nothing — `parts/40-network.md` is meant to show a
   redundancy group with one member, and if the demo has none, the image is
   worthless even though the page loaded. Fix the demo data first, or say in the
   fragment that the state could not be shown.
4. Navigate, capture, write.
5. Update that fragment's `generated_at` to the current `HEAD` sha. **Only that
   fragment's.**
6. Run `tools/manual-stale.sh` again. The fragment should be gone from the list.

---

## Writing the prose

The audience is an operator, not a buyer. `HANDOVER.md` puts it exactly: the
reader is *a person during an incident*, and the output is *understanding*.

- Say what the page answers, then what the controls do. Not the reverse.
- Name the finding, not the widget. "A VLAN with no ports is a record rather
  than a broadcast domain" beats "the Ports column shows a count".
- Where the software refuses something, say why — the refusals are the design.
  A port cannot have two untagged VLANs; a VLAN in use cannot be withdrawn.
- Keep a fragment under roughly 200 lines. If it wants to be longer it is
  probably two fragments, and two fragments regenerate independently.
- No marketing. No "powerful", no "seamless", no "simply".

## Fragment file shape

```markdown
# <Title>

> Covers: /path, /path/{id}
> Regenerated when: <the plain-English version of depends_on>

<one paragraph: what question this area of the software answers>

## <Screen>

![<alt text that stands in for the image>](../img/<file>.png)

<what the reader is looking at, what to do, what the software will refuse>
```

The alt text matters more here than usual: it is what a reader gets when the
image is missing, and it is what YOU get when auditing a fragment you did not
capture.
