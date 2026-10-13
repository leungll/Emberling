import { act, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { AppSidebar, type NavCounts, type RecentItem } from './AppSidebar';
import { ListShell } from './ListShell';
import { WorkflowHeader } from './WorkflowHeader';
import { readListState, writeListState } from '@/lib/nav/list-state';
import { highlightRow, RETURNED_MS, useListMemory } from '@/lib/nav/use-list-memory';
import { SIDEBAR_KEY } from '@/lib/nav/use-sidebar';
import { requestUrl } from '@/test/requestUrl';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const ITEMS = [
  {
    workflowId: 'wf_a',
    name: 'Alpha',
    description: '',
    latestVersion: 1,
    updatedAt: '2026-08-01T00:00:00Z',
    lastRun: { id: 'run_a', status: 'FAILED', createdAt: '2026-08-03T11:34:00Z' },
  },
  {
    workflowId: 'wf_b',
    name: 'Beta',
    description: '',
    latestVersion: 2,
    updatedAt: '2026-08-02T00:00:00Z',
    lastRun: null,
  },
];

let readyStatus = 200;

beforeEach(() => {
  readyStatus = 200;
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = requestUrl(input);
      if (url === '/ready') return Promise.resolve(jsonResponse(readyStatus, {}));
      if (url === '/api/definitions') return Promise.resolve(jsonResponse(200, { items: ITEMS }));
      throw new Error(`unexpected fetch: ${url}`);
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
});

function renderAt(path: string, element = <p>page</p>) {
  const router = createMemoryRouter(
    [
      {
        element: <ListShell />,
        children: [
          { path: '/runs', element },
          { path: '/workflows', element },
        ],
      },
      { path: '/workflows/:workflowId', element: <p>editor</p> },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

describe('ListShell sidebar', () => {
  it('marks the current list, counts workflows and lists Recent with each last run', async () => {
    renderAt('/workflows');
    const nav = screen.getByRole('navigation', { name: 'Main' });
    const workflows = await within(nav).findByRole('link', { name: 'Workflows: 2 workflows' });
    expect(workflows).toHaveAttribute('aria-current', 'page');
    expect(within(nav).getByRole('link', { name: 'Runs' })).not.toHaveAttribute('aria-current');

    // Most recently updated first; each says how its last run went.
    const recent = screen.getByRole('button', { name: 'Recent' });
    expect(recent).toHaveAttribute('aria-expanded', 'true');
    const links = screen.getAllByRole('link', { name: /^(Alpha|Beta)/ });
    expect(links.map((l) => l.textContent)).toEqual([
      'Beta. Not run yet',
      expect.stringMatching(/^Alpha\. Last run failed, /),
    ]);
    expect(links[0]).toHaveAttribute('href', '/workflows/wf_b');
  });

  it('puts the viewer’s own visits first in Recent', async () => {
    localStorage.setItem('emb.recent', JSON.stringify(['wf_a']));
    renderAt('/runs');
    const links = await screen.findAllByRole('link', { name: /^(Alpha|Beta)/ });
    expect(links[0]).toHaveTextContent(/^Alpha/);
  });

  it('remembers Recent closed', async () => {
    const user = userEvent.setup();
    renderAt('/runs');
    await screen.findAllByRole('link', { name: /^(Alpha|Beta)/ });
    await user.click(screen.getByRole('button', { name: 'Recent' }));
    expect(screen.getByRole('button', { name: 'Recent' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
    expect(localStorage.getItem('emb.sidebar.recent')).toBe('closed');
  });

  it('shows the Backend connection from the readiness probe', async () => {
    readyStatus = 503;
    renderAt('/runs');
    const label = await screen.findByText('Backend starting');
    expect(label.closest('[role="status"]')).not.toBeNull();
  });

  it('collapses to the rail with "[" and the button, and remembers the choice', async () => {
    const user = userEvent.setup();
    renderAt('/runs');
    const aside = screen.getByRole('complementary', { name: 'Main' });
    expect(aside).toHaveAttribute('data-state', 'expanded');

    await user.keyboard('[[');
    expect(aside).toHaveAttribute('data-state', 'collapsed');
    expect(localStorage.getItem(SIDEBAR_KEY)).toBe('collapsed');
    // On the rail the names leave the page but stay with each link.
    expect(screen.queryByText('Recent')).toBeNull();
    expect(screen.getByRole('link', { name: 'Runs' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Expand sidebar' }));
    expect(aside).toHaveAttribute('data-state', 'expanded');
    expect(localStorage.getItem(SIDEBAR_KEY)).toBe('expanded');
  });

  it('does not toggle while the viewer types', async () => {
    const user = userEvent.setup();
    renderAt('/runs', <input aria-label="Search" />);
    await user.type(screen.getByRole('textbox', { name: 'Search' }), '[[');
    expect(screen.getByRole('complementary', { name: 'Main' })).toHaveAttribute(
      'data-state',
      'expanded',
    );
  });

  it('starts as the rail below 1280 px unless the viewer chose otherwise', () => {
    vi.stubGlobal('matchMedia', () => ({
      matches: false,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }));
    renderAt('/runs');
    expect(screen.getByRole('complementary', { name: 'Main' })).toHaveAttribute(
      'data-state',
      'collapsed',
    );
  });

  it('links each list as the viewer left it', async () => {
    writeListState('workflows', { qs: '?q=alpha' });
    renderAt('/runs');
    expect(await screen.findByRole('link', { name: 'Workflows: 2 workflows' })).toHaveAttribute(
      'href',
      '/workflows?q=alpha&ret=1',
    );
    // The page already shown links to itself as it opens.
    expect(screen.getByRole('link', { name: 'Runs' })).toHaveAttribute('href', '/runs');
  });
});

describe('AppSidebar counts', () => {
  const base = {
    section: 'runs' as const,
    onListPage: true,
    hrefs: { runs: '/runs', workflows: '/workflows' },
    recent: [] as RecentItem[],
    connection: 'ready' as const,
    collapsed: false,
    onToggle: () => undefined,
    onShortcuts: () => undefined,
  };

  function renderSidebar(counts: NavCounts) {
    const router = createMemoryRouter(
      [{ path: '/runs', element: <AppSidebar {...base} counts={counts} /> }],
      {
        initialEntries: ['/runs'],
      },
    );
    render(<RouterProvider router={router} />);
  }

  it('names live runs on the Runs link', () => {
    renderSidebar({ runs: { total: 12, running: 2, waiting: 1 }, workflows: 3 });
    expect(
      screen.getByRole('link', { name: 'Runs: 12 runs, 2 running, 1 waiting' }),
    ).toBeInTheDocument();
  });

  it('opens the shortcut sheet from the foot', () => {
    const onShortcuts = vi.fn();
    const router = createMemoryRouter(
      [
        {
          path: '/runs',
          element: (
            <AppSidebar
              {...base}
              counts={{ runs: null, workflows: null }}
              onShortcuts={onShortcuts}
            />
          ),
        },
      ],
      { initialEntries: ['/runs'] },
    );
    render(<RouterProvider router={router} />);
    fireEvent.click(screen.getByRole('button', { name: /Keyboard shortcuts/ }));
    expect(onShortcuts).toHaveBeenCalled();
  });
});

describe('WorkflowHeader', () => {
  function renderHeader(path: string, props: Partial<Parameters<typeof WorkflowHeader>[0]> = {}) {
    const router = createMemoryRouter(
      [
        {
          path: '*',
          element: <WorkflowHeader workflowId="wf_1" name="Alpha" tab="editor" {...props} />,
        },
      ],
      { initialEntries: [path] },
    );
    render(<RouterProvider router={router} />);
  }

  it('goes back to Workflows by default, as the list was left', () => {
    writeListState('workflows', { qs: '?q=al' });
    renderHeader('/workflows/wf_1');
    expect(screen.getByRole('link', { name: 'Back to Workflows' })).toHaveAttribute(
      'href',
      '/workflows?q=al&ret=1',
    );
    expect(screen.getByRole('heading', { name: 'Alpha' })).toBeInTheDocument();
  });

  it('goes back to Runs when entered from Runs, and keeps that on its tabs', () => {
    renderHeader('/workflows/wf_1?from=runs', { tab: 'runs', runId: 'run_9' });
    const back = screen.getByRole('link', { name: 'Back to Runs' });
    expect(back).toHaveAttribute('href', '/runs?ret=1');
    const views = screen.getByRole('navigation', { name: 'Workflow views' });
    expect(within(views).getByRole('link', { name: 'Editor' })).toHaveAttribute(
      'href',
      '/workflows/wf_1?from=runs',
    );
    expect(within(views).getByRole('link', { name: 'Runs' })).toHaveAttribute(
      'aria-current',
      'page',
    );

    // Leaving marks the run on screen as the row to find on the list.
    fireEvent.click(back);
    expect(readListState('runs').opened).toBe('run_9');
  });

  it('has no Runs tab to go to for a workflow that is not saved yet', () => {
    renderHeader('/workflows/new', { workflowId: undefined });
    const views = screen.getByRole('navigation', { name: 'Workflow views' });
    expect(within(views).queryByRole('link')).toBeNull();
    expect(within(views).getByText('Runs')).toHaveAttribute('aria-disabled', 'true');
  });
});

describe('list memory', () => {
  function Table() {
    useListMemory('workflows', true);
    const { search } = useLocation();
    return (
      <>
        <p data-testid="search">{search}</p>
        <table>
          <tbody>
            {['wf_a', 'wf_b'].map((id) => (
              <tr key={id} data-row={id}>
                <td>
                  <a href={`#${id}`} data-row-primary tabIndex={id === 'wf_a' ? 0 : -1}>
                    {id}
                  </a>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </>
    );
  }

  function renderTable(path: string) {
    const router = createMemoryRouter([{ path: '/workflows', element: <Table /> }], {
      initialEntries: [path],
    });
    render(<RouterProvider router={router} />);
    return router;
  }

  it('saves the query without the return marker, and the row opened', () => {
    renderTable('/workflows?q=be');
    expect(readListState('workflows').qs).toBe('?q=be');
    fireEvent.click(screen.getByText('wf_b'));
    expect(readListState('workflows').opened).toBe('wf_b');
  });

  it('on return, marks the row last opened and focuses it quietly, then drops the marker', () => {
    vi.useFakeTimers();
    try {
      writeListState('workflows', { qs: '?q=be', opened: 'wf_b' });
      const router = renderTable('/workflows?q=be&ret=1');
      const row = document.querySelector('tr[data-row="wf_b"]')!;
      const link = screen.getByText('wf_b');
      expect(row).toHaveAttribute('data-returned');
      expect(link).toHaveFocus();
      expect(link).toHaveAttribute('data-quiet-focus');
      expect(link).toHaveAttribute('tabindex', '0');
      expect(screen.getByText('wf_a')).toHaveAttribute('tabindex', '-1');
      expect(router.state.location.search).toBe('?q=be');

      // The first key shows the ring; the tint fades on its own.
      fireEvent.keyDown(document.body, { key: 'ArrowDown' });
      expect(link).not.toHaveAttribute('data-quiet-focus');
      act(() => {
        vi.advanceTimersByTime(RETURNED_MS);
      });
      expect(row).not.toHaveAttribute('data-returned');
    } finally {
      vi.useRealTimers();
    }
  });

  it('a pointer press drops the quiet focus instead', () => {
    document.body.innerHTML =
      '<table><tbody><tr data-row="x"><td><a href="#x" data-row-primary>x</a></td></tr></tbody></table>';
    expect(highlightRow(document, 'x')).toBe(true);
    const link = document.querySelector('a')!;
    expect(link).toHaveFocus();
    fireEvent.pointerDown(document.body);
    expect(link).not.toHaveFocus();
    expect(link).not.toHaveAttribute('data-quiet-focus');
  });

  it('highlights nothing when the restored list does not show the row', () => {
    expect(highlightRow(document, 'missing')).toBe(false);
    expect(highlightRow(document, undefined)).toBe(false);
  });
});
