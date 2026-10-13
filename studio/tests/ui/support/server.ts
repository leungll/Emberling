/**
 * The fixture server for the UI review suite. It answers every Studio request to the
 * Backend (`/api/**` and `/ready`) from the recorded fixtures through `page.route`, so the
 * suite runs without a Backend, a database or Docker.
 *
 * A review state can override any path: an empty list, a load error, a slow response, a
 * 404, or a readiness probe that says the Backend is still starting. Requests that would
 * change something (POST, PUT, ...) are answered only when the state declares them; any
 * other one is recorded as an unexpected request and fails the review.
 */
import { readFileSync } from 'node:fs';
import path from 'node:path';

import type { Page, Route } from '@playwright/test';

interface FixtureIndex {
  recordedAt: string;
  paths: Record<string, { file: string; status: number }>;
}

const RECORDED = path.join(import.meta.dirname, '..', 'fixtures', 'recorded');
const fixtureIndex = JSON.parse(
  readFileSync(path.join(RECORDED, 'index.json'), 'utf8'),
) as FixtureIndex;

/** The instant the fixtures were recorded; the review clock is fixed here. */
export const RECORDED_AT = fixtureIndex.recordedAt;

export function fixture(pathname: string): unknown {
  const entry = fixtureIndex.paths[pathname];
  if (!entry?.file) throw new Error(`no recorded fixture for ${pathname}`);
  return JSON.parse(readFileSync(path.join(RECORDED, entry.file), 'utf8'));
}

export interface Reply {
  status?: number;
  body?: unknown;
  /** Holds the response this long, to show loading states. */
  delayMs?: number;
  /** Never answers: the request stays pending for the whole review. */
  hang?: boolean;
  /** The connection fails, as when the Backend is unreachable. */
  abort?: boolean;
}

export type Overrides = Record<string, Reply | ((url: URL, method: string) => Reply)>;

export interface FixtureServer {
  /** Requests no fixture or override answered; a review fails when this is not empty. */
  unexpected: string[];
}

export function errorBody(code: string, message: string): unknown {
  return { error: { code, message } };
}

function recorded(pathname: string, url: URL): Reply | null {
  const entry = fixtureIndex.paths[pathname];
  if (!entry) return null;
  if (pathname === '/ready') return { status: entry.status, body: { status: 'ready' } };
  const body = fixture(pathname);
  if (pathname.endsWith('/events')) {
    // The Backend pages events by afterSeq and limit; the fixture holds the whole list.
    const after = Number(url.searchParams.get('afterSeq') ?? '0');
    const limit = Number(url.searchParams.get('limit') ?? '1000');
    const items = (body as { items: { seq: number }[] }).items.filter((e) => e.seq > after);
    return { status: 200, body: { items: items.slice(0, limit) } };
  }
  return { status: entry.status, body };
}

async function fulfil(route: Route, reply: Reply): Promise<void> {
  if (reply.hang) return;
  if (reply.abort) {
    await route.abort('connectionrefused');
    return;
  }
  if (reply.delayMs) await new Promise((resolve) => setTimeout(resolve, reply.delayMs));
  await route.fulfill({
    status: reply.status ?? 200,
    contentType: 'application/json',
    body: JSON.stringify(reply.body ?? null),
  });
}

export async function serveFixtures(page: Page, overrides: Overrides = {}): Promise<FixtureServer> {
  const server: FixtureServer = { unexpected: [] };
  await page.route(
    (url) => url.pathname.startsWith('/api/') || url.pathname === '/ready',
    async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const method = request.method();
      const key = url.pathname;
      const override =
        overrides[`${method} ${key}`] ?? (method === 'GET' ? overrides[key] : undefined);
      if (override) {
        await fulfil(route, typeof override === 'function' ? override(url, method) : override);
        return;
      }
      // An event stream for a recorded run: the recorded runs are all terminal, so the
      // stream closes at once and the page keeps the history it already read.
      if (method === 'GET' && request.headers().accept?.includes('text/event-stream')) {
        await route.fulfill({ status: 204, body: '' });
        return;
      }
      const reply = method === 'GET' ? recorded(key, url) : null;
      if (reply) {
        await fulfil(route, reply);
        return;
      }
      server.unexpected.push(`${method} ${url.pathname}${url.search}`);
      await fulfil(route, {
        status: method === 'GET' ? 404 : 405,
        body: errorBody('NOT_FOUND', `no fixture for ${method} ${key}`),
      });
    },
  );
  return server;
}
