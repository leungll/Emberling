import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { CompletionCard } from './CompletionCard';
import type { AgentCompletion, CompletionOutcome } from '@/api/types';

function completion(
  outcome: CompletionOutcome,
  extra: Partial<AgentCompletion> = {},
): AgentCompletion {
  return {
    outcome,
    checks: [
      { name: 'workspace_patch_recorded', passed: true, refs: ['wp_1'] },
      { name: 'latest_patch_verified', passed: true, refs: ['fact_1'] },
    ],
    missing: [],
    evaluatedAt: '2026-11-02T10:00:00Z',
    ...extra,
  };
}

describe('CompletionCard', () => {
  it.each<[CompletionOutcome, string]>([
    ['READY_FOR_APPROVAL', 'Ready for approval'],
    ['DEPLOYED', 'Deployed'],
    ['DEPLOYED_CHECK_FAILED', 'Deployed, health check failed'],
    ['RELEASE_UNKNOWN', 'Release outcome unknown'],
    ['INCOMPLETE', 'Not complete'],
  ])('renders the %s outcome with its own title and description', (outcome, title) => {
    render(<CompletionCard completion={completion(outcome)} />);
    const card = screen.getByRole('group', { name: 'Task result' });
    expect(card).toHaveAttribute('data-outcome', outcome);
    expect(within(card).getByText(title)).toBeInTheDocument();
    expect(within(card).getByText('2 of 2 passed')).toBeInTheDocument();
    // Each outcome explains itself in a sentence after the title.
    const paragraphs = within(card).getAllByRole('paragraph');
    expect(paragraphs[1]?.textContent?.length ?? 0).toBeGreaterThan(20);
  });

  it('lists what an incomplete task still misses, by description and code', () => {
    render(
      <CompletionCard
        completion={completion('INCOMPLETE', {
          checks: [
            { name: 'workspace_patch_recorded', passed: true, refs: ['wp_1'] },
            { name: 'change_request_binds_latest_patch', passed: false, refs: [] },
          ],
          missing: [
            {
              code: 'CHANGE_REQUEST_MISSING',
              description: 'No change request names the latest patch.',
            },
          ],
        })}
      />,
    );
    const card = screen.getByRole('group', { name: 'Task result' });
    expect(within(card).getByText('Still missing')).toBeInTheDocument();
    const items = within(card).getAllByRole('listitem');
    expect(items).toHaveLength(1);
    expect(items[0]).toHaveTextContent('No change request names the latest patch.');
    expect(items[0]).toHaveTextContent('CHANGE_REQUEST_MISSING');
    expect(within(card).getByText('1 of 2 passed')).toBeInTheDocument();
  });

  it('shows no missing list when nothing is missing', () => {
    render(<CompletionCard completion={completion('DEPLOYED')} />);
    expect(screen.queryByText('Still missing')).not.toBeInTheDocument();
    expect(screen.queryByRole('list')).not.toBeInTheDocument();
  });
});
