import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import type { DetailContext } from './detail-context';
import { StepOutput } from './RunSummary';
import { MODELS, NODE_TYPES, TOOLS } from '@/test/registry-fixture';
import { definition, model, node, nodeRun, snapshot } from '@/test/run-fixtures';

describe('a step whose output equals its input', () => {
  it('still shows the value, with the pass-through as a note under it', () => {
    const m = model({
      definition: definition([node('out', 'text_output', 'Answer')], []),
      snapshot: snapshot('COMPLETED', [
        nodeRun('out', 'text_output', {
          input: { text: 'A short answer.' },
          output: { text: 'A short answer.' },
        }),
      ]),
    });
    const ctx: DetailContext = {
      model: m,
      failure: null,
      wait: null,
      tools: TOOLS,
      models: MODELS,
      nodeTypes: NODE_TYPES,
      workflowId: 'wf_1',
      latestVersion: 1,
      selectedId: 'out',
      onSelect: () => undefined,
    };
    const step = m.byNode.get('out');
    if (!step) throw new Error('no step');
    render(
      <MemoryRouter>
        <StepOutput step={step} ctx={ctx} status={false} />
      </MemoryRouter>,
    );
    expect(screen.getByText('A short answer.')).toBeInTheDocument();
    expect(screen.getByText('Same as the input')).toBeInTheDocument();
  });
});
