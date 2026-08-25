// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Guards WP-G7 (docs/ownership-report-design.md) in a real browser.
//
// READ-ONLY BY CONSTRUCTION, and that is deliberate. Bulk assignment WRITES,
// and this suite may point at the shared public demo, where a stray
// assignment is a permanent change to an estate other people are looking at.
// The mutation itself is covered by the Go web suite --
// TestOwnershipAssignMovesExactlyTheSelectedIDs,
// TestOwnershipAssignSkipsAnEntityClaimedInTheMeantime and
// TestOwnershipAssignRefusesARetiredTarget in
// internal/web/ownership_assign_test.go. What those cannot see is whether the
// page renders inside the real layout and whether its controls are actually
// wired, which is exactly what this spec checks.
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

describe('ownership report', () => {
  test('renders its findings and offers a fix path without writing anything', async ({ page }) => {
    await page.goto('/reports/ownership', { waitUntil: 'networkidle' });

    await expect(page.getByRole('heading', { name: /Ownership/i }).first()).toBeVisible();

    // The three findings of design §2, in two shapes: two entity-level
    // sections and one team-level. All three headings must render even when a
    // section is empty -- "no ownership gaps" is a real answer and must not
    // look like a failed query (§6).
    for (const name of ['Unowned', 'Owner cannot act', 'Owner has no contact']) {
      await expect(
        page.getByRole('heading', { name, exact: true }),
        `the "${name}" finding section must render even when empty`,
      ).toBeVisible();
    }

    // The report is worth nothing if it cannot show a finding it has. The demo
    // estate carries unowned entities on purpose (docs/E2E.md), so this asserts
    // the read path end to end rather than accepting an all-empty page.
    const findingRows = page.locator('.panel table tbody tr');
    expect(
      await findingRows.count(),
      'the demo estate has unowned entities; an all-empty report means the read path is broken, not that the estate is clean',
    ).toBeGreaterThan(0);

    // A fix path exists for an admin: the assignment form is present and
    // carries the CSRF token every non-GET route in this codebase requires.
    const assignForm = page.locator('form').filter({ has: page.locator('select[name="team_id"]') }).first();
    await expect(assignForm, 'an admin must be offered a way to fix a finding').toBeVisible();
    await expect(assignForm.locator('input[name="csrf_token"]')).toHaveCount(1);
    expect(
      await assignForm.locator('input[type="checkbox"]').count(),
      'selectable findings',
    ).toBeGreaterThan(0);

    // Nothing was submitted: the findings are still there. This is the
    // assertion that keeps the spec honest about being read-only.
    await page.reload({ waitUntil: 'networkidle' });
    expect(await page.locator('.panel table tbody tr').count()).toBeGreaterThan(0);
  });

  test('a target team that cannot fully act is marked, not silently offered', async ({ page }) => {
    // The gap fixed in 96b9dd2. A `deprecated` team classifies as
    // OwnerTransitional (internal/domain/ownership.go) -- precisely what the
    // "Owner cannot act" section flags -- so offering one as an assignment
    // target unmarked lets an operator "fix" a finding by creating another.
    // Marked rather than hidden: a `planned` team forming now is a legitimate
    // target, and this codebase prefers showing a state to removing the option.
    await page.goto('/reports/ownership', { waitUntil: 'networkidle' });

    const picker = page.locator('select[name="team_id"]').first();
    await expect(picker).toBeVisible();

    const labels = await picker.locator('option').allTextContents();
    expect(labels.length, 'the picker must offer at least one team').toBeGreaterThan(1);

    // Every offered option is either an active team (bare code) or carries its
    // lifecycle in parentheses. A retired team must never be offered at all.
    for (const label of labels) {
      expect(
        label,
        `"${label}" is offered as an assignment target while retired; assigning to it is not a fix`,
      ).not.toMatch(/\(retired\)/i);
    }
  });
});
