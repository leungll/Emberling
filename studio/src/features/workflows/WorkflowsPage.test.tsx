import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { WorkflowsPage } from './WorkflowsPage';
import type { DefinitionListItem } from '@/api/types';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function item(
  workflowId: string,
  name: string,
  updatedAt: string,
  lastRun: DefinitionListItem['lastRun'],
  description = '',
): DefinitionListItem {
  return { workflowId, name, description, latestVersion: 2, updatedAt, lastRun };
}

const ITEMS: DefinitionListItem[] = [
  item(
    'wf_aaaa1111bbbb2222',
    'Release checker',
    '2026-08-03T12:00:00Z',
    {
      id: 'run_1111aaaa2222',
      status: 'COMPLETED',
      createdAt: '2026-08-03T12:10:00Z',
      error: null,
      failedStep: null,
    },
    'Checks a release before it ships',
  ),
  item('wf_cccc3333dddd4444', 'Draft writer', '2026-08-02T09:00:00Z', null),
  item('wf_eeee5555ffff6666', 'Image maker', '2026-08-04T09:00:00Z', {
    id: 'run_3333cccc4444',
    status: 'FAILED',
    createdAt: '2026-08-04T09:10:00Z',
    error: { code: 'TIMEOUT' },
    failedStep: { nodeId: 'n1', nodeName: 'Painter' },
  }),
];

const DEFINITION = {
  workflowId: 'wf_aaaa1111bbbb2222',
  version: 2,
  name: 'Release checker',
  description: '',
  nodes: [],
  edges: [],
  runInputSchema: { type: 'object', properties: {}, required: [] },
  validation: { valid: true, errors: [] },
  createdAt: '2026-08-03T12:00:00Z',
};

type Routes = Record<string, () => Response>;

function stubFetch(routes: Routes) {
  const fetchMock = vi.fn((input: RequestInfo | URL, _init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(url, 'http://studio.test').pathname;
    const reply = routes[path];
    return Promise.resolve(
      reply ? reply() : jsonResponse(404, { error: { code: 'NOT_FOUND', message: path } }),
    );
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

const listOf = (items: DefinitionListItem[]): Routes => ({
  '/api/definitions': () => jsonResponse(200, { items }),
});

/** Shows where the router went, the query and the state it carried. */
function Probe() {
  const location = useLocation();
  return (
    <pre data-testid="probe">
      {JSON.stringify({
        path: location.pathname,
        search: location.search,
        state: location.state as unknown,
      })}
    </pre>
  );
}

function probe(): { path: string; search: string; state: unknown } {
  return JSON.parse(screen.getByTestId('probe').textContent ?? '{}') as {
    path: string;
    search: string;
    state: unknown;
  };
}

function renderPage(url = '/workflows') {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route
          path="/workflows"
          element={
            <>
              <WorkflowsPage />
              <Probe />
            </>
          }
        />
        <Route path="*" element={<Probe />} />
      </Routes>
    </MemoryRouter>,
  );
}

const rowNames = () =>
  screen
    .getAllByRole('row')
    .slice(1)
    .map((r) => within(r).getAllByRole('link')[0]!.textContent);

beforeEach(() => {
  window.localStorage.clear();
  window.sessionStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('WorkflowsPage states', () => {
  it('shows the error code and retries the list', async () => {
    let fail = true;
    stubFetch({
      '/api/definitions': () =>
        fail
          ? jsonResponse(503, {
              error: { code: 'DEPENDENCY_UNAVAILABLE', message: 'PostgreSQL is unavailable' },
            })
          : jsonResponse(200, { items: ITEMS }),
    });
    const user = userEvent.setup();
    renderPage();

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Couldn’t load workflows.');
    expect(alert).toHaveTextContent('503 DEPENDENCY_UNAVAILABLE');
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
    expect(screen.queryByRole('searchbox')).not.toBeInTheDocument();

    fail = false;
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('Release checker')).toBeInTheDocument();
  });

  it('shows the empty state with the one New workflow button', async () => {
    stubFetch(listOf([]));
    renderPage();

    expect(await screen.findByText('No workflows yet.')).toBeInTheDocument();
    expect(screen.getByText('Create one to define the steps a run will follow.')).toBeVisible();
    expect(screen.getAllByRole('button', { name: 'New workflow' })).toHaveLength(1);
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});

describe('WorkflowsPage rows', () => {
  it('renders name, description, last run and a workflow that never ran', async () => {
    stubFetch(listOf(ITEMS));
    renderPage();

    const name = await screen.findByRole('link', { name: 'Release checker' });
    expect(name).toHaveAttribute('href', '/workflows/wf_aaaa1111bbbb2222');
    expect(screen.getByText('Checks a release before it ships')).toBeInTheDocument();

    const lastRun = screen.getByRole('link', {
      name: /^Last run completed, .+\. Open run 1111aaaa$/,
    });
    expect(lastRun).toHaveAttribute('href', '/workflows/wf_aaaa1111bbbb2222/runs/run_1111aaaa2222');
    expect(within(lastRun).getByText('Completed')).toBeInTheDocument();
    expect(within(lastRun).getByText((_, el) => el?.tagName === 'TIME')).toHaveAttribute(
      'dateTime',
      '2026-08-03T12:10:00Z',
    );

    const draftRow = screen.getByRole('link', { name: 'Draft writer' }).closest('tr')!;
    expect(within(draftRow).getByText('Not run yet')).toBeInTheDocument();
    expect(
      within(draftRow).getByRole('link', { name: 'Workflow ID wf_cccc3333dddd4444' }),
    ).toHaveAttribute('href', '/workflows/wf_cccc3333dddd4444');
    expect(within(draftRow).getByRole('button', { name: 'Copy workflow ID' })).toBeInTheDocument();
    // Recently updated first.
    expect(rowNames()).toEqual(['Image maker', 'Release checker', 'Draft writer']);
  });

  it('a click on the row opens the Editor; a click on a control keeps its own target', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    const cell = (await screen.findByText('Checks a release before it ships')).closest('td')!;
    await user.click(cell);
    expect(probe().path).toBe('/workflows/wf_aaaa1111bbbb2222');
  });
});

describe('WorkflowsPage tabs, search and sort', () => {
  it('tab counts follow the search and the tab filters the rows', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByRole('tab', { name: 'All 3' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByRole('tab', { name: 'Last run failed 1' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Not run yet 1' })).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Last run failed 1' }));
    expect(rowNames()).toEqual(['Image maker']);
    expect(probe().search).toBe('?tab=failed');

    await user.click(screen.getByRole('tab', { name: /^All/ }));
    await user.type(
      screen.getByRole('searchbox', { name: 'Search workflows by name or ID' }),
      'release',
    );
    expect(screen.getByRole('tab', { name: 'All 1' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Last run failed 0' })).toBeInTheDocument();
    expect(rowNames()).toEqual(['Release checker']);
    const marks = screen.getAllByText((_, el) => el?.tagName === 'MARK');
    expect(marks.map((m) => m.textContent)).toEqual(['Release', 'release']);
    expect(probe().search).toBe('?q=release');
  });

  it('finds a workflow by its ID and highlights it in the ID chip', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    await user.type(await screen.findByRole('searchbox'), 'wf_cccc33');
    expect(rowNames()).toEqual(['Draft writer']);
    const idLink = screen.getByRole('link', { name: 'Workflow ID wf_cccc3333dddd4444' });
    expect(within(idLink).getByText('wf_cccc33').tagName).toBe('MARK');
  });

  it('Escape clears the search and / focuses it', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage('/workflows?q=draft');

    const search = await screen.findByRole('searchbox');
    expect(search).toHaveValue('draft');
    await user.click(search);
    await user.keyboard('{Escape}');
    expect(search).toHaveValue('');
    expect(rowNames()).toHaveLength(3);

    search.blur();
    await user.keyboard('/');
    expect(search).toHaveFocus();
  });

  it('sorts by name', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Sort: Recently updated' }));
    await user.click(screen.getByRole('option', { name: 'Name A–Z' }));
    expect(rowNames()).toEqual(['Draft writer', 'Image maker', 'Release checker']);
    expect(probe().search).toBe('?sort=name');
  });

  it('restores tab, search and sort from the URL', async () => {
    stubFetch(listOf(ITEMS));
    renderPage('/workflows?tab=failed&q=maker&sort=name');

    expect(await screen.findByRole('tab', { name: 'Last run failed 1' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByRole('searchbox')).toHaveValue('maker');
    expect(screen.getByRole('button', { name: 'Sort: Name A–Z' })).toBeInTheDocument();
    expect(rowNames()).toEqual(['Image maker']);
  });

  it('says when nothing matches and clears the search', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage('/workflows?q=zzzz');

    expect(await screen.findByText('No workflows match “zzzz”.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Clear search' }));
    expect(screen.getByRole('searchbox')).toHaveValue('');
    expect(screen.getByRole('searchbox')).toHaveFocus();
    expect(rowNames()).toHaveLength(3);
  });

  it('names the tab when the search finds nothing in it', async () => {
    stubFetch(listOf(ITEMS));
    renderPage('/workflows?tab=never&q=release');
    expect(await screen.findByText('No workflows match “release” in this tab.')).toBeVisible();
  });

  it('an empty tab offers all workflows', async () => {
    stubFetch(listOf(ITEMS.filter((i) => i.lastRun)));
    const user = userEvent.setup();
    renderPage('/workflows?tab=never');

    expect(await screen.findByText('Every workflow has run at least once.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Show all workflows' }));
    await waitFor(() => {
      expect(screen.getByRole('tab', { name: /^All/ })).toHaveFocus();
    });
    expect(rowNames()).toHaveLength(2);
  });

  it('shows the failure reason of the last run when it is known', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    const failed = await screen.findByRole('link', { name: /^Last run failed/ });
    await user.hover(failed);
    expect(
      await screen.findByRole('tooltip', {
        name: 'The agent ran out of time. It stopped at Painter.',
      }),
    ).toBeInTheDocument();
  });
});

describe('WorkflowsPage paging', () => {
  it('pages the list client-side when there are more than 20 rows', async () => {
    const many = Array.from({ length: 25 }, (_, i) =>
      item(
        `wf_${String(i).padStart(4, '0')}abcdabcd`,
        `Workflow ${String(i).padStart(2, '0')}`,
        `2026-08-${String(28 - i).padStart(2, '0')}T00:00:00Z`,
        null,
      ),
    );
    stubFetch(listOf(many));
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText('1–20 of 25 workflows')).toBeInTheDocument();
    expect(rowNames()).toHaveLength(20);
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('21–25 of 25 workflows')).toBeInTheDocument();
    expect(rowNames()).toEqual([
      'Workflow 20',
      'Workflow 21',
      'Workflow 22',
      'Workflow 23',
      'Workflow 24',
    ]);
    expect(probe().search).toBe('?page=2');
  });

  it('shows no pager for 20 rows or fewer', async () => {
    stubFetch(listOf(ITEMS));
    renderPage();
    await screen.findByText('Release checker');
    expect(screen.queryByRole('navigation', { name: 'Pages' })).not.toBeInTheDocument();
  });
});

describe('WorkflowsPage row menu and Run dialog', () => {
  it('lists Run, Edit and Runs', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Actions for Release checker' }));
    const menu = screen.getByRole('menu');
    expect(
      within(menu)
        .getAllByRole('menuitem')
        .map((m) => m.textContent),
    ).toEqual(['Run', 'Edit', 'Runs']);
    expect(within(menu).getByRole('menuitem', { name: 'Edit' })).toHaveAttribute(
      'href',
      '/workflows/wf_aaaa1111bbbb2222',
    );
    expect(within(menu).getByRole('menuitem', { name: 'Runs' })).toHaveAttribute(
      'href',
      '/workflows/wf_aaaa1111bbbb2222/runs',
    );
  });

  it('Run opens the Run dialog on the latest version and starts the run', async () => {
    const fetchMock = stubFetch({
      ...listOf(ITEMS),
      '/api/definitions/wf_aaaa1111bbbb2222': () => jsonResponse(200, DEFINITION),
      '/api/runs/run_1111aaaa2222': () =>
        jsonResponse(200, {
          run: { id: 'run_1111aaaa2222', input: {}, startedAt: '2026-08-03T12:10:00Z' },
          nodeRuns: [],
          lastSeq: 0,
        }),
      '/api/runs': () => jsonResponse(201, { id: 'run_new0000new0', status: 'RUNNING' }),
    });
    const user = userEvent.setup();
    renderPage();

    const kebab = await screen.findByRole('button', { name: 'Actions for Release checker' });
    await user.click(kebab);
    await user.click(screen.getByRole('menuitem', { name: 'Run' }));

    const dialog = await screen.findByRole('dialog', { name: 'Release checker' });
    expect(await within(dialog).findByText('Workflow version 2')).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/definitions/wf_aaaa1111bbbb2222',
      expect.anything(),
    );
    await user.click(within(dialog).getByRole('button', { name: 'Start run' }));

    await waitFor(() => {
      expect(probe().path).toBe('/workflows/wf_aaaa1111bbbb2222/runs/run_new0000new0');
    });
    const post = fetchMock.mock.calls.find(
      ([url, init]) => url === '/api/runs' && init?.method === 'POST',
    );
    expect(JSON.parse(post?.[1]?.body as string)).toEqual({
      workflowId: 'wf_aaaa1111bbbb2222',
      definitionVersion: 2,
      input: {},
    });
  });

  it('returns focus to the row’s menu button when the Run dialog closes', async () => {
    stubFetch({
      ...listOf(ITEMS),
      '/api/definitions/wf_cccc3333dddd4444': () =>
        jsonResponse(200, {
          ...DEFINITION,
          workflowId: 'wf_cccc3333dddd4444',
          name: 'Draft writer',
        }),
    });
    const user = userEvent.setup();
    renderPage();

    const kebab = await screen.findByRole('button', { name: 'Actions for Draft writer' });
    await user.click(kebab);
    await user.click(screen.getByRole('menuitem', { name: 'Run' }));
    const dialog = await screen.findByRole('dialog', { name: 'Draft writer' });
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => {
      expect(kebab).toHaveFocus();
    });
  });

  it('says so when the workflow can’t be loaded for the run', async () => {
    stubFetch({
      ...listOf(ITEMS),
      '/api/definitions/wf_cccc3333dddd4444': () =>
        jsonResponse(503, { error: { code: 'DEPENDENCY_UNAVAILABLE', message: 'down' } }),
    });
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Actions for Draft writer' }));
    await user.click(screen.getByRole('menuitem', { name: 'Run' }));
    const dialog = await screen.findByRole('dialog', { name: 'Draft writer' });
    expect(await within(dialog).findByText('Couldn’t load the run form.')).toBeInTheDocument();
  });
});

describe('WorkflowsPage New workflow', () => {
  it('validates the name on submit and opens a draft with router state', async () => {
    const fetchMock = stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New workflow' }));
    const dialog = screen.getByRole('dialog', { name: 'New workflow' });
    const create = within(dialog).getByRole('button', { name: 'Create' });
    expect(create).toBeEnabled();

    const name = within(dialog).getByRole('textbox', { name: 'Name' });
    await user.type(name, '   ');
    await user.click(create);
    expect(within(dialog).getByText('Enter a name.')).toBeInTheDocument();
    expect(name).toHaveAttribute('aria-invalid', 'true');
    expect(name).toHaveFocus();

    await user.type(name, 'Release checker ');
    expect(within(dialog).queryByText('Enter a name.')).not.toBeInTheDocument();
    await user.type(
      within(dialog).getByRole('textbox', { name: /Description/ }),
      ' Checks things ',
    );
    await user.click(create);

    expect(dialog).not.toBeInTheDocument();
    expect(probe()).toMatchObject({
      path: '/workflows/new',
      state: { name: 'Release checker', description: 'Checks things' },
    });
    // Only the list was fetched: the first Save in the Editor creates version 1.
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('closing the dialog resets the form', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New workflow' }));
    let dialog = screen.getByRole('dialog', { name: 'New workflow' });
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));
    await user.type(within(dialog).getByRole('textbox', { name: 'Name' }), 'x');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await user.click(screen.getByRole('button', { name: 'New workflow' }));
    dialog = screen.getByRole('dialog', { name: 'New workflow' });
    expect(within(dialog).getByRole('textbox', { name: 'Name' })).toHaveValue('');
    expect(within(dialog).queryByText('Enter a name.')).not.toBeInTheDocument();
  });

  it('⌘↵ submits', async () => {
    stubFetch(listOf(ITEMS));
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New workflow' }));
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'Quick');
    await user.keyboard('{Meta>}{Enter}{/Meta}');
    expect(probe()).toMatchObject({ path: '/workflows/new', state: { name: 'Quick' } });
  });
});
