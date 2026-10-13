/**
 * One review of one page state: serve the recorded fixtures, fix the clock, load the page,
 * then run the in-page checks, a keyboard walk and axe, and keep a full-page screenshot.
 *
 * A state is `block` (any violation fails the test) or `report` (violations are written to
 * the report and attached, but the test passes). Pages that have not been redesigned yet
 * run in report mode so the suite stays useful while they are being replaced.
 */
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';

import AxeBuilder from '@axe-core/playwright';
import { expect, type Page, type TestInfo } from '@playwright/test';

import { describeFocus, inspectPage, type CheckName, type Violation } from './checks/in-page';
import { RECORDED_AT, serveFixtures, type Overrides } from './server';

export const REPORT_DIR = path.join(import.meta.dirname, '..', 'report');

export interface ReviewState {
  /** A short kebab-case name; it names the screenshot. */
  name: string;
  url: string;
  mode: 'block' | 'report';
  overrides?: Overrides;
  /** Waits until the state under review is on screen. */
  ready: (page: Page) => Promise<void>;
  /** Drives the page into the state after it loads, for example by opening a menu. */
  act?: (page: Page) => Promise<void>;
  /** Checks that do not apply to this state; give the reason beside the entry. */
  skip?: CheckName[];
  /** Skips the Tab walk, for states whose focus is trapped on purpose. */
  noTabWalk?: boolean;
  /** Requests this state is expected to make that have no fixture. */
  allowUnexpected?: RegExp;
}

const TAB_LIMIT = 120;

async function tabWalk(page: Page): Promise<Violation[]> {
  const violations: Violation[] = [];
  await page.locator('body').click({ position: { x: 1, y: 1 }, force: true });
  for (let i = 0; i < TAB_LIMIT; i += 1) {
    await page.keyboard.press('Tab');
    const focus = await page.evaluate(describeFocus);
    // Focus has left the page or wrapped round to a stop it already visited.
    if (!focus || focus.revisit) break;
    if (!focus.visible) {
      violations.push({
        check: 'focus',
        selector: focus.selector,
        detail: 'focus lands on an invisible element',
      });
    } else if (!focus.ring) {
      violations.push({
        check: 'focus',
        selector: focus.selector,
        detail: 'no visible focus indicator',
      });
    }
  }
  return violations;
}

export async function review(page: Page, testInfo: TestInfo, state: ReviewState): Promise<void> {
  const consoleErrors: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('pageerror', (error) => consoleErrors.push(error.message));

  await page.clock.setFixedTime(new Date(RECORDED_AT));
  const server = await serveFixtures(page, state.overrides);
  await page.goto(state.url);
  await state.ready(page);
  if (state.act) await state.act(page);
  await page.evaluate(() => document.fonts.ready);

  const width = page.viewportSize()?.width ?? 0;
  const dir = path.join(REPORT_DIR, `w${width}`);
  mkdirSync(dir, { recursive: true });
  const shot = path.join(dir, `${state.name}.png`);
  await page.screenshot({ path: shot, fullPage: true, animations: 'disabled' });

  const { violations, exemptions } = await page.evaluate(inspectPage, { skip: state.skip });
  const focus = state.noTabWalk ? [] : await tabWalk(page);
  const axe = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    // Contrast is measured by the in-page check, which composites translucent surfaces.
    .disableRules(['color-contrast'])
    .analyze();
  const axeViolations: Violation[] = axe.violations.flatMap((rule) =>
    rule.nodes.map((node) => ({
      check: 'axe' as const,
      selector: node.target.join(' '),
      detail: `${rule.id}: ${rule.help}`,
    })),
  );
  const unexpected = server.unexpected.filter((request) => !state.allowUnexpected?.test(request));
  const all: Violation[] = [
    ...violations,
    ...focus,
    ...axeViolations,
    ...unexpected.map((request) => ({
      check: 'network' as const,
      selector: request,
      detail: 'request without a fixture',
    })),
    ...consoleErrors.map((text) => ({
      check: 'console' as const,
      selector: 'console',
      detail: text.slice(0, 200),
    })),
  ];

  const result = {
    state: state.name,
    url: state.url,
    width,
    mode: state.mode,
    violations: all,
    exemptions,
  };
  writeFileSync(path.join(dir, `${state.name}.json`), `${JSON.stringify(result, null, 2)}\n`);
  await testInfo.attach(`${state.name}.png`, { path: shot, contentType: 'image/png' });
  await testInfo.attach(`${state.name}.json`, {
    body: JSON.stringify(result, null, 2),
    contentType: 'application/json',
  });

  if (state.mode === 'block') {
    expect(all, `${state.name} at ${width}px`).toEqual([]);
  } else if (all.length > 0) {
    testInfo.annotations.push({
      type: 'report',
      description: `${all.length} findings in ${state.name} at ${width}px`,
    });
  }
}
