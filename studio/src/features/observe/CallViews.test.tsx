import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { CallArgumentsView, CallResultView } from './ValueView';
import type { DetailContext } from './detail-context';
import { MODELS, NODE_TYPES, TOOLS } from '@/test/registry-fixture';
import { definition, model, node, nodeRun, snapshot } from '@/test/run-fixtures';

function ctx(): DetailContext {
  const m = model({
    definition: definition([node('agent', 'agent', 'Agent', { position: { x: 0, y: 0 } })], []),
    snapshot: snapshot('COMPLETED', [nodeRun('agent', 'agent')]),
  });
  return {
    model: m,
    failure: null,
    wait: null,
    tools: TOOLS,
    models: MODELS,
    nodeTypes: NODE_TYPES,
    workflowId: 'wf_1',
    latestVersion: 1,
    selectedId: m.root.id,
    onSelect: () => undefined,
  };
}

const wrap = (ui: React.ReactNode) => render(<MemoryRouter>{ui}</MemoryRouter>);

describe('a generate_image call’s settings in its Arguments tab', () => {
  it('lists the prompt, aspect ratio and resolution', () => {
    wrap(
      <CallArgumentsView
        ctx={ctx()}
        args={{
          photo: null,
          settings: { prompt: 'Clay look, keep the face', aspectRatio: '3:4', resolution: '1K' },
          target: null,
          withheld: [],
        }}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Settings' })).toBeInTheDocument();
    expect(screen.getByText('Clay look, keep the face')).toBeInTheDocument();
    expect(screen.getByText('3:4')).toBeInTheDocument();
    expect(screen.getByText('1K')).toBeInTheDocument();
    expect(screen.queryByText('Shortened to its first 500 bytes.')).not.toBeInTheDocument();
  });

  it('marks a cut prompt and leaves out members the call did not set', () => {
    wrap(
      <CallArgumentsView
        ctx={ctx()}
        args={{
          photo: null,
          settings: { prompt: 'Clay look', promptTruncated: true },
          target: null,
          withheld: [],
        }}
      />,
    );

    expect(screen.getByText('Shortened to its first 500 bytes.')).toBeInTheDocument();
    expect(screen.queryByText('Aspect ratio')).not.toBeInTheDocument();
    expect(screen.queryByText('Resolution')).not.toBeInTheDocument();
  });

  it('says why a withheld prompt is missing', () => {
    wrap(
      <CallArgumentsView
        ctx={ctx()}
        args={{
          photo: null,
          settings: { resolution: '1K' },
          target: null,
          withheld: [{ field: 'settings.prompt', reason: 'TOO_LARGE' }],
        }}
      />,
    );

    expect(screen.getByText('The prompt is too long to show.')).toBeInTheDocument();
  });
});

describe('a review_asset call’s verdict in its Result tab', () => {
  it('shows a failed verdict with its reasons', () => {
    wrap(
      <CallResultView
        ctx={ctx()}
        saved={{
          facts: [],
          result: null,
          review: { passed: false, reasons: ['Face lost its likeness', 'Background is flat'] },
          videoUrl: null,
          withheld: [],
        }}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Review' })).toBeInTheDocument();
    expect(screen.getByText('Did not pass')).toBeInTheDocument();
    expect(screen.getByText('Reasons')).toBeInTheDocument();
    expect(screen.getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Face lost its likeness',
      'Background is flat',
    ]);
  });

  it('shows a pass without a reasons list when the review gave none', () => {
    wrap(
      <CallResultView
        ctx={ctx()}
        saved={{ facts: [], result: null, review: { passed: true }, videoUrl: null, withheld: [] }}
      />,
    );

    expect(screen.getByText('Passed')).toBeInTheDocument();
    expect(screen.queryByText('Reasons')).not.toBeInTheDocument();
  });

  it('says why withheld reasons are missing', () => {
    wrap(
      <CallResultView
        ctx={ctx()}
        saved={{
          facts: [],
          result: null,
          review: { passed: false },
          videoUrl: null,
          withheld: [{ field: 'review.reasons', reason: 'TOO_LARGE' }],
        }}
      />,
    );

    expect(screen.getByText('The review’s reason list is too long to show.')).toBeInTheDocument();
  });
});
