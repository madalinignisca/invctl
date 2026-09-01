// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WRITES, ON PURPOSE, AND MORE STRICTLY GATED THAN ANY OTHER SPEC HERE.
//
// Saving a view is the whole point of WP-G4b -- there is no way to prove it
// without a real POST to /views that leaves a row behind. docs/E2E.md's
// "This suite never writes" section already had two exceptions before this
// file (user-administration.spec.js and the two rbac-project-owner specs),
// each guarded by refusing to run against anything that LOOKS LIKE the
// shared public demo (a hostname match on invctl.madalin.me). This spec
// uses a STRICTER, POSITIVE opt-in instead of that negative denylist,
// because what it creates is not a throwaway account a later step in the
// same run demotes back -- a saved view is a soft-delete-only row
// (CLAUDE.md: "Soft delete only, for entities... Never delete"), so even
// this spec's own cleanup (the "remove" step below) leaves a retired row
// permanently on whatever instance it ran against. A hostname denylist only
// ever knows about hosts someone thought to list; this requires the
// operator to affirmatively say "this target is disposable" instead.
//
//   1. Without INV_E2E_DISPOSABLE=true, this spec SKIPS -- the one
//      legitimate runtime skip shape this suite recognises (CLAUDE.md: "a
//      runtime skip is legitimate only on an explicitly declared
//      precondition: an env-var opt-in"), exactly like INV_E2E_BASE_URL
//      itself. The skipped describe block's own title states the reason,
//      the same way the Makefile's `make e2e` prints one for the unset
//      BASE_URL case.
//   2. If INV_E2E_DISPOSABLE=true IS set but INV_E2E_BASE_URL does not
//      resolve to localhost or 127.0.0.1, this spec FAILS LOUDLY instead of
//      skipping. That combination is somebody actively asserting a
//      non-local host is disposable, and nothing in this file is in a
//      position to know that is true -- silently skipping would hide a
//      configuration mistake that is one flag away from writing a
//      permanent artefact onto a shared instance; silently running would be
//      worse.
//
// IF YOU ARE READING THIS BECAUSE THE SUITE FAILED: that is almost
// certainly case 2 above, not a real bug -- see docs/E2E.md's own warning
// that this suite's failures "look nothing like their cause". Point
// INV_E2E_BASE_URL at a disposable local instance you stood up yourself
// (INV_SEED=true INV_SEED_COMPANY=true; see docs/E2E.md's "demo estate"
// section for what that seeds and why this spec needs it) -- NEVER at the
// real deployment on this machine's :8088 (invctl-demo.service) and NEVER
// at https://invctl.madalin.me.
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const disposableOptIn = process.env.INV_E2E_DISPOSABLE === 'true';

// Resolves to true only for a target this file is willing to write to.
// Wrapped in try/catch because an unset or malformed BASE_URL must read as
// "not local", never throw here -- the loud failure below is a deliberate
// test.beforeAll throw with a clear message, not an unhandled exception from
// a bad URL parse.
function pointsAtLocalhost() {
  if (!BASE_URL) {
    return false;
  }
  try {
    const host = new URL(BASE_URL).hostname;
    return host === 'localhost' || host === '127.0.0.1';
  } catch {
    return false;
  }
}

const localTarget = pointsAtLocalhost();

// Case 1 above: no opt-in, skip -- and say so in the title itself, since a
// skipped describe block never runs a beforeAll that could print anything.
const describeTitle = disposableOptIn
  ? 'saved views (mutates -- disposable local instance only)'
  : 'saved views (mutates -- disposable local instance only) ' +
    '[skipped: set INV_E2E_DISPOSABLE=true to run this spec]';

const describe = disposableOptIn ? test.describe : test.describe.skip;

describe(describeTitle, () => {
  test.beforeAll(() => {
    // Case 2 above: the opt-in is set but the target does not look local.
    // Unreachable while the whole block is skipped (disposableOptIn false);
    // kept as the actual enforcement when it is NOT skipped, which is the
    // state where a mistake here would matter.
    if (!localTarget) {
      throw new Error(
        `INV_E2E_DISPOSABLE=true requires INV_E2E_BASE_URL to resolve to ` +
          `localhost or 127.0.0.1 (got ${BASE_URL ?? '<unset>'}). This spec ` +
          'saves a permanent saved_view row and this project never assumes a ' +
          'non-local host is disposable -- see this file\'s own header and ' +
          "docs/E2E.md's \"This suite never writes\" section.",
      );
    }
  });

  test('saving, reopening and removing a view genuinely applies and later clears the filter', async ({
    page,
  }) => {
    // A name unique per run: this instance is disposable but the spec may
    // still be run against it more than once before it is thrown away, and
    // a second run must not collide with the first run's still-present row.
    const viewName = `e2e-firewalls-${Date.now()}`;

    // --- Baseline: unfiltered /assets shows both a hypervisor and a
    // firewall (docs/E2E.md's named fixtures: hv-01, fw-dev-1). ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toBeVisible();
    await expect(page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ })).toBeVisible();

    // --- Apply a filter: kind=firewall. web/templates/pages/asset_list.html's
    // toolbar swaps #asset-table via hx-get on 'change', which a real
    // <select> firing selectOption satisfies without needing the 'q' box's
    // keyup debounce. ---
    await Promise.all([
      page.waitForResponse(
        (r) => r.request().method() === 'GET' && r.url().includes('/assets?') && r.url().includes('kind=firewall'),
      ),
      page.locator('#f-kind').selectOption('firewall'),
    ]);
    await expect(page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ })).toBeVisible();
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toHaveCount(0);

    // --- Open the Views menu AFTER filtering, not before: the whole
    // #asset-table fragment (menu included) is swapped on every filter
    // change, so an open menu closes the moment the filter above ran --
    // see web/templates/partials/asset_table.html's own comment on exactly
    // this. ---
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await expect(page.locator('.saved-views-menu')).toBeVisible();

    // --- Save the currently-applied filter under a name. The form carries
    // hx-post; on success the handler answers HX-Redirect (render.Redirect,
    // internal/web/handlers/savedviews.go), which htmx turns into a real
    // browser navigation to the SAME URL the address bar already shows
    // (the filter's own hx-push-url already put ?kind=firewall there before
    // this save even ran). Two things this rules out, in order:
    //   - waitForURL: resolves the instant its pattern matches the CURRENT
    //     url, which is already true before the click ever fires here, so it
    //     proves nothing.
    //   - waitForResponse on the document GET: the response can arrive before
    //     the navigation it belongs to actually COMMITS in the frame, so a
    //     goto() issued right after still raced a same-URL navigation that
    //     was, from Playwright's point of view, still in flight
    //     (net::ERR_ABORTED / "interrupted by another navigation to the same
    //     URL" on the very next page.goto -- observed directly while writing
    //     this spec).
    // waitForEvent('framenavigated') is the one signal that only fires once
    // the frame has actually navigated -- an event, not a poll of current
    // state, so it cannot resolve on a state that was already true before
    // the click. ---
    await page.locator('.saved-views-menu input[name="name"]').fill(viewName);
    await Promise.all([
      page.waitForEvent('framenavigated', (frame) => frame === page.mainFrame() && frame.url().includes('kind=firewall')),
      page.locator('.saved-view-save button[type="submit"]').click(),
    ]);
    await page.waitForLoadState('networkidle');

    // --- "Reload": deliberately NOT page.reload(), which would refresh a
    // URL that already carries ?kind=firewall from the redirect above and
    // prove nothing about the saved view itself -- the filter would still
    // be "applied" purely because it is sitting in the address bar. Instead,
    // navigate back to a genuinely clean, unfiltered /assets, the same way a
    // person closing this tab and opening a fresh one would, so the saved
    // view is the ONLY thing that can reapply the filter afterward. ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    expect(page.url()).not.toContain('kind=');
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toBeVisible();

    // --- Open Views again and follow the saved view. The link is a plain
    // <a href>, a full navigation, not an hx-get -- see this file's own
    // header and saved_views.html's comment on why: it is what lets
    // waitForURL below observe the navigation directly rather than an XHR. ---
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    const savedRow = page.locator('.saved-view-row', { hasText: viewName });
    await expect(savedRow).toBeVisible();
    const href = await savedRow.locator('a').getAttribute('href');
    expect(href, 'the saved view\'s own link should carry the filter it stored').toBe('/assets?kind=firewall');

    await Promise.all([page.waitForURL(/\/assets\?kind=firewall/), savedRow.locator('a').click()]);

    // The claim this whole spec exists to prove: genuinely applied, checked
    // BOTH ways -- the URL a person could paste into a ticket, and the
    // actual rendered rows, not merely one or the other.
    expect(page.url()).toContain('kind=firewall');
    await expect(page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ })).toBeVisible();
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toHaveCount(0);

    // --- Remove it. The "Remove" form has no hx-post (saved_views.html) --
    // a plain browser POST, so this is a real navigation too. RetireSavedView
    // redirects to the entity's base list ("/assets"), unfiltered, regardless
    // of what the retired view's own filter was. ---
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    const rowToRemove = page.locator('.saved-view-row', { hasText: viewName });
    await Promise.all([
      page.waitForURL((url) => url.pathname === '/assets' && url.search === ''),
      rowToRemove.locator('button', { hasText: 'Remove' }).click(),
    ]);

    // Soft-deleted, not hard-deleted (CLAUDE.md) -- the assertion here is
    // only that the MENU no longer offers it, which is all a person-facing
    // "remove" flow promises; the row itself persists with lifecycle =
    // 'retired' in the database, unchecked by this browser-level spec.
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await expect(page.locator('.saved-view-row', { hasText: viewName })).toHaveCount(0);
  });
});
