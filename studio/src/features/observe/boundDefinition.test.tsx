import { renderHook, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { useBoundDefinition } from './boundDefinition';
import type { Definition } from '@/api/types';
import { requestUrl } from '@/test/requestUrl';

function definition(workflowId: string, version: number, name: string): Definition {
  return {
    workflowId,
    version,
    name,
    description: '',
    nodes: [],
    edges: [],
    runInputSchema: { type: 'object', properties: {} },
    validation: { status: 'VALID', validatorVersion: 'v1', validatedAt: '2026-08-01T00:00:00Z' },
    createdAt: '2026-08-01T00:00:00Z',
  };
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('useBoundDefinition', () => {
  it("never returns the previous Run's Definition once the bound version changes", async () => {
    // The second version's read never settles, so anything rendered for it while in
    // flight can only come from the first version's stale state.
    let releaseSecond: (response: Response) => void = () => undefined;
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = new URL(requestUrl(input), 'http://studio.test');
        if (url.pathname === '/api/node-types') {
          return Promise.resolve(jsonResponse({ items: [] }));
        }
        if (url.pathname === '/api/definitions/wf_a/versions/1') {
          return Promise.resolve(jsonResponse(definition('wf_a', 1, 'First')));
        }
        if (url.pathname === '/api/definitions/wf_b/versions/2') {
          return new Promise<Response>((resolve) => {
            releaseSecond = resolve;
          });
        }
        throw new Error(`unexpected fetch: ${url.pathname}`);
      }),
    );

    const { result, rerender } = renderHook(
      ({ workflowId, version }) => useBoundDefinition(workflowId, version),
      { initialProps: { workflowId: 'wf_a', version: 1 } },
    );
    await waitFor(() => expect(result.current.definition?.name).toBe('First'));

    rerender({ workflowId: 'wf_b', version: 2 });
    expect(result.current.definition).toBeNull();
    expect(result.current.error).toBeNull();

    releaseSecond(jsonResponse(definition('wf_b', 2, 'Second')));
    await waitFor(() => expect(result.current.definition?.name).toBe('Second'));
  });
});
