import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiRequestError } from '@/api/client';
import type { Definition, Node } from '@/api/types';
import { link, MODELS, NODE_TYPES, step, TOOLS } from '@/test/registry-fixture';
import { nodeRun, snapshot } from '@/test/run-fixtures';
import { EditPage } from './EditPage';
import { useEditorStore } from './editor-store';

const api = vi.hoisted(() => ({
  getDefinition: vi.fn(),
  getRun: vi.fn(),
  saveDefinition: vi.fn(),
  createDefinition: vi.fn(),
  listDefinitions: vi.fn(),
  listNodeTypes: vi.fn(),
  listModels: vi.fn(),
  listTools: vi.fn(),
}));
const toast = vi.hoisted(() => vi.fn());

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...api,
}));
vi.mock('@/lib/toast', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  showToast: toast,
}));

const INPUT = step('in', 'text_input', 0, 0, { inputKey: 'brief', required: true }, 'Brief');
const OUTPUT = step('out', 'text_output', 320, 0, {}, 'Answer');

function definition(nodes: Node[] = [INPUT, OUTPUT], version = 4): Definition {
  return {
    workflowId: 'wf_123',
    version,
    name: 'Answer a brief',
    description: '',
    nodes,
    edges: nodes.length === 2 ? [link('e1', 'in', 'text', 'out', 'text')] : [],
    runInputSchema: {
      type: 'object',
      properties: { brief: { type: 'string', title: 'Brief' } },
      required: ['brief'],
    },
    validation: { status: 'VALID', validatorVersion: 'v1', validatedAt: '2026-08-03T12:00:00Z' },
    createdAt: '2026-08-01T00:00:00Z',
  };
}

function apiError(status: number, code: string): ApiRequestError {
  return new ApiRequestError(status, { error: { code, message: code.toLowerCase() } });
}

/** Shows where the router is, since a memory router never touches window.location. */
function LocationProbe() {
  const { pathname, search } = useLocation();
  return <div data-testid="location">{pathname + search}</div>;
}

function renderAt(path: string) {
  const page = (
    <>
      <EditPage />
      <LocationProbe />
    </>
  );
  const router = createMemoryRouter(
    [
      { path: '/workflows', element: <LocationProbe /> },
      { path: '/workflows/new', element: page },
      { path: '/workflows/:workflowId', element: page },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

const location = () => screen.getByTestId('location').textContent;

async function editName(value: string) {
  const user = userEvent.setup();
  const field = await screen.findByRole('textbox', { name: 'Workflow name' });
  await user.clear(field);
  await user.type(field, value);
  return user;
}

beforeEach(() => {
  api.listNodeTypes.mockResolvedValue(NODE_TYPES);
  api.listModels.mockResolvedValue(MODELS);
  api.listTools.mockResolvedValue(TOOLS);
  api.listDefinitions.mockResolvedValue([]);
  api.getDefinition.mockResolvedValue(definition());
  // The draft store outlives a render; a fresh page loads its workflow again.
  useEditorStore.setState({ source: null });
  sessionStorage.clear();
  localStorage.clear();
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('EditPage', () => {
  it('opens the Run dialog once for ?run=1 and drops the parameter', async () => {
    renderAt('/workflows/wf_123?run=1');

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('Answer a brief');
    await waitFor(() => expect(location()).toBe('/workflows/wf_123'));
  });

  it('names the tab after the workflow', async () => {
    renderAt('/workflows/wf_123');

    await waitFor(() => expect(document.title).toBe('Answer a brief – Editor – Emberling'));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('checks the loaded workflow at once rather than an empty draft', async () => {
    renderAt('/workflows/wf_123');

    await screen.findByRole('textbox', { name: 'Workflow name' });
    await waitFor(() => expect(screen.getAllByText('Answer').length).toBeGreaterThan(0));
    expect(screen.queryByRole('button', { name: /^\d+ issues?$/ })).toBeNull();
  });

  it('shows the not-found page for a workflow that does not exist', async () => {
    api.getDefinition.mockRejectedValue(apiError(404, 'NOT_FOUND'));
    renderAt('/workflows/wf_123');

    expect(await screen.findByRole('heading', { level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Workflow name' })).not.toBeInTheDocument();
  });

  it('creates the workflow on its first save and moves to its address', async () => {
    api.createDefinition.mockResolvedValue({ ...definition(), workflowId: 'wf_new', version: 1 });
    // Stands in for steps added on the canvas: an empty workflow has an issue and can't be saved.
    const { nodes, edges } = definition();
    useEditorStore.getState().load({ name: '', description: '', nodes, edges }, 'new');
    const router = renderAt('/workflows/new');

    const user = await editName('Fresh');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(location()).toBe('/workflows/wf_new'));
    expect(api.createDefinition).toHaveBeenCalledWith(expect.objectContaining({ name: 'Fresh' }));
    expect(router.state.historyAction).toBe('REPLACE');
  });

  it('keeps the edits and offers the latest version when another save got there first', async () => {
    api.saveDefinition.mockRejectedValue(apiError(409, 'VERSION_CONFLICT'));
    renderAt('/workflows/wf_123');

    const user = await editName('Renamed');
    api.getDefinition.mockResolvedValue(definition(undefined, 5));
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(toast).toHaveBeenCalled());
    const [text, options] = toast.mock.calls[0] as [string, { action?: { label: string } }];
    expect(text).toContain('5');
    expect(options.action?.label).toBe('Load latest');
    expect(api.saveDefinition).toHaveBeenCalledWith(
      'wf_123',
      expect.objectContaining({ baseVersion: 4, name: 'Renamed' }),
    );
    expect(screen.getByRole('textbox', { name: 'Workflow name' })).toHaveValue('Renamed');
  });

  it('does not save while the checks find issues', async () => {
    // The output step has nothing connected to its required input.
    api.getDefinition.mockResolvedValue(definition([INPUT, OUTPUT, { ...OUTPUT, id: 'out2' }]));
    renderAt('/workflows/wf_123');

    const user = await editName('Renamed');
    await screen.findByRole('button', { name: /^\d+ issues?$/ }, { timeout: 3000 });
    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect(toast).toHaveBeenCalledWith(expect.stringMatching(/^Fix \d+ issues? to save\.$/));
    expect(api.saveDefinition).not.toHaveBeenCalled();
  });
});

describe('EditPage links between the Editor and a run', () => {
  it('opens the step and field that ?select= and &field= name, then drops them', async () => {
    renderAt('/workflows/wf_123?select=in&field=inputKey');

    expect(await screen.findByRole('button', { name: 'Workflow overview' })).toBeInTheDocument();
    await waitFor(() =>
      expect(document.activeElement?.closest<HTMLElement>('[data-field]')?.dataset.field).toBe(
        'inputKey',
      ),
    );
    await waitFor(() => expect(location()).toBe('/workflows/wf_123'));
  });

  it('keeps the overview for a step the workflow no longer has', async () => {
    renderAt('/workflows/wf_123?select=gone');

    await waitFor(() => expect(location()).toBe('/workflows/wf_123'));
    expect(screen.queryByRole('button', { name: 'Workflow overview' })).toBeNull();
  });

  it('opens a step of the last run with the element the run view selects', async () => {
    api.listDefinitions.mockResolvedValue([
      {
        workflowId: 'wf_123',
        name: 'Answer a brief',
        description: '',
        latestVersion: 4,
        updatedAt: '2026-08-03T12:00:00Z',
        lastRun: { id: 'run_9', status: 'COMPLETED', createdAt: '2026-08-03T12:00:00Z' },
      },
    ]);
    api.getRun.mockResolvedValue(
      snapshot('COMPLETED', [nodeRun('in', 'text_input'), nodeRun('out', 'text_output')], {
        id: 'run_9',
        workflowId: 'wf_123',
        definitionVersion: 4,
      }),
    );
    localStorage.setItem('emb.editor.strip', '1');
    renderAt('/workflows/wf_123?from=runs');

    expect(await screen.findByRole('link', { name: 'Open Brief in the run' })).toHaveAttribute(
      'href',
      '/workflows/wf_123/runs/run_9?el=in&from=runs',
    );
  });
});

describe('EditPage leave guard', () => {
  it('stays on Stay and leaves on Leave', async () => {
    const router = renderAt('/workflows/wf_123');
    const user = await editName('Renamed');

    void router.navigate('/workflows');
    let dialog = await screen.findByRole('dialog', { name: 'Leave without saving?' });
    await user.click(within(dialog).getByRole('button', { name: 'Stay' }));
    expect(location()).toBe('/workflows/wf_123');

    void router.navigate('/workflows');
    dialog = await screen.findByRole('dialog', { name: 'Leave without saving?' });
    await user.click(within(dialog).getByRole('button', { name: 'Leave' }));
    await waitFor(() => expect(location()).toBe('/workflows'));
    expect(api.saveDefinition).not.toHaveBeenCalled();
  });

  it('saves and then leaves on Save and leave', async () => {
    api.saveDefinition.mockResolvedValue(definition(undefined, 5));
    const router = renderAt('/workflows/wf_123');
    const user = await editName('Renamed');

    void router.navigate('/workflows');
    const dialog = await screen.findByRole('dialog', { name: 'Leave without saving?' });
    await user.click(within(dialog).getByRole('button', { name: 'Save and leave' }));

    await waitFor(() => expect(location()).toBe('/workflows'));
    expect(api.saveDefinition).toHaveBeenCalledTimes(1);
  });

  it('offers only Leave and Stay when the changes cannot be saved', async () => {
    api.getDefinition.mockResolvedValue(definition([INPUT, OUTPUT, { ...OUTPUT, id: 'out2' }]));
    const router = renderAt('/workflows/wf_123');
    await editName('Renamed');
    await screen.findByRole('button', { name: /^\d+ issues?$/ }, { timeout: 3000 });

    void router.navigate('/workflows');
    const dialog = await screen.findByRole('dialog', { name: 'Leave without saving?' });
    expect(within(dialog).getByRole('button', { name: 'Leave' })).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Stay' })).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Save and leave' })).toBeNull();
  });
});
