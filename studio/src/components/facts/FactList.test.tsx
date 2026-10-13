import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { FactList, TechnicalDetails } from './FactList';

describe('FactList', () => {
  it('lists label and value pairs and marks a missing value', () => {
    render(
      <FactList
        facts={[
          { label: 'Workflow version', value: '1 (latest)' },
          { label: 'Tokens', value: null },
          false,
          { label: 'Ended', value: '' },
        ]}
      />,
    );
    const terms = screen.getAllByRole('term').map((t) => t.textContent);
    expect(terms).toEqual(['Workflow version', 'Tokens', 'Ended']);
    expect(screen.getAllByText('Not recorded')).toHaveLength(2);
  });
});

describe('TechnicalDetails', () => {
  it('is collapsed at first and remembers being opened for the session', async () => {
    const { unmount } = render(
      <TechnicalDetails storageKey="error">
        <code>EXECUTOR_ERROR</code>
      </TechnicalDetails>,
    );
    const summary = screen.getByText('Technical details');
    expect(summary.closest('details')).not.toHaveAttribute('open');
    await userEvent.click(summary);
    expect(summary.closest('details')).toHaveAttribute('open');
    unmount();

    render(
      <TechnicalDetails storageKey="error">
        <code>EXECUTOR_ERROR</code>
      </TechnicalDetails>,
    );
    expect(screen.getByText('Technical details').closest('details')).toHaveAttribute('open');
    sessionStorage.clear();
  });
});
