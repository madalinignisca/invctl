# WP-G4c Column Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a reader hide columns they do not care about on the four wide list tables, remembered per browser.

**Architecture:** Entirely client-side. Each configurable `<th>` and `<td>` carries a stable `data-col` attribute; an Alpine component registered in `app.js` keeps a set of hidden column keys in `localStorage` and toggles a class on the wrapping table; one CSS rule hides `[data-col]` cells whose key is hidden. No handler, store, schema or route changes.

**Tech Stack:** Alpine.js 3.x (**CSP build**), `localStorage`, plain CSS. No new dependencies.

**Spec:** `docs/table-configs-design.md`

## Global Constraints

- **Alpine is the CSP build.** `script-src 'self'`, no `unsafe-eval`. `x-data` takes a **registered component name**, never an inline object or expression. Event handlers are bare method names — `x-on:change="toggleColumn"` — and the method receives `event`, reading state from `event.target`. Any expression in an attribute will silently fail. See `web/static/app.js`'s header comment.
- **All JavaScript lives in `web/static/app.js`**, registered under `alpine:init`. No new script file, no new `<script>` tag in `base.html`, no inline script beyond `x-data`.
- **Store the HIDDEN column keys, never the visible ones** (spec §3). A new column must appear by default for existing users.
- **CSV export is untouched** (spec §4). `?format=csv` keeps its full fixed header.
- **The identity column of each table is not configurable** and carries no `data-col` attribute — see Task 1.
- Every source file opens with the AGPL-3.0-only notice; `internal/license` fails otherwise. Do not add one to `app.js` — it already has one.
- Gate with `make lint` and `make test`, foreground, one at a time, exit status read directly. Never pipe them through `tail`/`head`/`grep`.

---

### Task 1: Mark the configurable columns

**Files:**
- Modify: `web/templates/partials/asset_table.html`
- Modify: `web/templates/partials/rows.html` (the `service_table` define, from line ~120)
- Modify: `web/templates/pages/circuit_list.html` (the **first** table only — the circuits table, not the providers table below it)
- Modify: `web/templates/pages/prefix_list.html` (the **first** table only — the prefixes table, not the ranges table below it)

**Interfaces:**
- Produces: a `data-col="<key>"` attribute on every configurable `<th>` and its matching `<td>`, and a `data-table="<table key>"` attribute on each `<table>`. Tasks 2-4 consume both.

The four tables and their column keys. **The first column of each is the identity column and gets NO `data-col`** — hiding it leaves a row nobody can identify, with no way back except finding the Columns menu again:

| Table | `data-table` | Identity column (not configurable) | Configurable keys |
|---|---|---|---|
| assets | `asset` | Name | `kind`, `contained_by`, `environments`, `serial`, `lifecycle` |
| services | `service` | Code | `name`, `kind`, `environment`, `project`, `availability`, `instances`, `tier` |
| circuits | `circuit` | Circuit ID | `provider`, `service`, `commit`, `contract_ends`, `ends_recorded` |
| prefixes | `prefix` | CIDR | `vlan`, `environment`, `role`, `addresses`, `allocated`, `next_free` |

The bulk-tag checkbox column that appears when `$bulkTag` is set gets NO `data-col` either — it is already conditional and hiding it would strand a selection UI.

- [ ] **Step 1: Add the attributes to the asset table**

In `web/templates/partials/asset_table.html`, the header row becomes:

```html
<tr>
  {{if $bulkTag}}<th></th>{{end}}
  <th>Name</th>
  <th data-col="kind">Kind</th>
  <th data-col="contained_by">Contained by</th>
  <th data-col="environments">Environments</th>
  <th data-col="serial">Serial</th>
  <th data-col="lifecycle">Lifecycle</th>
</tr>
```

and each body `<td>` gains the same `data-col` as the `<th>` above it, in the same order. The `<table>` element gains `data-table="asset"`.

- [ ] **Step 2: Do the same for the other three**

Same treatment, using the keys in the table above. In `rows.html` edit only inside `{{define "service_table"}}`. In `circuit_list.html` and `prefix_list.html` edit only the first `<table>`; leave the second one alone entirely.

- [ ] **Step 3: Verify nothing rendered differently**

```bash
make test
```

Expected: PASS. These are additive attributes; no existing assertion should notice. If one fails, a test is matching on exact markup — read it before changing anything.

- [ ] **Step 4: Commit**

```bash
git add web/templates
git commit -m "Mark the configurable columns on the four wide list tables"
```

---

### Task 2: The Alpine component

**Files:**
- Modify: `web/static/app.js`
- Modify: `web/src/app.css` (**the source**; `web/static/app.css` is Tailwind output that `make build` regenerates — editing it is lost on the next build)

**Interfaces:**
- Consumes: `data-table` and `data-col` from Task 1.
- Produces: an Alpine component named `columnPicker`, and the CSS class `col-hidden`. Task 3 consumes both.

- [ ] **Step 1: Add the component to `app.js`**

Inside the existing `document.addEventListener('alpine:init', ...)` block, alongside the other `Alpine.data(...)` registrations:

```js
  // Column visibility for a wide list table, remembered per browser.
  //
  // STORES THE HIDDEN KEYS, NOT THE VISIBLE ONES, and that is the whole
  // design (docs/table-configs-design.md §3). If it stored what to show, a
  // release that adds a column would hide it from everyone who had ever
  // touched this menu -- they would never see the new field and would have no
  // reason to suspect one existed. Storing what to hide makes a new column
  // visible by default and makes a stale key naming a removed column inert.
  //
  // The table key comes from x-data's argument, so one component serves all
  // four tables without knowing anything about them.
  Alpine.data('columnPicker', (table = '') => ({
    table: table,
    hidden: [],
    open: false,

    init() {
      this.hidden = this.read();
      this.apply();
    },

    // localStorage throws in some privacy modes rather than returning null,
    // so every access is guarded. A browser that refuses to store simply
    // shows every column, which is the correct degraded state.
    read() {
      try {
        const raw = window.localStorage.getItem('invctl.cols.' + this.table);
        const parsed = raw ? JSON.parse(raw) : [];
        return Array.isArray(parsed) ? parsed.filter((k) => typeof k === 'string') : [];
      } catch (e) {
        return [];
      }
    },

    save() {
      try {
        window.localStorage.setItem('invctl.cols.' + this.table, JSON.stringify(this.hidden));
      } catch (e) {
        // Nothing to do: the preference is a convenience, not state anything
        // depends on.
      }
    },

    apply() {
      const root = this.$root;
      root.querySelectorAll('[data-col]').forEach((cell) => {
        cell.classList.toggle('col-hidden', this.hidden.indexOf(cell.dataset.col) !== -1);
      });
    },

    toggleMenu() {
      this.open = !this.open;
    },

    // x-on hands the event to the method; the key rides on the checkbox as
    // data-col, because the CSP build cannot pass an argument from an
    // attribute expression.
    toggleColumn(event) {
      const key = event.target.dataset.col;
      if (!key) {
        return;
      }
      const at = this.hidden.indexOf(key);
      if (at === -1) {
        this.hidden.push(key);
      } else {
        this.hidden.splice(at, 1);
      }
      this.save();
      this.apply();
    },

    isVisible(key) {
      return this.hidden.indexOf(key) === -1;
    },

    showAll() {
      this.hidden = [];
      this.save();
      this.apply();
    },
  }));
```

- [ ] **Step 2: Add the CSS rule**

Append to `web/src/app.css` — **the source file**. `web/static/app.css` is generated by the Tailwind CLI during `make build` and anything written there is overwritten:

```css
/* Column visibility (WP-G4c). The class is toggled by the columnPicker
   Alpine component; the server always renders every column it is willing to
   show this reader, and hiding is purely local. */
.col-hidden {
  display: none;
}
```

- [ ] **Step 3: Verify the build is unaffected**

```bash
make build
```

Expected: success. `app.js` and `app.css` are served from `web/static` and embedded; nothing compiles them.

- [ ] **Step 4: Commit**

```bash
git add web/static/app.js web/src/app.css
git commit -m "Add the columnPicker Alpine component and its hiding rule"
```

---

### Task 3: The picker partial

**Files:**
- Create: `web/templates/partials/column_picker.html`

**Interfaces:**
- Consumes: the `columnPicker` component and `col-hidden` class from Task 2.
- Produces: a template named `column_picker` taking a `dict` with `Table` (string, the `data-table` key) and `Columns` (a slice of `dict` with `Key` and `Label`). Task 4 calls it.

- [ ] **Step 1: Write the partial**

```html
<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->
{{define "column_picker"}}
{{/* Renderable standalone: it needs only .Table and .Columns, and the
     component it drives finds its cells through $root, so this partial
     never needs to know what the table contains. */}}
{{/* No x-data here. The component is declared on a wrapper that contains
     BOTH this picker and the table, so $root reaches the cells -- see the
     plan's Task 4. A second x-data here would create a second, empty
     component instance and the picker would toggle nothing. */}}
<div class="column-picker" data-columns="{{.Table}}">
  <button type="button" class="btn btn-sm" x-on:click="toggleMenu">Columns</button>
  <div class="column-picker-menu" x-show="open" x-cloak>
    {{range .Columns}}
    <label>
      <input type="checkbox" data-col="{{.Key}}" x-on:change="toggleColumn" checked>
      {{.Label}}
    </label>
    {{end}}
    <button type="button" class="btn btn-sm" x-on:click="showAll">Show all</button>
  </div>
</div>
{{end}}
```

**Note on the `checked` attribute:** every box renders checked and the
component corrects them on `init` by calling `apply`. Rendering the real
state server-side is impossible — the server does not know what this browser
has hidden — so the checkboxes must be reconciled on the client.

- [ ] **Step 2: Reconcile the checkboxes in `init`**

In `app.js`, extend `apply()` so the boxes match the stored state, not just the cells:

```js
    apply() {
      const root = this.$root;
      root.querySelectorAll('[data-col]').forEach((cell) => {
        cell.classList.toggle('col-hidden', this.hidden.indexOf(cell.dataset.col) !== -1);
      });
      document.querySelectorAll('.column-picker[data-columns="' + this.table +
        '"] input[data-col]').forEach((box) => {
        box.checked = this.hidden.indexOf(box.dataset.col) === -1;
      });
    },
```

**Why the picker's `$root` is not the table's `$root`:** the picker sits
outside the `<table>`, so one `x-data` cannot wrap both. Task 4 puts the
`x-data="columnPicker"` on a wrapper `<div>` that contains **both** the
picker and the table, which makes `$root` cover both and the first
`querySelectorAll` work. The second selector is scoped by `data-columns` so
two pickers on one page cannot fight.

- [ ] **Step 3: Add the menu styling**

Append to `web/src/app.css` — **the source file**. `web/static/app.css` is generated by the Tailwind CLI during `make build` and anything written there is overwritten:

```css
.column-picker { position: relative; display: inline-block; }
.column-picker-menu {
  position: absolute; z-index: 20; right: 0; min-width: 12rem;
  padding: 8px; border: 1px solid var(--line);
  background: var(--raised); border-radius: 6px;
}
.column-picker-menu label { display: block; padding: 2px 0; }

/* [x-cloak] is NOT declared here: web/src/app.css already carries it.
   Adding a second rule is harmless but noise -- check before you write it. */
```

- [ ] **Step 4: Commit**

```bash
git add web/templates/partials/column_picker.html web/static/app.js web/src/app.css
git commit -m "Add the column picker partial"
```

---

### Task 4: Wire the picker to the four tables

**Files:**
- Modify: `web/templates/partials/asset_table.html`
- Modify: `web/templates/partials/rows.html`
- Modify: `web/templates/pages/circuit_list.html`
- Modify: `web/templates/pages/prefix_list.html`

**Interfaces:**
- Consumes: `column_picker` from Task 3, `columnPicker` from Task 2, `data-col`/`data-table` from Task 1.

- [ ] **Step 1: Wrap the asset table and add the picker**

The `x-data` goes on a wrapper containing both the picker and the table, so
`$root` covers the cells:

```html
<div x-data="columnPicker">
  {{template "column_picker" dict
     "Table" "asset"
     "Columns" .ColumnOptions}}
  <table data-table="asset">
    ... existing markup ...
  </table>
</div>
```

**`x-data="columnPicker"` appears once, on the wrapper.** Task 3's partial
deliberately does not declare it; it inherits the component from this parent.

**`list` DOES NOT EXIST** in `internal/web/render/funcs.go` — only `dict`
does, so the column set cannot be built inline in the template. That is why
the blocks above pass `.ColumnOptions` instead.

Two ways to supply it, and take the first:

1. **A method on the page's view model.** Each of the four list handlers
   already builds a page struct; give it a `ColumnOptions() []ColumnOption`
   method returning the key/label pairs, and call
   `{{template "column_picker" dict "Table" "asset" "Columns" .ColumnOptions}}`.
   The labels then live in Go beside the handler that renders the table.
2. If that is awkward for a template that is not backed by a struct you can
   reach, add a `list` function to `funcs.go` — but say so in your report,
   because it is a new template function and this repo keeps that set small.

`ColumnOption` is `struct{ Key, Label string }` in
`internal/web/handlers`. Define it once, in `app.go`, next to `Base`.

**The dot in `rows.html`.** `service_table` lives in a shared partials file,
so `.ColumnOptions` resolves against whatever the caller passed. Checked
while writing this plan: it has exactly **one** call site today,
`service_list.html:72`, so the field will be there. Re-check before you rely
on it — if a second caller has appeared, pass the options explicitly through
`dict` at that call site rather than making the partial guess.

- [ ] **Step 2: Services** — in `rows.html`, inside `{{define "service_table"}}`

```html
<div x-data="columnPicker">
  {{template "column_picker" dict
     "Table" "service"
     "Columns" .ColumnOptions}}
  <table data-table="service">
    ... existing markup, unchanged apart from Task 1's data-col attributes ...
  </table>
</div>
```

- [ ] **Step 3: Circuits** — in `circuit_list.html`, the FIRST table only

```html
<div x-data="columnPicker">
  {{template "column_picker" dict
     "Table" "circuit"
     "Columns" .ColumnOptions}}
  <table data-table="circuit">
    ... existing markup ...
  </table>
</div>
```

Leave the providers table below it completely alone.

- [ ] **Step 4: Prefixes** — in `prefix_list.html`, the FIRST table only

```html
<div x-data="columnPicker">
  {{template "column_picker" dict
     "Table" "prefix"
     "Columns" .ColumnOptions}}
  <table data-table="prefix">
    ... existing markup ...
  </table>
</div>
```

Leave the ranges table below it completely alone.

- [ ] **Step 5: Verify**

```bash
make lint
make test
```

Expected: both PASS.

- [ ] **Step 6: Commit**

```bash
git add web/templates
git commit -m "Offer the column picker on the four wide list tables"
```

---

### Task 5: End-to-end proof

**Files:**
- Create: `tests/e2e/specs/column-config.spec.js`

**Interfaces:**
- Consumes: everything above.

Read `docs/E2E.md` before running anything — the suite has prerequisites that
fail in ways that look nothing like the cause.

- [ ] **Step 1: Write the spec**

```js
const { test, expect } = require('@playwright/test');
const { login } = require('../helpers/login');

// The three claims worth pinning. The third is the one most likely to be
// "fixed" into a bug later: hiding a column must NOT change what a CSV
// export contains, because the export is an importable round-trip artifact
// and not a picture of the screen (docs/table-configs-design.md §4).
test('a hidden column stays hidden across a reload and never leaves the CSV', async ({ page }) => {
  await login(page);
  await page.goto('/assets');

  const serialHeader = page.locator('th[data-col="serial"]');
  await expect(serialHeader).toBeVisible();

  await page.locator('.column-picker button', { hasText: 'Columns' }).first().click();
  await page.locator('.column-picker input[data-col="serial"]').first().uncheck();
  await expect(serialHeader).toBeHidden();

  await page.reload();
  await expect(page.locator('th[data-col="serial"]')).toBeHidden();

  const csv = await page.request.get('/assets?format=csv');
  expect(csv.ok()).toBeTruthy();
  expect((await csv.text()).toLowerCase()).toContain('serial');
});
```

- [ ] **Step 2: Run it and watch it pass**

Follow `docs/E2E.md` for bringing an instance up, then run this spec alone.
**Watch it pass; do not infer it from a green summary line.**

- [ ] **Step 3: Prove it can fail**

Delete the `.col-hidden { display: none; }` rule from `app.css`, rebuild, and
re-run. Expected: the spec fails at the first `toBeHidden`. Restore the rule
and confirm green again.

- [ ] **Step 4: Commit**

```bash
git add tests/e2e/specs/column-config.spec.js
git commit -m "E2E: a hidden column survives a reload and stays in the CSV"
```

---

### Task 6: Documentation

**Files:**
- Modify: `docs/ROADMAP.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Mark G4c done in the roadmap**

In `docs/ROADMAP.md`, the WP-G4 entry's G4c bullet becomes **DONE**, noting
it shipped without a table and pointing at `docs/table-configs-design.md`.
Remove G4c from the "Deferred to 1.1" list in "The 1.0 line" section.

- [ ] **Step 2: Add the CHANGELOG entry**

Under `## [Unreleased]`, in an `### Added` section:

```markdown
- **Hide columns you do not use** on the asset, service, circuit and prefix
  lists. The choice is remembered in your browser, per table. It does not
  follow you to another device, and clearing site data resets it — this is a
  display preference, not account state, and invctl deliberately stores
  nothing about it.

  A CSV export still contains every column, whatever the screen shows: the
  export is an importable table, not a picture of the page.
```

- [ ] **Step 3: Commit**

```bash
git add docs/ROADMAP.md CHANGELOG.md
git commit -m "Record WP-G4c as shipped"
```
