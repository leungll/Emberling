import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { flushSync } from 'react-dom';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { describe, expect, it } from 'vitest';

import { RunView } from './RunView';
import type { RunData } from './useRunData';
import { MODELS, NODE_TYPES, TOOLS } from '@/test/registry-fixture';
import { definition, model, node, nodeRun, snapshot } from '@/test/run-fixtures';

const FLOW = definition(
  [node('in', 'text_input', 'Brief'), node('out', 'text_output', 'Answer')],
  [['in', 'out']],
);

function data(): RunData {
  const snap = snapshot(
    'COMPLETED',
    [nodeRun('in', 'text_input'), nodeRun('out', 'text_output', { output: { text: 'Done' } })],
    { output: { text: 'Done' } },
  );
  return {
    observed: { snapshot: snap, events: [], loadError: null, connection: 'stopped' },
    definition: FLOW,
    definitionFailed: false,
    model: model({ snapshot: snap, definition: FLOW }),
    ready: true,
    reconnect: () => undefined,
  };
}

describe('the run view’s details tabs', () => {
  it('shows the chosen tab in the same commit that writes it to the address', async () => {
    const router = createMemoryRouter(
      [
        {
          path: '/workflows/:workflowId/runs/:runId',
          element: (
            <RunView
              runId="run_1"
              workflowId="wf_1"
              latestVersion={1}
              registry={{ nodeTypes: NODE_TYPES, tools: TOOLS, models: MODELS }}
              data={data()}
              onRetry={() => undefined}
              onRunAgain={() => undefined}
            />
          ),
        },
      ],
      { initialEntries: ['/workflows/wf_1/runs/run_1'] },
    );
    // Studio's RouterProvider from react-router/dom passes React DOM's flushSync the same way.
    render(
      <RouterProvider
        router={router}
        flushSync={(fn) => {
          flushSync(fn);
        }}
      />,
    );
    const output = await screen.findByRole('tab', { name: 'Output' });

    // What the panel shows at each moment the address changes. A tab rendered only in a
    // later transition leaves a window where the address and the panel disagree.
    const seen: string[] = [];
    const unsubscribe = router.subscribe((state) => {
      const selected = screen.getByRole('tab', { selected: true }).textContent;
      seen.push(`${state.location.search} ${selected}`);
    });
    fireEvent.mouseDown(output);
    await waitFor(() => {
      expect(router.state.location.search).toBe('?tab=output');
    });
    unsubscribe();

    expect(seen).not.toHaveLength(0);
    expect(seen.filter((s) => s !== '?tab=output Output')).toEqual([]);
    expect(screen.getByRole('tabpanel')).toHaveTextContent('Done');
  });
});
