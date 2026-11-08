import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Definition, Node } from '@/api/types';
import { MODELS, NODE_TYPES, step, TOOLS } from '@/test/registry-fixture';
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

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...api,
}));

// Property order as a saved Definition returns it; `required` keeps the author's order.
const TASK = step(
  'task',
  'task_input',
  0,
  0,
  {
    inputKey: 'task',
    label: 'Editing Task',
    required: true,
    schema: {
      properties: {
        goal: {
          type: 'string',
          enum: ['DELIVER_CLIP'],
          title: 'Goal',
          description: 'What counts as done.',
        },
        sourceId: { type: 'string', minLength: 1, title: 'Source video' },
        durationMs: { type: 'integer', minimum: 0, title: 'Source duration (ms)' },
        requirement: { type: 'string', minLength: 1, maxLength: 2000, title: 'Requirement' },
        sourceSha256: { type: 'string', minLength: 64, maxLength: 64, title: 'SHA-256' },
      },
      required: ['requirement', 'sourceId', 'sourceSha256', 'durationMs', 'goal'],
    },
  },
  'Editing Task',
);

function workflow(nodes: Node[]): Definition {
  return {
    workflowId: 'wf_inputs',
    version: 1,
    name: 'Inputs',
    description: '',
    nodes,
    edges: [],
    runInputSchema: { type: 'object', properties: {} },
    validation: { status: 'VALID', validatorVersion: 'v1', validatedAt: '2026-11-07T12:00:00Z' },
    createdAt: '2026-11-07T00:00:00Z',
  };
}

function renderStep(nodes: Node[], select: string) {
  api.getDefinition.mockResolvedValue(workflow(nodes));
  const router = createMemoryRouter([{ path: '/workflows/:workflowId', element: <EditPage /> }], {
    initialEntries: [`/workflows/wf_inputs?select=${select}`],
  });
  render(<RouterProvider router={router} />);
}

beforeEach(() => {
  api.listNodeTypes.mockResolvedValue(NODE_TYPES);
  api.listModels.mockResolvedValue(MODELS);
  api.listTools.mockResolvedValue(TOOLS);
  api.listDefinitions.mockResolvedValue([]);
  useEditorStore.setState({ source: null });
  sessionStorage.clear();
  localStorage.clear();
});

afterEach(() => {
  vi.clearAllMocks();
});

const section = (name: string) => screen.getByRole('region', { name });

describe('Task input settings', () => {
  it('lists every field a run needs, required ones first, with its type and description', async () => {
    renderStep([TASK], 'task');
    const needs = await screen.findByRole('region', { name: 'What a run needs' });
    const rows = within(needs).getAllByRole('listitem');
    expect(rows.map((r) => r.querySelector('[data-field-label]')?.textContent)).toEqual([
      'Requirement',
      'Source video',
      'SHA-256',
      'Source duration (ms)',
      'Goal',
    ]);
    expect(rows[0]).toHaveTextContent('Required');
    expect(rows[0]).toHaveTextContent('Text, up to 2,000 characters');
    expect(rows[2]).toHaveTextContent('Text, exactly 64 characters');
    expect(rows[3]).toHaveTextContent('Whole number, 0 or more');
    expect(rows[4]).toHaveTextContent('One of: DELIVER_CLIP');
    expect(rows[4]).toHaveTextContent('What counts as done.');
    expect(within(needs).getByRole('button', { name: /Edit/ })).toBeInTheDocument();
    expect(within(needs).queryByText(/and \d+ more/)).toBeNull();
  });

  it('previews the run form with the same controls the Run dialog shows', async () => {
    renderStep([TASK], 'task');
    const preview = await screen.findByRole('region', { name: 'In the run form' });
    const inputs = preview.querySelectorAll('input, textarea, button');
    expect(inputs.length).toBe(5);
    expect(preview).toHaveTextContent('DELIVER_CLIP');
    for (const el of inputs) expect(el).toBeDisabled();
  });

  it('keeps the label, key and an example from the schema in a closed section', async () => {
    const user = userEvent.setup();
    renderStep([TASK], 'task');
    const toggle = await screen.findByRole('button', { name: /API and form label/ });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('Use the step name as the field label')).toBeNull();
    await user.click(toggle);
    const api = section('API and form label');
    expect(within(api).getByText('Use the step name as the field label')).toBeInTheDocument();
    const example = api.querySelector('[data-input-example]');
    expect(JSON.parse(example?.textContent ?? '')).toEqual({
      task: {
        requirement: '…',
        sourceId: '…',
        sourceSha256: '…',
        durationMs: 0,
        goal: 'DELIVER_CLIP',
      },
    });
  });
});

describe('Task input card', () => {
  it('names every field on the canvas card, never “and more”', async () => {
    renderStep([TASK], 'task');
    expect(
      await screen.findByText(
        '5 fields: Requirement, Source video, SHA-256, Source duration (ms), Goal',
      ),
    ).toBeInTheDocument();
  });
});

describe('Text input settings', () => {
  it('previews the same multi-line box the Run dialog shows', async () => {
    renderStep(
      [step('in', 'text_input', 0, 0, { inputKey: 'brief', required: true }, 'Brief')],
      'in',
    );
    const preview = await screen.findByRole('region', { name: 'In the run form' });
    const box = within(preview).getByRole('textbox', { hidden: true });
    expect(box.tagName).toBe('TEXTAREA');
    expect(box).toBeDisabled();
  });
});
