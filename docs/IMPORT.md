# Bulk import

Create many things at once from a CSV file. Sign in as an administrator:

| What | Where |
|---|---|
| Assets | **Assets → Import a CSV**, or `/import/assets` |
| Device types | **Catalogue → Import a CSV**, or `/import/device-types` |

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
| `serial` | | Manufacturer serial number. Searchable as an exact identifier, in any case. |
| `asset_tag` | | Your own asset tag. |
| `vendor` | | |
| `model` | | |
| `lifecycle` | | `planned`, `active`, `maintenance`, `deprecated`, `retired`. Defaults to `active`. |
| `eol_date` | | End of support, `YYYY-MM-DD`. Feeds the expiry report. |
| `environments` | | Environment codes, comma separated: `prod,dr`. |
| `team` | | The owning team's **code**. |
| `manager_role` | | A code from the **responsibility roles** vocabulary: `owner`, `operator`, `approver`, `oncall`, `custodian`, `vendor`. Requires `team`. |
| `device_type` | | A catalogued model as `manufacturer/model` — `dell/PowerEdge R650`. The asset inherits its end-of-support date. |

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

`device_type` matches case-insensitively, so `dell/r650` finds `dell/R650`. In
the one situation where that is genuinely ambiguous — two catalogued models
differing only in capitalisation — the row is refused rather than resolved to
whichever came first.

**Teams and roles, never people.** `team` names a team and `manager_role` names
a capacity. This system does not record individuals, and an import is not an
exception.

An empty cell means *not stated*, which is stored as no value rather than as an
empty one.

---

## An example

```csv
parent,name,kind,device_type,eol_date,environments,team,manager_role
,dc-a,site,,,,platform,owner
dc-a,rack-1,rack,,,,platform,owner
dc-a/rack-1,esx-01,hypervisor,dell/R650,,"prod,dr",platform,operator
dc-a/rack-1,esx-02,hypervisor,dell/R650,2031-12-31,"prod,dr",platform,operator
```

`esx-01` inherits its end-of-support date from the `dell/R650` model.
`esx-02` is the same model but states its own — a support contract on that
particular box — and its own date wins. Every screen showing either one says
which of the two it is.

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

## It runs in the background

A **preview** answers immediately — it is you standing there asking a question.

A **real import** is queued and runs on the server. You get a page showing
progress, and you can close it: the import carries on and **Imports** shows how
it ended. Measured at about 1.4ms a row, so five thousand assets take roughly
seven seconds and a full file would sit well past any proxy timeout if it tried
to answer in the request.

While it runs the page says **"examined N of M rows"**, not "N imported". The
distinction is real: the import is one transaction, so a run that stops at row
five thousand has written nothing, and a percentage of *imported* rows would be
a number that can still become zero.

If invctl restarts mid-import, the job is marked failed and says so. There is
nothing half-written to resume — the transaction went with the process.

One import runs at a time. SQLite takes a single writer, so a second would only
queue behind the first, and while an import runs other saves wait for it.

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

---

# Device types

`/import/device-types` catalogues models. Each carries the manufacturer's end of
support, and every asset pointed at it inherits that date — so one hardware list
loaded here answers "what lapses next year" for the whole estate at once.

The three rules above apply unchanged: it creates and never updates, the whole
file lands or none of it does, and a preview runs the real thing and discards it.

## Identifying a model: manufacturer and model

A model is identified by its manufacturer's **code** and its model name, so
`dell/r650`. Two manufacturers using the same model string do not collide.

**The manufacturer must already exist.** Add it in the catalogue first. A row
naming an unknown code is refused rather than quietly creating one — a
manufacturer invented from a bare code would have no name, and the catalogue
would fill with entries nobody chose.

## Columns

| Column | Required | Meaning |
|---|---|---|
| `manufacturer` | yes | The maker's **code**, as catalogued: `dell`, `hpe`. Case is ignored. |
| `model` | yes | `R650` |
| `part_number` | | What procurement and support portals call it. Searchable as an exact identifier. |
| `u_height` | | Rack units, a whole number. Leave empty for anything that does not mount. |
| `full_depth` | | `true`/`false`, `yes`/`no` or `1`/`0`. Empty means no. |
| `eol_date` | | The manufacturer's end of support, `YYYY-MM-DD`. |
| `notes` | | |
| `lifecycle` | | `planned`, `active`, `deprecated`, `retired`. Defaults to `active`. |

A `full_depth` value that is none of the accepted spellings is **refused**, not
read as "no". A full-depth chassis recorded as half-depth is wrong in a way
nobody notices until a rack diagram is built on it. The same goes for a rack
height that is not a number: refused, never quietly dropped.

## An example

```csv
manufacturer,model,part_number,u_height,full_depth,eol_date
dell,R650,P30721-B21,1,yes,2029-03-31
dell,R750,P30722-B21,2,yes,2030-06-30
hpe,DL380 Gen10,868703-B21,2,true,2028-12-31
```

## What it changes

An asset gains its model's end-of-support date the moment you point it at the
model — and every screen showing that date says it came from the model rather
than from this box, because those are different claims. An asset with a support
contract of its own overrides it.

---

## Not yet importable

Services, dependencies, interfaces, addresses and certificates.

Custom attributes (`attrs`) are deliberately excluded: a file that could write
into them would be a file that could put anything anywhere, and a value worth
filtering or reporting on should be a real column rather than an attribute.
