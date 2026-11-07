import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { ElementOverview, WorkflowAbout } from './ElementOverview';
import type { DetailContext } from './detail-context';
import { MODELS, NODE_TYPES, TOOLS } from '@/test/registry-fixture';
import type { ReleaseOccupationTrace } from '@/api/types';
import {
  AGENT_FLOW,
  agentTrace,
  at,
  definition,
  model,
  node,
  nodeRun,
  snapshot,
  toolAttempt,
  turn,
} from '@/test/run-fixtures';

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

/** A Run a person stopped while its release held the target, with the occupation given. */
function stoppedReleaseCtx(occupation: ReleaseOccupationTrace): DetailContext {
  const release = turn(2, 'deploy_change', 5, {
    action: {
      status: 'FAILED',
      authorization: 'ASK',
      release: {
        operations: [
          {
            operationKey: 'op_act_2_1',
            toolAttemptId: 'att_2',
            attemptNo: 1,
            status: 'FAILED',
            outcome: 'UNKNOWN',
            confirmDeadline: at(65),
            outcomeUnknownSince: null,
            voidRequestedAt: null,
          },
        ],
        occupation,
        humanWait: {
          id: 'hw_1',
          status: 'STOPPED',
          openedAt: at(65),
          deadline: at(1865),
          closedAt: at(120),
          closedBy: 'test-operator',
          outcome: 'RELEASE_UNKNOWN',
        },
      },
    },
    attempts: [toolAttempt('att_2', 'deploy_change', { status: 'FAILED', completedAt: at(120) })],
  });
  const m = model({
    definition: AGENT_FLOW,
    snapshot: snapshot('FAILED', [
      nodeRun('in', 'text_input'),
      nodeRun('agent', 'agent', { status: 'FAILED', startedAt: at(2), completedAt: at(120) }),
    ]),
    traces: { nr_agent: agentTrace([turn(1, 'lint', 3), release]) },
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
    selectedId: 'act_2',
    onSelect: () => undefined,
  };
}

function renderAction(c: DetailContext) {
  const el = c.model.byId.get('act_2');
  if (!el) throw new Error('the release action is not in the model');
  return render(
    <MemoryRouter>
      <ElementOverview el={el} ctx={c} />
    </MemoryRouter>,
  );
}

const OCCUPATION: ReleaseOccupationTrace = {
  id: 'occ_1',
  targetKey: 'token-service/staging',
  operationKey: 'op_act_2_1',
  status: 'ACTIVE',
  acquiredAt: at(6),
  releasedAt: null,
  releaseReason: null,
  nextQueryAt: at(180),
  queryCount: 1,
  deployedUncheckedVersion: null,
};

describe('the release action overview after the run ended', () => {
  it('shows the target still held and when production is checked next', () => {
    renderAction(stoppedReleaseCtx(OCCUPATION));

    expect(screen.getByText('token-service/staging, held')).toBeInTheDocument();
    expect(screen.getByText('Next production check')).toBeInTheDocument();
    expect(screen.queryByText('Running version')).not.toBeInTheDocument();
  });

  it('shows the version a late deploy left running as unchecked once the target is freed', () => {
    renderAction(
      stoppedReleaseCtx({
        ...OCCUPATION,
        status: 'RELEASED',
        releasedAt: at(300),
        releaseReason: 'DEPLOYED',
        nextQueryAt: null,
        queryCount: 3,
        deployedUncheckedVersion: '1.4.2',
      }),
    );

    expect(screen.getByText(/token-service\/staging, released/)).toBeInTheDocument();
    expect(screen.getByText('Running version')).toBeInTheDocument();
    expect(
      screen.getByText('1.4.2, deployed after the run ended and not health-checked'),
    ).toBeInTheDocument();
    expect(screen.queryByText('Next production check')).not.toBeInTheDocument();
  });
});
