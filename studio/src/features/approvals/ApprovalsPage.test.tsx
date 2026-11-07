import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
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

const CHANGE_REQUEST_CONTENT =
  '# Change request\n\n## Patch\n\n- Patch digest: `d1d1d1`\n\n## Summary\n\nUpgrade the signing library.\n\n## Decision basis\n\n1. The advisory names the fixed release. (turn 3, manifest item `advisories/GO-2025-3553.md`)\n';

const APPROVAL_DETAIL = {
  ...APPROVAL,
  toolName: 'deploy_change',
  arguments: { changeRequestId: 'cr_1' },
  changeRequest: {
    id: 'cr_1',
    patchDigest: 'd1d1d1',
    verificationFactId: 'fact_1',
    contentArtifactId: 'artifact_1',
    contentDigest: 'c2c2c2',
    toolAttemptId: 'tool_attempt_cr',
    createdAt: '2026-11-02T09:59:00Z',
    content: CHANGE_REQUEST_CONTENT,
    decisionBasis: [
      {
        statement: 'The advisory names the fixed release.',
        turnNo: 3,
        turnId: 'turn_3',
        manifestRef: 'advisories/GO-2025-3553.md',
        item: {
          kind: 'WORKSPACE_FILE',
          label: 'advisories/GO-2025-3553.md',
          ref: 'advisories/GO-2025-3553.md',
          version: '0',
          sha256: 'abcd',
          length: 120,
          included: true,
          omittedReason: null,
          range: null,
        },
      },
      {
        statement: 'The verification passed.',
        turnNo: 9,
        turnId: '',
        manifestRef: 'tool_attempt_gone',
        item: null,
      },
    ],
  },
  patch: {
    seq: 3,
    patchDigest: 'd1d1d1',
    touchedPaths: ['go.mod', 'internal/auth/token.go'],
    toolAttemptId: 'tool_attempt_write',
    createdAt: '2026-11-02T09:58:00Z',
    patch:
      'diff --git a/go.mod b/go.mod\n-require github.com/golang-jwt/jwt/v5 v5.2.1\n+require github.com/golang-jwt/jwt/v5 v5.3.1\n',
  },
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
let detailResponse: () => Response;

beforeEach(() => {
  calls = [];
  detailResponse = () => jsonResponse({ ...APPROVAL, changeRequest: null, patch: null });
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
      if (url === '/api/human-waits') return Promise.resolve(jsonResponse({ items: [] }));
      if (url.startsWith('/api/approvals?status=PENDING')) {
        return Promise.resolve(jsonResponse({ items: [APPROVAL] }));
      }
      if (url.startsWith('/api/approvals?status='))
        return Promise.resolve(jsonResponse({ items: [] }));
      if (url === '/api/approvals/apr_1' && init?.method === 'GET') {
        return Promise.resolve(detailResponse());
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

  it('bounds the reason by bytes and sends a 400-byte non-ASCII reason whole', async () => {
    useOperatorStore.getState().setToken(TOKEN);
    const reason = 'é'.repeat(200); // 400 bytes in UTF-8
    decisionResponse = () =>
      jsonResponse({
        approval: { ...APPROVAL, status: 'REJECTED', decidedBy: 'test-operator', reason },
        repeated: false,
      });
    renderAt('/approvals/apr_1');
    await screen.findByRole('heading', { name: 'deploy' });

    fireEvent.change(screen.getByLabelText('Reason'), { target: { value: 'é'.repeat(201) } });
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }));
    expect(screen.getByText('The reason must be at most 400 bytes.')).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith('/decision'))).toBe(false);

    fireEvent.change(screen.getByLabelText('Reason'), { target: { value: reason } });
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }));

    await screen.findByText('Rejected. The agent is told why and its clock continues.');
    const post = calls.find((c) => c.url === '/api/approvals/apr_1/decision');
    expect(JSON.parse(post?.init?.body as string)).toEqual({ decision: 'REJECT', reason });
  });

  it('explains a pending approval whose run has ended instead of calling it decided', async () => {
    useOperatorStore.getState().setToken(TOKEN);
    decisionResponse = () =>
      jsonResponse(
        { error: { code: 'APPROVAL_NOT_ACTIONABLE', message: 'the run has ended' } },
        409,
      );
    renderAt('/approvals/apr_1');
    await screen.findByRole('heading', { name: 'deploy' });

    fireEvent.click(screen.getByRole('button', { name: 'Approve' }));

    await screen.findByText(
      'The run this approval belongs to has already ended. The approval stays undecided and expires at its deadline.',
    );
    expect(
      screen.queryByText(
        'This approval was already decided differently. Showing the recorded decision.',
      ),
    ).not.toBeInTheDocument();
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
              ? { ...APPROVAL, changeRequest: null, patch: null }
              : {
                  ...APPROVAL,
                  status: 'REJECTED',
                  decidedBy: 'someone-else',
                  reason: 'no',
                  changeRequest: null,
                  patch: null,
                },
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

describe('the Approval detail with a change request', () => {
  it('renders the change request as Markdown, links each basis item and shows the patch text', async () => {
    detailResponse = () => jsonResponse(APPROVAL_DETAIL);
    useOperatorStore.getState().setToken(TOKEN);
    renderAt('/approvals/apr_1');
    await screen.findByRole('heading', { name: 'deploy_change' });

    const changeRequest = screen.getByRole('region', { name: 'Change request' });
    expect(within(changeRequest).getByRole('heading', { name: 'Summary' })).toBeInTheDocument();
    expect(within(changeRequest).getByText('Upgrade the signing library.')).toBeInTheDocument();
    expect(within(changeRequest).getAllByText('d1d1d1').length).toBeGreaterThan(0);
    expect(within(changeRequest).getByText('fact_1')).toBeInTheDocument();

    expect(
      within(changeRequest).getByText('The advisory names the fixed release.'),
    ).toBeInTheDocument();
    const turnLinks = within(changeRequest).getAllByRole('link', { name: /^Turn \d+$/ });
    expect(turnLinks.map((l) => l.textContent)).toEqual(['Turn 3', 'Turn 9']);
    expect(turnLinks[0]).toHaveAttribute('href', '/runs/run_1');
    expect(
      within(changeRequest).getByText('workspace file: advisories/GO-2025-3553.md'),
    ).toBeInTheDocument();
    expect(
      within(changeRequest).getByText('The cited manifest item can no longer be resolved.'),
    ).toBeInTheDocument();

    const patch = screen.getByRole('region', { name: 'Patch' });
    expect(within(patch).getByText('Workspace patch 3')).toBeInTheDocument();
    expect(within(patch).getByText('go.mod, internal/auth/token.go')).toBeInTheDocument();
    expect(
      within(patch).getByText(/\+require github.com\/golang-jwt\/jwt\/v5 v5.3.1/),
    ).toBeInTheDocument();
    expect(document.querySelector('script')).toBeNull();
  });

  it('says so when the call names no change request and shows no patch section', async () => {
    useOperatorStore.getState().setToken(TOKEN);
    renderAt('/approvals/apr_1');
    await screen.findByRole('heading', { name: 'deploy' });
    expect(screen.getByText('This call names no change request.')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Patch' })).not.toBeInTheDocument();
  });

  it('explains a change request whose patch record is missing', async () => {
    detailResponse = () => jsonResponse({ ...APPROVAL_DETAIL, patch: null });
    useOperatorStore.getState().setToken(TOKEN);
    renderAt('/approvals/apr_1');
    await screen.findByRole('heading', { name: 'deploy_change' });
    expect(
      screen.getByText('The workspace holds no patch with the digest this change request names.'),
    ).toBeInTheDocument();
  });
});
