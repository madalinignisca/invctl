# Bulk import

Create many assets at once from a CSV file. Sign in as an administrator and go
to **Assets → Import a CSV**, or straight to `/import/assets`.

This page is the reference. The import screen itself carries a shorter version
of the same thing, because that is where somebody building a file is actually
looking.

---

## The three rules

**It creates. It never updates.** A row naming something that already exists is
reported and the file is refused. That is deliberate: a file that quietly
rewrote four hundred assets would write four hundred audit entries nobody had
reviewed, and there would be no way to tell afterwards which values a person
chose and which a spreadsheet overwrote. To change something that exists, edit
it — the change log then records who, when, and from what.

**The whole file lands, or none of it does.** Every row shares one database
transaction. A fault on line 300 leaves the first 299 unwritten. A partly
applied import is the worst outcome available: you cannot tell what landed, and
re-running the corrected file collides with its own successful half.

**A preview runs the real thing and throws it away.** Tick *Preview only* and
the import performs the actual inserts — same validation, same constraints, same
containment rebuilding — inside a transaction that is then discarded. It is not
a simulation, so what it lists is what a real run creates. Nothing is written,
including audit entries.

Because of the second rule, the preview is a convenience rather than a
safety net. Submitting a bad file without previewing changes nothing.

---

## Identifying an asset: the path

An asset is named by its **path** through the containment tree: each name
joined with `/`, outermost first.

```
dc-a
dc-a/rack-1
dc-a/rack-1/esx-01
```

A name has to be unique among an asset's **live siblings** — so two racks called
`rack-1` in different sites are fine, and so is `web-01` in two clusters. A
retired asset does not hold its name, so a path freed by retiring something can
be used again.

The `parent` column holds the parent's path, and is empty for a top-level asset.

**Rows may appear in any order.** A row whose parent is created further down the
file still resolves. Sort the spreadsheet however you like.

---

## Columns

The first line is a header naming the columns. Their order does not matter.

| Column | Required | Meaning |
|---|---|---|
| `name` | yes | What the asset is called. |
| `kind` | yes | A code from the **asset kinds** vocabulary — see below. |
| `parent` | | The containing asset, as a path. Empty means top level. |
| `serial` | | Manufacturer serial number. |
| `asset_tag` | | Your own asset tag. |
| `vendor` | | |
| `model` | | |
| `lifecycle` | | `planned`, `active`, `maintenance`, `deprecated`, `retired`. Defaults to `active`. |
| `eol_date` | | End of support, `YYYY-MM-DD`. Feeds the expiry report. |
| `environments` | | Environment codes, comma separated: `prod,dr`. |
| `team` | | The owning team's **code**. |
| `manager_role` | | A code from the **responsibility roles** vocabulary: `owner`, `operator`, `approver`, `oncall`, `custodian`, `vendor`. Requires `team`. |

### Kinds are a vocabulary, not a fixed list

`kind` is checked against the `asset_kind` vocabulary, which an administrator
can extend at **/vocabularies**. Look there for the authoritative set rather
than trusting this page — an estate that has added its own kinds will accept
them here too.

As shipped: `site`, `rack`, `pdu`, `firewall`, `switch`, `patch_panel`,
`server`, `hypervisor`, `cluster`, `vm`, `k8s_node`, `bridge`, `storage`.

**A column that is not in the table above is refused, not ignored.** A misspelled
`lifecyle` would otherwise be dropped silently, the asset created with the wrong
lifecycle, and the result page would cheerfully report a success.

**Teams and roles, never people.** `team` names a team and `manager_role` names
a capacity. This system does not record individuals, and an import is not an
exception.

An empty cell means *not stated*, which is stored as no value rather than as an
empty one.

---

## An example

```csv
parent,name,kind,vendor,model,eol_date,environments,team,manager_role
,dc-a,site,,,,,platform,owner
dc-a,rack-1,rack,,,,,platform,owner
dc-a/rack-1,esx-01,hypervisor,Dell,R650,2029-03-31,"prod,dr",platform,operator
dc-a/rack-1,esx-02,hypervisor,Dell,R650,2029-03-31,"prod,dr",platform,operator
```

Quote any cell containing a comma, as `environments` does above.

---

## When it refuses

The result page lists every problem it found, each with its line number, the
path the row was trying to create, and the field at fault. It reports the
**whole** list rather than the first failure — fixing a four-hundred-line file
one error per upload is how people give up on a feature.

Common ones:

| Problem | What it means |
|---|---|
| `there is no asset at "dc-x", and nothing in this file creates one` | The `parent` path does not match an existing asset, and no row in the file creates it. Check spelling and the full path. |
| `this already exists; import creates, it does not update` | Something live already sits at that path. Edit it instead, or retire it first. |
| `line 12 already claims this path` | Two rows in your own file describe the same asset. |
| `there is no team with the code "…"` | Create the team first, or correct the code. |
| `there is no asset column called "…"` | A header is misspelled or unsupported. |

---

## Limits

A file may be up to **1 MiB**, which is roughly fifteen thousand rows. A larger
upload is refused with a message saying so rather than a bare error.

---

## What gets recorded

Each imported asset gets **its own** entry in the change log — one per asset,
not one per file — attributed to the administrator who uploaded it. "Who put
this box in the inventory, and when" stays answerable per box, and each entry
is citable at `/changes/{id}`.

A preview writes no audit entries at all.

---

## Not yet importable

Services, dependencies, interfaces, addresses and certificates. Only assets so
far.

Custom attributes (`attrs`) are deliberately excluded: a file that could write
into them would be a file that could put anything anywhere, and a value worth
filtering or reporting on should be a real column rather than an attribute.
