import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { HumanWaitsPanel } from './HumanWaitsPanel';
import { useOperatorStore } from '@/stores/operator-store';
import { requestUrl } from '@/test/requestUrl';

const TOKEN = 'operator-token-for-the-human-wait-test-0123456789';

const WAIT = {
  id: 'hw_1',
  status: 'OPEN',
  openedAt: '2026-11-02T10:05:00Z',
  deadline: '2026-11-02T10:35:00Z',
  closedAt: null,
  closedBy: null,
  outcome: null,
  actionId: 'act_1',
  runId: 'run_1',
  agentRunId: 'ar_1',
  nodeRunId: 'nr_agent',
  workflowId: 'wf_1',
  toolName: 'deploy_change',
  targetKey: 'token-service/staging',
  operationKey: 'op_act_1_1',
  confirmDeadline: '2026-11-02T10:05:00Z',
  reason: 'NOT_CONFIRMED',
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

interface Call {
  url: string;
  init: RequestInit | undefined;
}
let calls: Call[];
let listItems: unknown[];
let stopResponse: () => Response;

beforeEach(() => {
  calls = [];
  listItems = [WAIT];
  stopResponse = () =>
    jsonResponse({
      humanWait: {
        ...WAIT,
        status: 'STOPPED',
        closedBy: 'test-operator',
        closedAt: '2026-11-02T10:10:00Z',
        outcome: 'RELEASE_UNKNOWN',
      },
      repeated: false,
    });
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = requestUrl(input);
      calls.push({ url, init });
      if (new Headers(init?.headers).get('X-Emberling-Operator-Token') !== TOKEN) {
        return Promise.resolve(
          jsonResponse({ error: { code: 'INVALID_OPERATOR_CREDENTIAL', message: 'denied' } }, 401),
        );
      }
      if (url === '/api/human-waits' && init?.method === 'GET') {
        return Promise.resolve(jsonResponse({ items: listItems }));
      }
      if (url === '/api/human-waits/hw_1/stop' && init?.method === 'POST') {
        return Promise.resolve(stopResponse());
      }
      throw new Error(`unexpected fetch: ${url}`);
    }),
  );
  useOperatorStore.getState().setToken(TOKEN);
});

afterEach(() => {
  vi.unstubAllGlobals();
  useOperatorStore.getState().clearToken();
});

function renderPanel() {
  render(
    <MemoryRouter>
      <HumanWaitsPanel />
    </MemoryRouter>,
  );
}

describe('the human wait panel', () => {
  it('shows the target, operation, deadlines and reason of an open wait', async () => {
    renderPanel();
    const card = await screen.findByRole('listitem', {
      name: 'deploy_change token-service/staging',
    });
    expect(within(card).getByText('token-service/staging')).toBeInTheDocument();
    expect(within(card).getByText('Target')).toBeInTheDocument();
    expect(within(card).getByText('Operation')).toBeInTheDocument();
    expect(within(card).getByText('Confirm deadline')).toBeInTheDocument();
    expect(within(card).getByText('Wait ends')).toBeInTheDocument();
    expect(
      within(card).getByText('Production knows the operation but has not confirmed it.'),
    ).toBeInTheDocument();
    const list = calls.find((c) => c.url === '/api/human-waits');
    expect(new Headers(list?.init?.headers).get('X-Emberling-Operator-Token')).toBe(TOKEN);
    expect(list?.url).not.toContain(TOKEN);
  });

  it('stops only after the confirmation and shows the stopped task', async () => {
    renderPanel();
    const card = await screen.findByRole('listitem', {
      name: 'deploy_change token-service/staging',
    });

    fireEvent.click(within(card).getByRole('button', { name: 'Stop waiting' }));
    const dialog = await screen.findByRole('dialog', { name: 'Stop waiting for this release?' });
    expect(within(dialog).getByText(/token-service\/staging stays held/)).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith('/stop'))).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith('/stop'))).toBe(false);

    fireEvent.click(within(card).getByRole('button', { name: 'Stop waiting' }));
    listItems = [];
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Stop the task' }),
    );

    await screen.findByText(
      'Stopped. The task has ended; the target stays held until production answers.',
    );
    const stop = calls.find((c) => c.url === '/api/human-waits/hw_1/stop');
    expect(stop?.init?.method).toBe('POST');
    expect(new Headers(stop?.init?.headers).get('X-Emberling-Operator-Token')).toBe(TOKEN);
    await screen.findByText('No releases are waiting for a person.');
    expect(document.body.textContent).not.toContain(TOKEN);
  });

  it('reports that production answered first when the stop lost', async () => {
    stopResponse = () =>
      jsonResponse(
        {
          error: {
            code: 'HUMAN_WAIT_RESOLVED',
            message: 'the release concluded first',
            details: { status: 'RESOLVED' },
          },
        },
        409,
      );
    renderPanel();
    const card = await screen.findByRole('listitem', {
      name: 'deploy_change token-service/staging',
    });
    fireEvent.click(within(card).getByRole('button', { name: 'Stop waiting' }));
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Stop the task' }),
    );
    await screen.findByText('Production answered first, so the release concluded on its own.');
  });

  it('reports a task that already ended', async () => {
    stopResponse = () =>
      jsonResponse(
        { error: { code: 'HUMAN_WAIT_CLOSED', message: 'run ended', details: { status: 'OPEN' } } },
        409,
      );
    renderPanel();
    const card = await screen.findByRole('listitem', {
      name: 'deploy_change token-service/staging',
    });
    fireEvent.click(within(card).getByRole('button', { name: 'Stop waiting' }));
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Stop the task' }),
    );
    await screen.findByText('The task already ended. This wait closes on its own at its deadline.');
  });

  it('shows nothing without an operator token', () => {
    useOperatorStore.getState().clearToken();
    renderPanel();
    expect(screen.queryByText('Releases waiting for a person')).not.toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});
