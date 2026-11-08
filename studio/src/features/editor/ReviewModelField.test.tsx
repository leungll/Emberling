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
  it('offers every registered model, defaults to the mock policy and can be cleared', async () => {
    const user = userEvent.setup();
    renderAgent({ allowedTools: ['review_asset'] });

    const trigger = await screen.findByRole('button', { name: 'Review model: Mock review policy' });
    expect(
      screen.getByText(
        'Used when the agent calls review_asset. Saving checks that it supports what each tool needs. Without one, reviews use the mock policy.',
      ),
    ).toBeInTheDocument();
    await user.click(trigger);
    const names = screen.getAllByRole('option').map((o) => o.textContent);
    expect(names).toEqual([
      'Mock review policy',
      'Mock Image Model v1',
      'Vision Review Model',
      'Mock Text Model v1',
    ]);

    await user.click(screen.getByRole('option', { name: 'Vision Review Model' }));
    expect(
      await screen.findByRole('button', { name: 'Review model: Vision Review Model' }),
    ).toBeInTheDocument();
    expect(useEditorStore.getState().doc.nodes[0]?.config.reviewModelId).toBe('vision-model-v1');

    await user.click(screen.getByRole('button', { name: 'Review model: Vision Review Model' }));
    await user.click(screen.getByRole('option', { name: 'Mock review policy' }));
    expect(useEditorStore.getState().doc.nodes[0]?.config.reviewModelId).toBeUndefined();
  });

  it('says a patch review needs its own model instead of falling back to the mock policy', async () => {
    const reviewAsset = TOOLS.find((x) => x.name === 'review_asset')!;
    api.listTools.mockResolvedValue([
      ...TOOLS,
      {
        ...reviewAsset,
        name: 'review_patch',
        description: 'Review the current workspace patch',
        reviewModelCapability: 'structured_decision',
        requiresReviewModel: true,
      },
    ]);
    renderAgent({ allowedTools: ['review_patch'] });

    expect(
      await screen.findByText("Used for review_patch. Must differ from the agent's model."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/mock policy\.$/)).toBeNull();
  });

  it('is not offered to an agent without a review tool', async () => {
    renderAgent({ allowedTools: ['lookup'] });
    expect(await screen.findByRole('button', { name: /^Model: / })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Review model/ })).toBeNull();
  });
});
