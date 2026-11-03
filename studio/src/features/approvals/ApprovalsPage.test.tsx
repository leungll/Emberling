import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApprovalDetailPage } from './ApprovalDetailPage';
import { ApprovalsPage } from './ApprovalsPage';
import { useOperatorStore } from '@/stores/operator-store';
import { requestUrl } from '@/test/requestUrl';

const TOKEN = 'operator-token-for-the-page-test-0123456789abcdef';

const APPROVAL = {
  id: 'apr_1',
  actionId: 'act_1',
  runId: 'run_1',
  agentRunId: 'ar_1',
  status: 'PENDING',
  requestedAt: '2026-11-02T10:00:00Z',
  deadline: '2026-11-02T11:00:00Z',
  decidedAt: null,
  decidedBy: null,
  reason: null,
  toolName: 'deploy',
  arguments: { service: 'payments-api', version: '1.2.3' },
  workflowId: 'wf_1',
  nodeRunId: 'nr_agent',
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function headerOf(init: RequestInit | undefined, name: string): string | null {
  const h = new Headers(init?.headers);
  return h.get(name);
}

interface Call {
  url: string;
  init: RequestInit | undefined;
}
let calls: Call[];
let decisionResponse: () => Response;

beforeEach(() => {
  calls = [];
  decisionResponse = () =>
    jsonResponse({
      approval: { ...APPROVAL, status: 'APPROVED', decidedBy: 'test-operator' },
      repeated: false,
    });
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = requestUrl(input);
      calls.push({ url, init });
      if (headerOf(init, 'X-Emberling-Operator-Token') !== TOKEN) {
        return Promise.resolve(
          jsonResponse(
            {
              error: {
                code: 'INVALID_OPERATOR_CREDENTIAL',
                message: 'operator credential is missing or invalid',
              },
            },
            401,
          ),
        );
      }
      if (url === '/api/definitions') return Promise.resolve(jsonResponse({ items: [] }));
      if (url.startsWith('/api/approvals?status=PENDING')) {
        return Promise.resolve(jsonResponse({ items: [APPROVAL] }));
      }
      if (url.startsWith('/api/approvals?status='))
        return Promise.resolve(jsonResponse({ items: [] }));
      if (url === '/api/approvals/apr_1' && init?.method === 'GET') {
        return Promise.resolve(jsonResponse(APPROVAL));
      }
      if (url === '/api/approvals/apr_1/decision' && init?.method === 'POST') {
        return Promise.resolve(decisionResponse());
      }
      throw new Error(`unexpected fetch: ${url}`);
    }),
  );
  localStorage.clear();
  sessionStorage.clear();
  useOperatorStore.getState().clearToken();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  const router = createMemoryRouter(
    [
      { path: '/approvals', element: <ApprovalsPage /> },
      { path: '/approvals/:approvalId', element: <ApprovalDetailPage /> },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

function enterToken(token = TOKEN) {
  fireEvent.change(screen.getByLabelText('Operator token'), { target: { value: token } });
  fireEvent.click(screen.getByRole('button', { name: 'Use token' }));
}

describe('the Approvals list', () => {
  it('asks for the operator token, keeps it in memory only and sends it as a header', async () => {
    renderAt('/approvals');
    expect(calls).toHaveLength(0);
    expect(screen.getByText('Enter the operator token to see approvals.')).toBeInTheDocument();

    enterToken();

    await screen.findByRole('link', { name: 'deploy' });
    const list = calls.find((c) => c.url.startsWith('/api/approvals?status=PENDING'));
    expect(list).toBeDefined();
    expect(headerOf(list?.init, 'X-Emberling-Operator-Token')).toBe(TOKEN);
    // The credential never reaches browser storage or the address.
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain(TOKEN);
    expect(JSON.stringify(Object.entries(sessionStorage))).not.toContain(TOKEN);
    expect(list?.url).not.toContain(TOKEN);
    expect(screen.getByRole('tab', { name: 'Pending', selected: true })).toBeInTheDocument();
  });

  it('reports a rejected token without showing the token', async () => {
    renderAt('/approvals');
    enterToken('not-the-token');
    await screen.findByText('The operator token was not accepted.');
    expect(document.body.textContent).not.toContain('not-the-token');
  });
});

describe('the Approval detail', () => {
  it('approves through the API with the token header and shows the recorded decision', async () => {
    useOperatorStore.getState().setToken(TOKEN);
    renderAt('/approvals/apr_1');
    await screen.findByRole('heading', { name: 'deploy' });
    expect(screen.getByText('payments-api')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));

    await screen.findByText('Approved. The call runs now and the agent’s clock continues.');
    const post = calls.find((c) => c.url === '/api/approvals/apr_1/decision');
    expect(post?.init?.method).toBe('POST');
    expect(headerOf(post?.init, 'X-Emberling-Operator-Token')).toBe(TOKEN);
    expect(JSON.parse(post?.init?.body as string)).toEqual({ decision: 'APPROVE' });
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
    expect(screen.getByText('test-operator')).toBeInTheDocument();
  });

  it('requires a reason to reject and sends it', async () => {
    useOperatorStore.getState().setToken(TOKEN);
    decisionResponse = () =>
      jsonResponse({
        approval: {
          ...APPROVAL,
          status: 'REJECTED',
          decidedBy: 'test-operator',
          reason: 'not this week',
        },
        repeated: false,
      });
    renderAt('/approvals/apr_1');
    await screen.findByRole('heading', { name: 'deploy' });

    fireEvent.click(screen.getByRole('button', { name: 'Reject' }));
    expect(screen.getByText('Give a reason to reject.')).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith('/decision'))).toBe(false);

    fireEvent.change(screen.getByLabelText('Reason'), { target: { value: 'not this week' } });
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }));

    await screen.findByText('Rejected. The agent is told why and its clock continues.');
    const post = calls.find((c) => c.url === '/api/approvals/apr_1/decision');
    expect(JSON.parse(post?.init?.body as string)).toEqual({
      decision: 'REJECT',
      reason: 'not this week',
    });
  });

  it('shows the decision the Backend already holds after a conflict', async () => {
    useOperatorStore.getState().setToken(TOKEN);
    let detailCalls = 0;
    decisionResponse = () =>
      jsonResponse(
        {
          error: {
            code: 'APPROVAL_CONFLICT',
            message: 'already decided',
            details: { status: 'REJECTED' },
          },
        },
        409,
      );
    const fetchMock = vi.mocked(fetch);
    const original = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = requestUrl(input);
      if (url === '/api/approvals/apr_1' && init?.method === 'GET') {
        detailCalls += 1;
        calls.push({ url, init });
        return Promise.resolve(
          jsonResponse(
            detailCalls === 1
              ? APPROVAL
              : { ...APPROVAL, status: 'REJECTED', decidedBy: 'someone-else', reason: 'no' },
          ),
        );
      }
      return original?.(input, init) ?? Promise.reject(new Error('no implementation'));
    });

    renderAt('/approvals/apr_1');
    await screen.findByRole('heading', { name: 'deploy' });
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));

    await screen.findByText(
      'This approval was already decided differently. Showing the recorded decision.',
    );
    await waitFor(() => {
      expect(screen.getByText('someone-else')).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
  });
});
