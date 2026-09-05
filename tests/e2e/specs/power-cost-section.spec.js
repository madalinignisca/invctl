// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-I2 Task 9: the electricity-estimate section on /reports/cost
// (docs/power-cost-design.md, web/templates/partials/power_cost.html). Unit
// and functional (real-router) tests already assert every string this
// section can render -- internal/domain/power_cost_test.go and
// internal/web/power_cost_test.go -- so nothing here repeats a substring
// check a Go test already makes. This file exists for the four claims that
// specifically need a real browser and a real DOM:
//
//   1. The section is reachable by actually CLICKING through the nav rail
//      (Reports -> "What it costs"), not by fetching the URL directly. This
//      project has shipped a 404 on a button with every handler test green,
//      because a handler test injects router params by hand and never asks
//      the router whether anything can actually reach it (docs/E2E.md,
//      CLAUDE.md's evidence-gate note) -- routing this test through nav.go's
//      own link is the whole point.
//   2. Exactly one of the section's THREE render states is visible at a time
//      -- no tariff configured (D5), a tariff with nothing declared (§4b.7),
//      or a tariff with a figure. Zero or two states rendering together is a
//      template-branching bug no Go string-match test would notice, because
//      `strings.Contains` on a body that (wrongly) contains both branches'
//      text still finds each string it looks for.
//   3. "Not comparable to an all-in hosting quote" (§2.3) sits INSIDE THE
//      SAME #power-cost element the amount renders in, not merely somewhere
//      on the page -- a DOM-ancestry claim a substring check cannot make.
//   4. The estate's priced totals above are visually and structurally
//      DISTINCT from the derived power figure (§2.4): the totals are a bare
//      stat-row with no panel; the power figure sits inside its own bordered
//      panel with its own heading tier, and never reuses the totals' "Per
//      month" label -- the exact collision power_cost.html's own comment
//      names as the failure mode this guards against.
//
// This is a content assertion in every branch, never a runtime skip: D5 and
// "tariff but no draws" are real, legitimate states with their own real
// assertions below, not a reason to skip anything (docs/E2E.md, CLAUDE.md's
// testing policy on the difference between "cannot exercise this branch
// right now" and "this test found nothing so it gave up").
//
// Read-only. Never submits a form -- see docs/E2E.md, "This suite is
// read-only by default".
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

describe('power cost section on /reports/cost', () => {
  test('is reached by clicking through the nav rail, not a direct URL fetch', async ({ page }) => {
    const consoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(msg.text());
    });

    await page.goto('/', { waitUntil: 'networkidle' });

    // nav.go's "Reports" group holds "What it costs" -> /reports/cost. The
    // group starts collapsed on a fresh session (web/static/app.js's
    // navGroup: closed unless it is the current page or was remembered open
    // in localStorage), so open it first if the link isn't already showing.
    const group = page.locator('.rail-group[data-group="Reports"]');
    await expect(group, 'the Reports nav group should exist in the rail').toHaveCount(1);
    const link = group.locator('a.rail-link', { hasText: 'What it costs' });
    if (!(await link.isVisible())) {
      await group.locator('.rail-group-head').click();
    }
    await expect(link, 'the "What it costs" link should be reachable from the Reports group').toBeVisible();

    await Promise.all([
      page.waitForLoadState('networkidle'),
      link.click(),
    ]);

    await expect(page, 'clicking "What it costs" should land on /reports/cost').toHaveURL(/\/reports\/cost$/);

    // D5 guarantees this heading renders unconditionally, in every state --
    // there is no branch here to skip on (docs/power-cost-design.md D5).
    await expect(
      page.locator('#power-cost h3', { hasText: 'Electricity, estimated' }),
      'the electricity heading should render inside the real layout after a real click-through navigation',
    ).toBeVisible();

    expect(consoleErrors, 'console errors on /reports/cost').toEqual([]);
  });

  test('renders exactly one of the three states', async ({ page }) => {
    await page.goto('/reports/cost', { waitUntil: 'networkidle' });

    const panel = page.locator('#power-cost');
    await expect(panel, 'the power-cost panel should always render (D5)').toBeVisible();

    // The three states are mutually exclusive branches of the same
    // {{if}}/{{else if}}/{{else}} in power_cost.html -- see the file's own
    // comment. Both present, or neither, means the template rendered more
    // than one branch (or none), which no Go body.Contains test would catch
    // if it merely looked for each string in isolation.
    const noTariff = panel.getByText('No electricity figure: no tariff is configured.');
    const noDraw = panel.getByText('nothing in the estate currently declares a power draw');
    const figure = panel.locator('.stat-value');

    const [noTariffCount, noDrawCount, figureCount] = await Promise.all([
      noTariff.count(),
      noDraw.count(),
      figure.count(),
    ]);

    const statesPresent = [noTariffCount > 0, noDrawCount > 0, figureCount > 0].filter(Boolean).length;
    expect(
      statesPresent,
      `expected exactly one of the three power-cost states to be visible, saw ${statesPresent} ` +
        `(no-tariff=${noTariffCount}, no-draw=${noDrawCount}, figure=${figureCount})`,
    ).toBe(1);
  });

  test('the hosting-quote caveat sits inside the same section as the figure, and only when there is one', async ({
    page,
  }) => {
    await page.goto('/reports/cost', { waitUntil: 'networkidle' });

    const panel = page.locator('#power-cost');
    const figureVisible = (await panel.locator('.stat-value').count()) > 0;
    const caveat = panel.getByText('Not comparable to an all-in hosting quote', { exact: false });

    if (figureVisible) {
      // The claim a Go string check cannot make: "beside the number" is
      // ancestry, not substring presence. Scoping the locator to `panel`
      // (#power-cost) before searching for the caveat text means this
      // assertion only passes if the caveat is a DESCENDANT of the exact
      // element the amount lives in.
      await expect(
        caveat,
        'the hosting-quote caveat must be inside #power-cost, the same section as the amount',
      ).toHaveCount(1);
    } else {
      // D5, or a tariff configured over an estate with no declared draws
      // (§4b.7): there is no figure for the caveat to sit beside, so it must
      // not appear stranded with nothing to caveat.
      await expect(
        caveat,
        'no figure is shown in this state, so the hosting-quote caveat must not appear either',
      ).toHaveCount(0);
    }
  });

  test('the estate totals above are visually and structurally distinct from the power figure', async ({ page }) => {
    await page.goto('/reports/cost', { waitUntil: 'networkidle' });

    // cost_report.html: the priced estate totals are a bare .stat-row, a
    // DIRECT child of #cost-report -- no bordered .panel, no own heading.
    const estateTotals = page.locator('#cost-report > .stat-row');
    await expect(
      estateTotals,
      'the priced estate totals should render as a bare stat-row directly on the page',
    ).toHaveCount(1);
    await expect(estateTotals.locator('.stat-label', { hasText: 'Per month' })).toBeVisible();

    // power_cost.html: the power figure sits inside its OWN bordered .panel
    // with its own heading tier (h3, "Electricity, estimated"), separated
    // from the totals above by <hr class="section-break"> -- never sharing
    // the totals' stat-row styling or its "Per month" label.
    const powerPanel = page.locator('#power-cost.panel');
    await expect(
      powerPanel,
      'the power-cost section should be its own bordered panel, not a bare stat-row like the estate totals',
    ).toHaveCount(1);
    await expect(page.locator('hr.section-break')).toHaveCount(1);

    // The label collision power_cost.html's own comment names as the exact
    // failure mode docs/power-cost-design.md §2.4 exists to prevent: a
    // reader seeing two stat-rows both labelled "Per month" would read them
    // as one series and add them.
    await expect(
      powerPanel.getByText('Per month', { exact: true }),
      'the power section must never reuse the estate stat-row\'s "Per month" label',
    ).toHaveCount(0);
  });
});
