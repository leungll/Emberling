import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { SKELETON_DELAY_MS, Skeleton, StateBlock } from './StateBlock';

describe('StateBlock', () => {
  it('shows an error with its code and one action, as an alert', async () => {
    const retry = vi.fn();
    render(
      <StateBlock
        kind="error"
        title="Couldn’t load runs."
        code="503 DEPENDENCY_UNAVAILABLE"
        action={{ label: 'Retry', onClick: retry }}
      />,
    );
    expect(screen.getByRole('alert')).toHaveTextContent('Couldn’t load runs.');
    expect(screen.getByText('503 DEPENDENCY_UNAVAILABLE')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it('renders a page-level not-found state with a heading and a link action', () => {
    render(
      <MemoryRouter>
        <StateBlock
          kind="notfound"
          page
          title="This page doesn’t exist."
          action={{ label: 'Go to Runs', to: '/runs' }}
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { name: 'This page doesn’t exist.' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Go to Runs' })).toHaveAttribute('href', '/runs');
  });
});

describe('Skeleton', () => {
  it('stays blank for a fast load and appears after the delay', () => {
    vi.useFakeTimers();
    const { container } = render(<Skeleton kind="rows" count={2} />);
    expect(screen.getByRole('status', { name: 'Loading' })).toBeEmptyDOMElement();
    act(() => {
      vi.advanceTimersByTime(SKELETON_DELAY_MS);
    });
    expect(container.querySelectorAll('.animate-skeleton').length).toBeGreaterThan(0);
    vi.useRealTimers();
  });
});
