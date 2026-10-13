import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { MODELS, NODE_TYPES, link, step } from '@/test/registry-fixture';

import { WorkflowSummary } from './WorkflowSummary';

const registry = {
  nodeTypes: new Map(NODE_TYPES.map((m) => [m.type, m])),
  models: new Map(MODELS.map((m) => [m.id, m])),
};

const definition = {
  description: 'Writes a short story from a topic.',
  nodes: [
    step('in', 'text_input', 0, 0, { inputKey: 'topic', label: 'Topic', required: true }),
    step(
      'ag',
      'agent',
      336,
      0,
      { modelId: 'text-model-v1', allowedTools: ['lookup'], maxTurns: 8, timeoutMs: 120000 },
      'Writer',
    ),
    step('out', 'text_output', 672, 0, {}, 'Story'),
  ],
  edges: [link('1', 'in', 'text', 'ag', 'input'), link('2', 'ag', 'text', 'out', 'text')],
};

describe('WorkflowSummary', () => {
  it('describes each step, what a run needs and what it produces', () => {
    render(<WorkflowSummary definition={definition} registry={registry} />);
    expect(screen.getByText('Writes a short story from a topic.')).toBeInTheDocument();
    expect(screen.getByText('Asks for “Topic” when a run starts.')).toBeInTheDocument();
    expect(
      screen.getByText('Uses Mock Text Model v1 with lookup. Stops after 8 turns or 2 min.'),
    ).toBeInTheDocument();
    expect(
      screen.getByText('Shows the result from Writer as the run’s result.'),
    ).toBeInTheDocument();
    expect(screen.getByText('Text, required')).toBeInTheDocument();
    expect(screen.getAllByText('Story')).toHaveLength(2);
  });

  it('says when a run needs nothing and when nothing is produced yet', () => {
    render(
      <WorkflowSummary
        definition={{ nodes: [step('ag', 'agent', 0, 0)], edges: [] }}
        registry={registry}
        showEmptyOutputs
      />,
    );
    expect(screen.getByText('Nothing. Runs start without input.')).toBeInTheDocument();
    expect(screen.getByText('No output step yet.')).toBeInTheDocument();
    expect(screen.getByText('Uses a model without tools. No limits set.')).toBeInTheDocument();
  });

  it('lets a caller act on a step', async () => {
    const onStepClick = vi.fn();
    render(
      <WorkflowSummary definition={definition} registry={registry} onStepClick={onStepClick} />,
    );
    await userEvent.click(screen.getByRole('button', { name: /^2\s*Writer/ }));
    expect(onStepClick).toHaveBeenCalledWith('ag');
  });
});
