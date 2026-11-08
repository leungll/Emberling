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
          shown: [],
          media: null,
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
          shown: [],
          media: null,
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
          shown: [],
          media: null,
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
          shown: [],
          media: null,
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
        saved={{
          facts: [],
          result: null,
          review: { passed: true },
          videoUrl: null,
          withheld: [],
          shown: [],
          media: null,
        }}
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
          shown: [],
          media: null,
        }}
      />,
    );

    expect(screen.getByText('The review’s reason list is too long to show.')).toBeInTheDocument();
  });
});

describe('the members a Tool declares for its run card', () => {
  const media = {
    items: [
      {
        ref: 'https://media.example/v1/clips/c1.mp4',
        url: 'https://media.example/v1/clips/c1.mp4',
        mediaType: 'video/mp4',
        toolAttemptId: 'ta_1',
      },
    ],
    truncated: false,
  };

  it('shows each declared argument under its title before any named view', () => {
    wrap(
      <CallArgumentsView
        ctx={ctx()}
        args={{
          photo: null,
          settings: { prompt: 'Clay look', aspectRatio: '3:4' },
          target: null,
          withheld: [],
          shown: [
            { title: 'Brief', source: 'ARGUMENTS', pointer: '/brief', value: 'Warm and quiet' },
            { title: 'Length', source: 'ARGUMENTS', pointer: '/seconds', value: 12 },
          ],
          media: null,
        }}
      />,
    );
    expect(screen.getByText('Brief')).toBeInTheDocument();
    expect(screen.getByText('Warm and quiet')).toBeInTheDocument();
    expect(screen.getByText('Length')).toBeInTheDocument();
    expect(screen.getByText('12')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Settings' })).not.toBeInTheDocument();
  });

  it('reads a withheld member as a plain fact', () => {
    wrap(
      <CallResultView
        ctx={ctx()}
        saved={{
          facts: [],
          result: null,
          review: null,
          videoUrl: null,
          withheld: [],
          shown: [
            { title: 'Log', source: 'RESULT', pointer: '/log', value: null, withheld: 'TOO_LARGE' },
          ],
          media: null,
        }}
      />,
    );
    expect(screen.getByText('Log')).toBeInTheDocument();
    expect(screen.getByText('Not shown: too long to show.')).toBeInTheDocument();
  });

  it('plays a result member only through the trace media that serves it', () => {
    const served = 'https://media.example/v1/clips/c1.mp4';
    const { container, unmount } = wrap(
      <CallResultView
        ctx={ctx()}
        saved={{
          facts: [],
          result: null,
          review: null,
          videoUrl: null,
          withheld: [],
          shown: [{ title: 'Clip', source: 'RESULT', pointer: '/url', value: served }],
          media,
        }}
      />,
    );
    expect(container.querySelector('video')?.getAttribute('src')).toBe(served);
    unmount();

    const other = 'https://media.example/v1/clips/c2.mp4';
    const second = wrap(
      <CallResultView
        ctx={ctx()}
        saved={{
          facts: [],
          result: null,
          review: null,
          videoUrl: null,
          withheld: [],
          shown: [{ title: 'Clip', source: 'RESULT', pointer: '/url', value: other }],
          media,
        }}
      />,
    );
    expect(second.container.querySelector('video')).toBeNull();
    expect(screen.getByText(other)).toBeInTheDocument();
  });
});
