import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { WorkflowAbout } from './ElementOverview';
import type { DetailContext } from './detail-context';
import { MODELS, NODE_TYPES, TOOLS } from '@/test/registry-fixture';
import { definition, model, node, nodeRun, snapshot } from '@/test/run-fixtures';

/** The Definition version the run is bound to. */
const BOUND = definition(
  [
    node('in', 'text_input', 'Brief', { position: { x: 0, y: 0 }, config: { label: 'Brief' } }),
    node('out', 'text_output', 'Answer', { position: { x: 300, y: 0 } }),
  ],
  [['in', 'out']],
);

function ctx(latestVersion: number | null): DetailContext {
  const m = model({
    definition: { ...BOUND, version: 2, description: 'Turns a brief into an answer.' },
    snapshot: snapshot('COMPLETED', [nodeRun('in', 'text_input'), nodeRun('out', 'text_output')], {
      definitionVersion: 2,
    }),
  });
  return {
    model: m,
    failure: null,
    wait: null,
    tools: TOOLS,
    models: MODELS,
    nodeTypes: NODE_TYPES,
    workflowId: 'wf_1',
    latestVersion,
    selectedId: m.root.id,
    onSelect: () => undefined,
  };
}

function renderAbout(latestVersion: number | null) {
  return render(
    <MemoryRouter>
      <WorkflowAbout ctx={ctx(latestVersion)} />
    </MemoryRouter>,
  );
}

describe('the run overview’s About this workflow', () => {
  it('summarises the bound version with the shared workflow summary', () => {
    renderAbout(2);

    expect(screen.getByRole('heading', { name: 'About this workflow' })).toBeInTheDocument();
    expect(screen.getByText('Turns a brief into an answer.')).toBeInTheDocument();
    // Numbered steps in reading order, each with its sentence.
    const steps = screen.getAllByRole('listitem').map((li) => li.textContent);
    expect(steps[0]).toMatch(/^1Brief/);
    expect(steps[0]).toContain('Asks for “Brief” when a run starts.');
    expect(steps[1]).toMatch(/^2Answer/);
    expect(screen.queryByText(/This run used version/)).not.toBeInTheDocument();
  });

  it('notes an older version and links to the latest one in the Editor', () => {
    renderAbout(5);

    expect(
      screen.getByText(/This run used version 2\. The workflow has changed since\./),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('link', { name: 'Open the latest version in the Editor' }),
    ).toHaveAttribute('href', '/workflows/wf_1');
  });

  it('shows no note while the latest version is still unknown', () => {
    renderAbout(null);

    expect(screen.queryByText(/This run used version/)).not.toBeInTheDocument();
  });
});
