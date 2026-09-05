// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-B4 Task 10: panel breakout on /interfaces/{id}/trace
// (docs/panel-breakout-design.md, internal/store/cabling.go's tree walk,
// web/templates/pages/trace.html). Go tests already assert the tree's exact
// structure and every string it can render --
// internal/store/cabling_breakout_test.go,
// internal/seed/fit_test.go's TestTheDemoEstateHasAPanelBreakoutToTrace,
// internal/web/power_test.go's TestTracingARunThroughTwoPanelsOnScreen -- so
// nothing here repeats a substring check a Go test already makes. This file
// exists for the four claims that specifically need a real browser and a
// real router:
//
//   1. The trace is reached by CLICKING the panel's own "Trace" link on its
//      asset page (the "Patching" panel, asset_detail.html), never by
//      fetching /interfaces/{id}/trace directly. This project has shipped a
//      404 on a button with every handler test green, because a handler
//      test injects router params by hand and never asks the router whether
//      anything can actually reach it (docs/E2E.md, CLAUDE.md's
//      evidence-gate note).
//   2. A breakout (pp-a2-3, three recorded strands at positions 1, 5 and 12
//      -- internal/seed/seed_cabling.go's panelBreakout) renders every
//      recorded strand, each labelled with the position it was declared at,
//      in position order.
//
//      ONE ROW AT A TIME IS THE ONLY WAY THE UI CAN SHOW IT. The sole
//      "Trace" link on the Patching panel is keyed to a FRONT port
//      (asset_detail.html:866) -- there is no link that starts a walk from a
//      rear port. Clicking one strand's row makes THAT strand the walk's own
//      starting point (its own position is therefore never itself printed --
//      see point 3), and the OTHER recorded strands become its children.
//      This test clicks two different rows and combines what each shows,
//      which between them names all three recorded positions -- 1, 5 and 12
//      -- each individually reachable and each in ascending order relative
//      to its siblings.
//   3. An ordinary 1:1 panel (pp-a2-2) still renders as a single chain with
//      no "strand" label at all -- the regression that would hurt everybody,
//      since almost every real run in this codebase is 1:1. Position 1 on an
//      ordinary panel is not noteworthy and printing it on every hop in the
//      estate would be noise (docs/panel-breakout-design.md D5, TraceRow.
//      Strand's own comment in internal/store/cabling.go).
//   4. The page claims nothing about the nine positions nobody recorded
//      (D4, corrected 2026-09-05): exactly as many "strand N" pills as
//      positions actually patched appear, no other number stands in for one,
//      and the page states what the figure excludes rather than merely
//      omitting it.
//
// Read-only. Never submits a form -- see docs/E2E.md, "This suite is
// read-only by default".
import { test, expect } from '@playwright/test';
import { resolveAssetPath } from '../helpers/resolve.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

// The "Patching" panel on an asset's detail page -- front to rear, inside
// this box (asset_detail.html's own panel-note). Scoped by its heading text
// rather than an id because the panel has none; "Patching" is unique on the
// page.
function patchingPanel(page) {
  return page.locator('.panel:has(h2:text("Patching"))');
}

// patchingRows reads the panel's own rows in the order the page renders
// them -- the same order PassThroughsFor (grouped by rear port, then
// position, WP-B4 Task 7) produces -- and each row's own "Trace" href, so
// this spec always follows a link the page actually renders rather than
// building one.
async function patchingRows(page) {
  const rows = patchingPanel(page).locator('table.grid tbody tr');
  const count = await rows.count();
  const out = [];
  for (let i = 0; i < count; i++) {
    const row = rows.nth(i);
    const front = (await row.locator('td').first().textContent()).trim();
    const href = await row.locator('a.btn', { hasText: 'Trace' }).getAttribute('href');
    out.push({ front, href });
  }
  return out;
}

describe('panel breakout on the trace page', () => {
  test('is reached by clicking the panel\'s own Trace link, not a direct URL fetch', async ({ page }) => {
    const consoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(msg.text());
    });

    const assetPath = await resolveAssetPath(page, 'pp-a2-3');
    await page.goto(assetPath, { waitUntil: 'networkidle' });

    const rows = await patchingRows(page);
    expect(rows.length, 'pp-a2-3 should show its three recorded strands').toBe(3);

    await Promise.all([
      page.waitForLoadState('networkidle'),
      patchingPanel(page).locator('a.btn', { hasText: 'Trace' }).first().click(),
    ]);

    await expect(
      page,
      'clicking the panel\'s own Trace link should land on /interfaces/{id}/trace',
    ).toHaveURL(/\/interfaces\/[^/]+\/trace$/);
    await expect(
      page.locator('#trace-path'),
      'the trace table should render after a real click-through navigation',
    ).toBeVisible();
    expect(consoleErrors, 'console errors on the trace page').toEqual([]);
  });

  test('a breakout renders every recorded strand, labelled with its position, in position order', async ({
    page,
  }) => {
    const assetPath = await resolveAssetPath(page, 'pp-a2-3');
    await page.goto(assetPath, { waitUntil: 'networkidle' });
    const rows = await patchingRows(page);
    // Also pins WP-B4 Task 7's ordering on the page that reads it: the panel
    // lists its one rear port's strands in position order (1, 5, 12), which
    // for this specific fixture happens to agree with front-port name order
    // too -- the fixture that tells the two apart (front-port names sorting
    // the OPPOSITE way from position) is deliberately Go-only,
    // cabling_test.go's TestPatchesAreListedInStrandOrderNotNameOrder; this
    // spec's job is confirming the browser renders what the store returns,
    // not re-deriving the sort itself.
    expect(rows.map((r) => r.front), 'the recorded strands, in the order the page lists them').toEqual([
      'front-01',
      'front-02',
      'front-03',
    ]);

    // Trace from position 1's own row (front-01): the OTHER two recorded
    // strands, 5 and 12, appear as its children, in position order.
    await page.goto(rows[0].href, { waitUntil: 'networkidle' });
    const traceFrom1 = page.locator('#trace-path');
    // Every panel hop (the rear port itself and each of its two children)
    // carries this pill, so at least one must be visible -- there is no
    // "exactly one" claim here, unlike the strand pills below.
    await expect(traceFrom1.getByText('through the panel').first()).toBeVisible();
    const strandsFrom1 = traceFrom1.locator('.pill-muted', { hasText: /^strand \d+$/ });
    await expect(strandsFrom1).toHaveCount(2);
    await expect(strandsFrom1.nth(0)).toHaveText('strand 5');
    await expect(strandsFrom1.nth(1)).toHaveText('strand 12');

    // Trace from position 5's own row (front-02): the OTHER two recorded
    // strands, 1 and 12, appear, again in position order. Between these two
    // page loads every one of the three recorded positions has been named,
    // individually reachable and correctly ordered -- as much as a UI with
    // no rear-port entry point can show (see the file header, point 2).
    await page.goto(rows[1].href, { waitUntil: 'networkidle' });
    const traceFrom5 = page.locator('#trace-path');
    const strandsFrom5 = traceFrom5.locator('.pill-muted', { hasText: /^strand \d+$/ });
    await expect(strandsFrom5).toHaveCount(2);
    await expect(strandsFrom5.nth(0)).toHaveText('strand 1');
    await expect(strandsFrom5.nth(1)).toHaveText('strand 12');
  });

  test('a 1:1 run still renders as a single chain, with no strand label', async ({ page }) => {
    const assetPath = await resolveAssetPath(page, 'pp-a2-2');
    await page.goto(assetPath, { waitUntil: 'networkidle' });
    const rows = await patchingRows(page);
    expect(rows.length, 'pp-a2-2 is the ordinary 1:1 fixture -- one recorded strand').toBe(1);

    await page.goto(rows[0].href, { waitUntil: 'networkidle' });
    const trace = page.locator('#trace-path');
    await expect(
      trace.locator('tbody tr'),
      'a 1:1 run is the start row plus exactly one hop, never a branch',
    ).toHaveCount(2);
    await expect(
      trace.locator('.pill-muted', { hasText: /^strand \d+$/ }),
      'position 1 on an ordinary panel is not noteworthy and must not be printed (D5)',
    ).toHaveCount(0);
    await expect(
      trace.locator('.pill-ok', { hasText: 'complete' }),
      'the run should still report reaching its far end',
    ).toHaveCount(1);
  });

  test('the breakout trace claims nothing about a position nobody recorded (D4)', async ({ page }) => {
    const assetPath = await resolveAssetPath(page, 'pp-a2-3');
    await page.goto(assetPath, { waitUntil: 'networkidle' });
    const rows = await patchingRows(page);
    await page.goto(rows[0].href, { waitUntil: 'networkidle' });

    const trace = page.locator('#trace-path');
    // Exactly the two OTHER recorded strands (5, 12) -- and nothing standing
    // in for a fourth. Nothing anywhere in this system records how many
    // positions a rear port physically has (docs/panel-breakout-design.md
    // D4, corrected 2026-09-05), so a fabricated "strand 2"/"strand 9", or a
    // bare unlabelled continuation implying more strands exist, would be
    // exactly the claim the design forbids.
    await expect(trace.locator('.pill-muted', { hasText: /^strand \d+$/ })).toHaveCount(2);
    for (const n of [2, 3, 4, 6, 7, 8, 9, 10, 11]) {
      await expect(
        trace.getByText(`strand ${n}`, { exact: true }),
        `strand ${n} was never recorded and must not appear`,
      ).toHaveCount(0);
    }

    // The page must SAY what the figure excludes, not merely omit it
    // (trace.html's own comment: "never blank, and never a single verdict").
    await expect(
      trace.getByText('nothing records how many positions a rear port physically has', { exact: false }),
      'the trace must state the D4 caveat, not just stay silent about a total',
    ).toBeVisible();
    await expect(
      trace,
      'no wording should imply a total, a free count, or an unpatched slot on this trunk',
    ).not.toContainText(/\bfree\b|\bout of\b|\bof 12\b|\b12 positions\b/i);
  });
});
