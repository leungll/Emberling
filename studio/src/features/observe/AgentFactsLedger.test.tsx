import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { AgentFactsLedger } from './AgentFactsLedger';
import type { AgentTrace } from '@/api/types';

function trace(overrides: Partial<AgentTrace> = {}): AgentTrace {
  return {
    agentRun: {
      id: 'ar_1',
      nodeRunId: 'nr_agent',
      termination: 'FINAL_RESPONSE',
      currentTurnNo: 3,
      currentContextVersion: 4,
      currentStateVersion: 0,
      deadline: '2026-10-09T12:10:00Z',
      terminatedAt: '2026-10-09T12:00:09Z',
      error: null,
    },
    turns: [],
    facts: {
      items: [
        {
          id: 'fact_gen',
          factType: 'image_generated',
          subject: 'img_a1',
          bindings: { photoAssetId: 'photo_a', settingsDigest: 'sha256:abc' },
          verdict: null,
          basisFactId: null,
          toolAttemptId: 'ta_1',
          createdAt: '2026-10-09T12:00:02Z',
        },
        {
          id: 'fact_review',
          factType: 'asset_reviewed',
          subject: 'img_a1',
          bindings: { photoAssetId: 'photo_a' },
          verdict: false,
          basisFactId: 'fact_gen',
          toolAttemptId: 'ta_2',
          createdAt: '2026-10-09T12:00:04Z',
        },
      ],
      truncated: false,
    },
    generationBudget: { maxGenerationCalls: 4, generationCallsUsed: 1 },
    ...overrides,
  };
}

describe('AgentFactsLedger', () => {
  it('lists the facts in the order received with verdict, basis and bindings', () => {
    render(<AgentFactsLedger trace={trace()} />);

    const rows = screen.getAllByTestId('agent-fact');
    expect(rows).toHaveLength(2);
    expect(within(rows[0]!).getByText('image_generated')).toBeInTheDocument();
    expect(
      within(rows[0]!).getByText('photoAssetId=photo_a · settingsDigest=sha256:abc'),
    ).toBeInTheDocument();
    expect(within(rows[0]!).getByText('—')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('asset_reviewed')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('failed')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('basis fact_gen')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('2026-10-09 12:00:04Z')).toBeInTheDocument();
    expect(screen.getByTestId('generation-budget')).toHaveTextContent('1 / 4');
    expect(screen.queryByTestId('agent-facts-truncated')).not.toBeInTheDocument();
  });

  it('reports a cut ledger and an absent limit without inventing either', () => {
    const base = trace();
    render(
      <AgentFactsLedger
        trace={trace({
          facts: { items: base.facts.items, truncated: true },
          generationBudget: { maxGenerationCalls: null, generationCallsUsed: 2 },
        })}
      />,
    );

    expect(screen.getByTestId('generation-budget')).toHaveTextContent('2 · no limit');
    expect(screen.getByTestId('agent-facts-truncated')).toHaveTextContent('oldest 2 facts');
  });

  it('renders nothing before the Trace has loaded', () => {
    const { container } = render(<AgentFactsLedger trace={null} />);
    expect(container).toBeEmptyDOMElement();
  });
});
