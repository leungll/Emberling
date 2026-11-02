import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Definition, JsonObject } from '@/api/types';
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

function agentWorkflow(config: JsonObject): Definition {
  const agent = step('agent', 'agent', 0, 0, {
    modelId: 'text-model-v1',
    maxTurns: 4,
    timeoutMs: 60000,
    ...config,
  });
  return {
    workflowId: 'wf_review',
    version: 1,
    name: 'Review images',
    description: '',
    nodes: [agent],
    edges: [],
    runInputSchema: { type: 'object', properties: {} },
    validation: { status: 'VALID', validatorVersion: 'v1', validatedAt: '2026-11-02T12:00:00Z' },
    createdAt: '2026-11-02T00:00:00Z',
  };
}

function renderAgent(config: JsonObject) {
  api.getDefinition.mockResolvedValue(agentWorkflow(config));
  const router = createMemoryRouter([{ path: '/workflows/:workflowId', element: <EditPage /> }], {
    initialEntries: ['/workflows/wf_review?select=agent'],
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

describe('Agent review model', () => {
  it('offers only vision models, defaults to the mock policy and can be cleared', async () => {
    const user = userEvent.setup();
    renderAgent({ allowedTools: ['review_asset'] });

    const trigger = await screen.findByRole('button', { name: 'Review model: Mock review policy' });
    expect(
      screen.getByText(
        'Reviews images when the agent calls review_asset. Without one, reviews use the mock policy.',
      ),
    ).toBeInTheDocument();
    await user.click(trigger);
    const names = screen.getAllByRole('option').map((o) => o.textContent);
    expect(names).toEqual(['Mock review policy', 'Vision Review Model']);

    await user.click(screen.getByRole('option', { name: 'Vision Review Model' }));
    expect(
      await screen.findByRole('button', { name: 'Review model: Vision Review Model' }),
    ).toBeInTheDocument();
    expect(useEditorStore.getState().doc.nodes[0]?.config.reviewModelId).toBe('vision-model-v1');

    await user.click(screen.getByRole('button', { name: 'Review model: Vision Review Model' }));
    await user.click(screen.getByRole('option', { name: 'Mock review policy' }));
    expect(useEditorStore.getState().doc.nodes[0]?.config.reviewModelId).toBeUndefined();
  });

  it('is not offered to an agent without a review tool', async () => {
    renderAgent({ allowedTools: ['lookup'] });
    expect(await screen.findByRole('button', { name: /^Model: / })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Review model/ })).toBeNull();
  });
});
