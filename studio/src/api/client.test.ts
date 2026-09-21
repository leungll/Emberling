import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  ApiRequestError,
  getAgentTrace,
  getNodeRunDetail,
  listDefinitions,
  saveDefinition,
} from './client';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('api client', () => {
  it('maps a non-2xx error envelope to ApiRequestError with the stable code', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(409, {
          error: {
            code: 'VERSION_CONFLICT',
            message: 'baseVersion 3 is not the latest version',
            details: { latestVersion: 4 },
          },
        }),
      ),
    );

    const failure = await saveDefinition('wf_123', {
      baseVersion: 3,
      name: 'AIGC Media Generation',
      description: '',
      nodes: [],
      edges: [],
    }).catch((error: unknown) => error);

    expect(failure).toBeInstanceOf(ApiRequestError);
    const apiError = failure as ApiRequestError;
    expect(apiError.status).toBe(409);
    expect(apiError.code).toBe('VERSION_CONFLICT');
    expect(apiError.message).toBe('baseVersion 3 is not the latest version');
    // The envelope survives intact so the UI never has to reconstruct it.
    expect(apiError.body.error.details).toEqual({ latestVersion: 4 });
  });

  it('falls back to INTERNAL when a failure carries no error envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('upstream exploded', { status: 502 })),
    );

    const failure = (await listDefinitions().catch((e: unknown) => e)) as ApiRequestError;

    expect(failure).toBeInstanceOf(ApiRequestError);
    expect(failure.status).toBe(502);
    expect(failure.code).toBe('INTERNAL');
  });

  it('unwraps the items envelope of a list response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(200, {
          items: [
            {
              workflowId: 'wf_123',
              name: 'AIGC Media Generation',
              description: 'Generate an image and caption',
              latestVersion: 4,
              updatedAt: '2026-08-03T12:00:00Z',
              lastRun: null,
            },
          ],
        }),
      ),
    );

    const items = await listDefinitions();

    expect(items).toHaveLength(1);
    expect(items[0]?.workflowId).toBe('wf_123');
    expect(items[0]?.lastRun).toBeNull();
  });

  it('sends a save as PUT with baseVersion in the body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { workflowId: 'wf_123' }));
    vi.stubGlobal('fetch', fetchMock);

    await saveDefinition('wf_123', {
      baseVersion: 4,
      name: 'n',
      description: '',
      nodes: [],
      edges: [],
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/definitions/wf_123');
    expect(init.method).toBe('PUT');
    expect(JSON.parse(String(init.body)).baseVersion).toBe(4);
  });

  it('reads Node Detail from the runs/{runId}/nodes/{nodeRunId} path', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(200, {
        nodeRun: { id: 'nr_123', status: 'WAITING_CALLBACK' },
        attempts: [],
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    const detail = await getNodeRunDetail('run_123', 'nr_123');

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/runs/run_123/nodes/nr_123');
    expect(detail.nodeRun.id).toBe('nr_123');
  });

  it('reads the Agent Trace from the runs/{runId}/nodes/{nodeRunId}/agent path', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(200, {
        agentRun: {
          id: 'ar_123',
          nodeRunId: 'nr_agent',
          termination: null,
          currentTurnNo: 2,
          currentContextVersion: 2,
          currentStateVersion: 1,
          deadline: '2026-08-03T12:05:00Z',
          terminatedAt: null,
          error: null,
        },
        turns: [
          {
            id: 'turn_1',
            turnNo: 1,
            status: 'COMPLETED',
            startedAt: '2026-08-03T12:00:01Z',
            completedAt: '2026-08-03T12:00:04Z',
            error: null,
            decision: { kind: 'TOOL_CALL', tool: 'lookup', hasStatePatch: false },
            action: null,
            toolAttempts: [],
          },
        ],
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    const trace = await getAgentTrace('run_123', 'nr_agent');

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/runs/run_123/nodes/nr_agent/agent');
    expect(trace.agentRun.id).toBe('ar_123');
    expect(trace.turns[0]?.decision?.tool).toBe('lookup');
  });

  it('maps an Agent Trace failure to ApiRequestError with the stable code', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'agent run not found' } }),
        ),
    );

    const failure = (await getAgentTrace('run_123', 'nr_agent').catch(
      (error: unknown) => error,
    )) as ApiRequestError;

    expect(failure).toBeInstanceOf(ApiRequestError);
    expect(failure.status).toBe(404);
    expect(failure.code).toBe('NOT_FOUND');
  });
});
