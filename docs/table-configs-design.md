<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# WP-G4c — table column configuration

The last third of WP-G4. Tags (G4a) shipped; saved filters (G4b) still need
their own design. This is the piece that lets somebody who lives in the asset
list stop looking at four columns they do not care about.

`docs/tags-design.md` §0 deferred this until WP-G1 on the grounds that
per-user state is close to meaningless while authorization is a
comma-separated list of usernames, and that anything whose SUBJECT is a
person needs a scrub operation to exist first. Both conditions are now met:
WP-G1 shipped and `ScrubUser` is implemented and routed.

**And then the feature turned out not to need either of them.**

## 1. It does not need a table

The deferral assumed a `user_table_config` row keyed by `app_user.id` — the
first table in this product whose subject is a person rather than its author.
That would have brought a retention position in `docs/AUDIT.md`, an extension
to `ScrubUser`, an entry in the column classification, a scope class, and a
migration landing immediately after 1.0 promised schema stability.

A column preference does not earn any of that. It is a display preference
about one browser's rendering of one page. It is not a fact about the estate,
nobody else needs to read it, no report aggregates it, and losing it costs a
user fifteen seconds.

**So it lives in `localStorage` and the database never learns it exists.**

What that buys:

- No migration, no `user_id` column anywhere, no change to `ScrubUser`.
- Nothing new to answer in an erasure request. The strongest form of GDPR
  compliance for a piece of data is not collecting it.
- No entry in `domain.entityScope`, no `change_log` obligation, no
  interaction with WP-G1 at all.

What it costs, stated so nobody discovers it as a bug:

- **Preferences do not follow a person between devices.** Their laptop and
  their phone disagree, permanently.
- **Clearing site data resets them.**

Both are acceptable for a display preference and would not be for anything
else. If a later requirement genuinely needs cross-device persistence, that
is a new decision with the table's full cost attached, and this document is
the record of why it was not paid the first time.

## 2. Scope

**Four lists**, the ones wide enough that hiding a column is worth a control:
assets, services, circuits, prefixes.

The tables themselves are not all in the page templates — `asset_list.html`
and `service_list.html` delegate to the `asset_table` and `service_table`
partials, while `circuit_list.html` (13 columns) and `prefix_list.html` (15)
carry their own markup. The implementation touches whichever file holds the
`<th>`, and the picker goes on the page.

**Visibility only. No reordering.** Reordering needs a drag interaction, a
persisted order array, and an answer for what a stored order means after a
release adds a column. It is where this class of feature stops paying for
itself.

Not the other thirteen list pages. Most are narrow enough that nobody would
open the control, and every one added is surface to verify for a feature
whose whole justification is that it is cheap.

## 3. Store the hidden columns, not the visible ones

This is the one decision in the design that is not obvious, and getting it
backwards produces a bug that is invisible in testing and permanent in
production.

If the stored value is the list of **visible** columns, then a release that
adds a column hides it from every existing user — their stored list does not
name it, so it does not render. They never see the new field, nobody reports
it as missing, and the only symptom is that a feature you shipped appears not
to exist for exactly the people who have used the product longest.

If the stored value is the list of **hidden** columns:

- A new column is visible by default, because nothing hides it.
- A stored entry naming a column that no longer exists is inert.
- No migration of browser state is ever needed.

The stale-preference problem does not get solved; it stops existing.

## 4. CSV export is untouched

`ExportAssets` and its siblings render "an asset list as an importable
table" — the CSV is a round-trip artifact for the bulk importer, not a
picture of the screen. Its header is fixed.

**Hiding a column must not change what an export contains.** An export that
followed the view would emit files this product's own importer cannot read,
and it would do so silently, at the moment somebody is trying to move real
data.

A user who hides a column and then exports gets it anyway. That is correct
and worth a line in the UI rather than a surprise.

## 5. There is no disclosure surface

Every column these tables render is already one the server decided this user
may see. Cost columns are gated by `Authorizer.CanSeeCosts` server-side and
never reach the browser for somebody without the grant; `identity.secret_ref`
is redacted in the handler's view model.

So hiding is cosmetic by construction, and doing it client-side changes
nothing about what is sent. **This is the argument that makes a client-side
implementation acceptable, and it holds only because the server-side gating
is already correct.** A future column carrying data some readers may not see
must be gated server-side like the others — never by this mechanism.

## 6. Shape

- `web/static/columns.js` — one Alpine component, registered the way
  `app.js` already registers behaviour. No inline script beyond `x-data`.
- `web/templates/partials/column_picker.html` — the dropdown, one checkbox
  per column, renderable standalone.
- The four table templates gain a stable per-column class on each `<th>` and
  `<td>`, and include the picker.
- One CSS rule in `app.css` does the hiding.

No handler changes. No store changes. No new dependency.

## 7. Testing

Browser state is not reachable from a Go test, so the honest check is an E2E
spec: hide a column, reload the page, assert it is still hidden; then request
the CSV and assert the hidden column is still in it.

That second assertion is the one worth having. §4 is a claim about behaviour
that a reasonable person might implement the other way, and it is exactly the
kind of thing that gets "fixed" later by somebody who thinks it is a bug.

The existing suites should need no changes. If one does, that is a signal
this touched something it should not have.

## 8. Explicitly not in scope

- Reordering columns.
- The other thirteen list pages.
- Cross-device or cross-browser persistence.
- **WP-G4b saved filters.** A saved filter is a different feature with a
  different storage question — it names estate content, is plausibly worth
  sharing, and per `docs/tags-design.md` §0 needs its own design. Nothing
  here should be built as a foundation for it.
- Server-side defaults, per-role or per-estate. If "the view network ops
  uses" is ever wanted, it is a separate feature and probably a better one.

## 9. Size

**S.** Four templates, one JS file, one partial, one CSS rule, one E2E spec.

The deferral in `docs/tags-design.md` §0 sized this against a schema change
that this design removes. That is the substance of the work: not building the
table, and being able to say why.
