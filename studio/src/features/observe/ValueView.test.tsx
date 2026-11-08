import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import type { DetailContext } from './detail-context';
import { ValueView } from './ValueView';
import { MediaSourceContext, mediaOfRun } from '@/components/media/media-source';
import { MODELS, NODE_TYPES, TOOLS } from '@/test/registry-fixture';
import { definition, model, node, nodeRun, snapshot } from '@/test/run-fixtures';

function ctx(): DetailContext {
  const m = model({
    definition: definition([node('task', 'task_input', 'Task')], []),
    snapshot: snapshot('COMPLETED', [nodeRun('task', 'task_input')]),
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
    selectedId: 'task',
    onSelect: () => undefined,
  };
}

const served = mediaOfRun(
  {
    items: [
      {
        ref: 'src_7f3a',
        url: 'https://media.example/files/src_7f3a.webm',
        mediaType: 'video/webm',
        nodeRunId: 'nr_task',
      },
    ],
    truncated: false,
  },
  [],
);

describe('ValueView', () => {
  it('plays a nested input value the run’s media list serves, with the JSON one click away', () => {
    const value = { task: { sourceId: 'src_7f3a', brief: 'Cut the opening.' } };
    const { container } = render(
      <MemoryRouter>
        <MediaSourceContext.Provider value={{ media: served, runInput: value }}>
          <ValueView ctx={ctx()} value={value} root="input" storageKey="t" />
        </MediaSourceContext.Provider>
      </MemoryRouter>,
    );
    expect(screen.getByRole('button', { name: /^Play Source /i })).toBeInTheDocument();
    expect(container.querySelector('video')?.getAttribute('src')).toBe(
      'https://media.example/files/src_7f3a.webm#t=1',
    );
    expect(screen.getByText('JSON')).toBeInTheDocument();
  });

  it('keeps the JSON viewer alone for a value nothing serves', () => {
    const value = { task: { sourceId: 'src_other' } };
    const { container } = render(
      <MemoryRouter>
        <MediaSourceContext.Provider value={{ media: served, runInput: value }}>
          <ValueView ctx={ctx()} value={value} root="input" storageKey="t" />
        </MediaSourceContext.Provider>
      </MemoryRouter>,
    );
    expect(container.querySelector('video')).toBeNull();
  });
});
