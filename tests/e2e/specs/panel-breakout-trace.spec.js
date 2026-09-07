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
//   1. The trace is reached by CLICKING a link on the panel's own asset page
//      (the "Patching" panel, asset_detail.html), never by fetching
//      /interfaces/{id}/trace directly. This project has shipped a 404 on a
//      button with every handler test green, because a handler test injects
//      router params by hand and never asks the router whether anything can
//      actually reach it (docs/E2E.md, CLAUDE.md's evidence-gate note).
//
//      TWO DIFFERENT LINKS, TWO DIFFERENT QUESTIONS (asset_detail.html's own
//      comment on the row). "Trace strand" starts at the FRONT port and
//      follows that one strand outward -- the 1:1 question, and the only
//      link that existed before this table grew a rear-port entry point.
//      "Trace trunk" starts at the REAR port and is what fans every recorded
//      strand out at once -- the breakout's headline case. Both are covered
//      below, on the fixture where they give genuinely different answers.
//   2. A breakout (pp-a2-3, three recorded strands at positions 1, 5 and 12
//      -- internal/seed/seed_cabling.go's panelBreakout) renders every
//      recorded strand, each labelled with the position it was declared at,
//      in position order, IN ONE CLICK on "Trace trunk" -- that is the whole
//      point of a link that starts at the rear port rather than a front one.
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
// position, WP-B4 Task 7) produces -- and each row's OWN two hrefs, so this
// spec always follows a link the page actually renders rather than building
// one. "Trace strand" and "Trace trunk" are full, distinct link texts
// (asset_detail.html), so matching each by its own text is exact -- neither
// is a substring of the other, unlike the single "Trace" label this table
// used to render.
async function patchingRows(page) {
  const rows = patchingPanel(page).locator('table.grid tbody tr');
  const count = await rows.count();
  const out = [];
  for (let i = 0; i < count; i++) {
    const row = rows.nth(i);
    const cells = row.locator('td');
    const front = (await cells.nth(0).textContent()).trim();
    const position = (await cells.nth(2).textContent()).trim();
    const strandHref = await row.locator('a.btn', { hasText: 'Trace strand' }).getAttribute('href');
    const trunkHref = await row.locator('a.btn', { hasText: 'Trace trunk' }).getAttribute('href');
    out.push({ front, position, strandHref, trunkHref });
  }
  return out;
}

describe('panel breakout on the trace page', () => {
  test('the trunk trace is reached by clicking "Trace trunk", not a direct URL fetch', async ({ page }) => {
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
      patchingPanel(page).locator('a.btn', { hasText: 'Trace trunk' }).first().click(),
    ]);

    await expect(
      page,
      'clicking "Trace trunk" should land on /interfaces/{id}/trace',
    ).toHaveURL(/\/interfaces\/[^/]+\/trace$/);
    await expect(
      page.locator('#trace-path'),
      'the trace table should render after a real click-through navigation',
    ).toBeVisible();
    expect(consoleErrors, 'console errors on the trace page').toEqual([]);
  });

  test('"Trace trunk" renders every recorded strand in one click, labelled with its position, in position order', async ({
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
    expect(rows.map((r) => r.position), 'the Strand column, in the same order').toEqual(['1', '5', '12']);

    // ONE CLICK on the REAR port's own "Trace trunk" link -- every row on
    // this panel points at the same rear port, so any one of them reaches
    // the whole fan-out. All three recorded positions appear as children of
    // that one rear-port node, in position order, because nothing on the
    // walk up to here has been visited yet (unlike following one strand
    // outward -- see the "Trace strand" test below).
    await page.goto(rows[0].trunkHref, { waitUntil: 'networkidle' });
    const trace = page.locator('#trace-path');
    await expect(trace.getByText('through the panel').first()).toBeVisible();
    const strands = trace.locator('.pill-muted', { hasText: /^strand \d+$/ });
    await expect(strands).toHaveCount(3);
    await expect(strands.nth(0)).toHaveText('strand 1');
    await expect(strands.nth(1)).toHaveText('strand 5');
    await expect(strands.nth(2)).toHaveText('strand 12');
  });

  test('"Trace strand" follows one strand outward, a genuinely different question from the trunk', async ({
    page,
  }) => {
    const assetPath = await resolveAssetPath(page, 'pp-a2-3');
    await page.goto(assetPath, { waitUntil: 'networkidle' });
    const rows = await patchingRows(page);

    // Starting from position 1's own front port: this run's OWN position is
    // never itself printed (it is the starting point, not a continuation --
    // see the 1:1 test below for why position 1 is unlabelled generally too),
    // and the OTHER two recorded strands, 5 and 12, appear as the rear
    // port's remaining continuations, in position order.
    await page.goto(rows[0].strandHref, { waitUntil: 'networkidle' });
    const trace = page.locator('#trace-path');
    const strands = trace.locator('.pill-muted', { hasText: /^strand \d+$/ });
    await expect(strands).toHaveCount(2);
    await expect(strands.nth(0)).toHaveText('strand 5');
    await expect(strands.nth(1)).toHaveText('strand 12');
  });

  test('a 1:1 run still renders as a single chain, with no strand label', async ({ page }) => {
    const assetPath = await resolveAssetPath(page, 'pp-a2-2');
    await page.goto(assetPath, { waitUntil: 'networkidle' });
    const rows = await patchingRows(page);
    expect(rows.length, 'pp-a2-2 is the ordinary 1:1 fixture -- one recorded strand').toBe(1);

    await page.goto(rows[0].trunkHref, { waitUntil: 'networkidle' });
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

  test('the trunk trace claims nothing about a position nobody recorded (D4)', async ({ page }) => {
    const assetPath = await resolveAssetPath(page, 'pp-a2-3');
    await page.goto(assetPath, { waitUntil: 'networkidle' });
    const rows = await patchingRows(page);
    await page.goto(rows[0].trunkHref, { waitUntil: 'networkidle' });

    const trace = page.locator('#trace-path');
    // Exactly the three recorded strands (1, 5, 12) -- and nothing standing
    // in for a fourth. Nothing anywhere in this system records how many
    // positions a rear port physically has (docs/panel-breakout-design.md
    // D4, corrected 2026-09-05), so a fabricated "strand 2"/"strand 9", or a
    // bare unlabelled continuation implying more strands exist, would be
    // exactly the claim the design forbids.
    await expect(trace.locator('.pill-muted', { hasText: /^strand \d+$/ })).toHaveCount(3);
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
