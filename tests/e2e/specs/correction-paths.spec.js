// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WRITES, ON PURPOSE, AND GATED THE SAME STRICT WAY saved-views.spec.js IS.
//
// A correction is a permanent audited change: it writes a change_log row that
// is append-only and can never be removed (CLAUDE.md), and it changes a figure
// on whatever instance it ran against. So this uses the POSITIVE opt-in,
// INV_E2E_DISPOSABLE=true, and refuses a non-local target loudly rather than
// skipping -- the reasoning is saved-views.spec.js's, in full, and it applies
// here for the same reason: a hostname denylist only knows the hosts somebody
// remembered to list.
//
// WHAT THIS ASSERTS THAT NO GO TEST CAN, and the reason the file exists at all.
//
// The six correction paths (power feed, supply and input; reservation; circuit;
// dependency) all render as ROW EDITORS, and a row editor puts its inputs in
// the table cells they belong under rather than inside the <form> element:
//
//     <td><input form="feed-abc" name="amperage" ...></td>
//     ...
//     <td><form id="feed-abc" method="post" action="/power/feeds/abc"> ... </td>
//
// That is HTML5 form association, and it is the browser that performs it. Every
// Go test for these routes posts a hand-built payload straight at the handler,
// so if `form="feed-abc"` were misspelled, pointed at another row's id, or
// silently ignored, the submission would carry only the hidden inputs
// physically nested inside the <form> -- and EVERY ONE OF THOSE GO TESTS WOULD
// STILL PASS. The save would appear to work and quietly blank the fields it did
// not carry, because these Update methods write every column.
//
// This project has shipped exactly that class of defect before: a 404 on a
// button with four green checks, because handler tests inject router params by
// hand and never ask the router whether anything can reach it (docs/E2E.md,
// CLAUDE.md's "Green CI is not evidence"). Form association is the same shape
// of gap one layer further in -- the route is reachable, the handler is right,
// and the markup does not deliver the fields.
//
// So each test here types into a cell OUTSIDE the form element, clicks Save,
// and asserts the typed value came back rendered. Nothing here re-checks the
// store; that is what internal/web/*_update_test.go is for.
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const disposableOptIn = process.env.INV_E2E_DISPOSABLE === 'true';

// True only for a target this file is willing to write to. Wrapped for
// saved-views.spec.js's reason: an unset or malformed BASE_URL must read as
// "not local" rather than throw out of module scope.
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
const describeTitle = disposableOptIn
  ? 'correction paths (mutates -- disposable local instance only)'
  : 'correction paths (mutates -- disposable local instance only) ' +
    '[skipped: set INV_E2E_DISPOSABLE=true to run this spec]';
const describe = disposableOptIn ? test.describe : test.describe.skip;

describe(describeTitle, () => {
  test.beforeAll(() => {
    if (!localTarget) {
      throw new Error(
        `INV_E2E_DISPOSABLE=true requires INV_E2E_BASE_URL to resolve to ` +
          `localhost or 127.0.0.1 (got ${BASE_URL ?? '<unset>'}). This spec ` +
          'writes permanent, append-only change_log rows and corrects real ' +
          "figures -- see this file's header and docs/E2E.md.",
      );
    }
  });

  // A feed's rating, corrected through the row editor.
  //
  // The amperage input lives in the Rating cell; the <form> lives in the
  // actions cell at the far end of the row. Nothing but the browser's form
  // association connects them.
  test('a power feed rating typed outside the form element is actually submitted', async ({
    page,
  }) => {
    await page.goto('/power', { waitUntil: 'networkidle' });

    // /power renders three tables -- Feeds, Supplies, Panels -- so every
    // locator here names the Feeds one by its own "Allocated" header rather
    // than taking the first table on the page. Two of the six correction
    // paths live on this page and a bare `table` matches both plus a third.
    const feeds = page.locator('table').filter({ has: page.locator('th:text-is("Allocated")') });
    await expect(feeds, 'no Feeds table on /power').toHaveCount(1);

    // Reached by CLICKING Edit, not by fetching ?edit=<id>: whether that link
    // resolves to a row that opens is itself part of the claim.
    const row = feeds.locator('tbody tr').filter({ has: page.locator('a:text-is("Edit")') }).first();
    await expect(row, 'no editable row on /power -- the seed has no feeds, or Edit did not render').toBeVisible();
    await row.locator('a:text-is("Edit")').click();

    const editing = feeds.locator('tr.row-editing').first();
    await expect(editing, 'clicking Edit did not open a row editor').toBeVisible();

    const amperage = editing.locator('input[name="amperage"]');
    await expect(amperage, 'the open row has no amperage input').toBeVisible();

    // The association claim, checked before relying on it: the input must
    // name the form it belongs to, and that form must exist on the page.
    const formID = await amperage.getAttribute('form');
    expect(formID, 'the amperage input carries no form= attribute, so a browser ' +
      'will not submit it with the row and the save would blank the rating').toBeTruthy();
    await expect(page.locator(`form#${formID}`),
      `the amperage input points at form#${formID}, which is not on this page -- ` +
      'the browser submits nothing for it and the rating is written as empty').toHaveCount(1);

    // A value nothing else on the page would show, so finding it afterwards
    // cannot be a coincidence.
    const corrected = String(17 + (Date.now() % 40));
    await amperage.fill(corrected);
    await editing.locator('button:text-is("Save")').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('table').filter({ has: page.locator('th:text-is("Allocated")') }),
      `the corrected amperage ${corrected} A is not on /power after saving. The ` +
      'route and handler are covered by Go tests, so this is the markup: the ' +
      'input was not submitted with its form.').toContainText(`${corrected} A`);
  });

  // The dependency row editor, and the case a browser decides: a checkbox
  // group with every box unticked submits NO key at all.
  //
  // Go tests can post an empty form value list, but only a real browser proves
  // that unticking in the DOM produces that absence -- and the handler has to
  // read the absence as "clear the set" rather than "leave it alone", which is
  // a distinction that cost a silent bug when this was written.
  test('unticking every data class on a dependency actually clears them', async ({ page }) => {
    await page.goto('/services', { waitUntil: 'networkidle' });
    const service = page.locator('table tbody tr a.id').first();
    await expect(service, 'no services in the seed').toBeVisible();
    await service.click();
    await page.waitForLoadState('networkidle');

    // A DEPENDENCY row's Edit, not the service's own: the service detail page
    // renders "Correct this service" and an endpoint editor too, and the first
    // Edit on the page is none of them reliably. Dependency rows carry
    // id="dep-<id>" (partials/rows.html), which is the only stable way to say
    // "an edge, in either panel".
    //
    // NO RUNTIME SKIP HERE, DELIBERATELY. An earlier version of this read
    // `if (count === 0) test.skip(...)`, which is the anti-pattern CLAUDE.md
    // names outright: a regression that removed the Edit control from every
    // dependency row -- PRECISELY the class this spec exists to catch -- would
    // have made the precondition false and turned the failure into a silent
    // pass, for ever. The absence of an editable dependency row is a failure
    // of this suite's fixture expectations (docs/E2E.md, "the demo estate"),
    // and it is stated as one.
    const depEdit = page.locator('tr[id^="dep-"] a:text-is("Edit")').first();
    await expect(depEdit,
      'no dependency row on this service offers Edit. Either the seed has no ' +
      'dependency here (a fixture problem -- see docs/E2E.md) or the Edit ' +
      'control has been lost from the row, which is the regression this spec ' +
      'exists to catch. Both are failures; neither is a reason to skip.').toBeVisible();
    await depEdit.click();
    await page.waitForLoadState('networkidle');

    const editing = page.locator('tr[id^="dep-"].row-editing').first();
    await expect(editing, 'clicking Edit on a dependency did not open a row editor').toBeVisible();

    const boxes = editing.locator('input[name="data_class"]');
    const count = await boxes.count();
    expect(count, 'the dependency editor renders no data-class checkboxes').toBeGreaterThan(0);

    // Ensure at least one class is ticked, WITHOUT assuming the starting
    // state: the seed already puts classes on some edges, so "tick one and
    // expect exactly one" was wrong the first time this ran. check() is
    // idempotent on an already-ticked box.
    await boxes.first().check();
    await editing.locator('button:text-is("Save")').click();
    await page.waitForLoadState('networkidle');

    await page.locator('tr[id^="dep-"] a:text-is("Edit")').first().click();
    await page.waitForLoadState('networkidle');
    const reopened = page.locator('tr[id^="dep-"].row-editing').first();
    const ticked = reopened.locator('input[name="data_class"]:checked');
    const tickedBefore = await ticked.count();
    expect(tickedBefore, 'nothing is ticked after saving a ticked class, so either ' +
      'the save did not take or the editor does not render what is stored -- and ' +
      'the unticking claim below would then pass over an already-empty set').toBeGreaterThan(0);

    // Untick every one of them. This is the case only a browser decides: the
    // group then submits NO key at all, and the handler has to read that
    // absence as "replace the set with nothing".
    for (let i = tickedBefore - 1; i >= 0; i -= 1) {
      await ticked.nth(i).uncheck();
    }
    await reopened.locator('button:text-is("Save")').click();
    await page.waitForLoadState('networkidle');

    await page.locator('tr[id^="dep-"] a:text-is("Edit")').first().click();
    await page.waitForLoadState('networkidle');
    await expect(page.locator('tr[id^="dep-"].row-editing input[name="data_class"]:checked'),
      `${tickedBefore} data class(es) were unticked and saved, and something is ` +
      'still ticked. An ' +
      'unticked checkbox group submits no key, so the handler must read that ' +
      'absence as "replace the set with nothing" -- reading it as "leave them ' +
      'alone" makes removing the last class impossible, silently.').toHaveCount(0);
  });
});
