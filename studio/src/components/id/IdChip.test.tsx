import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { COPIED_MS, IdChip } from './IdChip';

const ID = 'run_d9c23ab668c54acf9ec83c59c55ce742';

afterEach(() => {
  vi.useRealTimers();
});

function renderChip(props: Partial<Parameters<typeof IdChip>[0]> = {}) {
  return render(
    <MemoryRouter>
      <IdChip id={ID} kind="Run ID" label="run ID" {...props} />
    </MemoryRouter>,
  );
}

describe('IdChip', () => {
  it('shows the kind and the eight-character code, never the full ID', () => {
    renderChip();
    expect(screen.getByText('Run ID')).toBeInTheDocument();
    expect(screen.getByText('d9c23ab6')).toBeInTheDocument();
    expect(screen.queryByText(ID)).toBeNull();
  });

  it('copies the full ID and says Copied for a moment', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: (ms) => vi.advanceTimersByTime(ms) });
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    renderChip();

    await user.click(screen.getByRole('button', { name: 'Copy run ID' }));
    expect(writeText).toHaveBeenCalledWith(ID);
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(COPIED_MS);
    });
    expect(screen.getByRole('button', { name: 'Copy run ID' })).toBeInTheDocument();
  });

  it('links the code to the object when given a target', () => {
    renderChip({ to: '/runs/x' });
    expect(screen.getByRole('link', { name: `Run ID ${ID}` })).toHaveAttribute('href', '/runs/x');
  });

  it('highlights a search match inside the code', () => {
    renderChip({ match: 'c23a' });
    expect(screen.getByText('c23a').tagName).toBe('MARK');
  });

  it('can leave out the copy button', () => {
    renderChip({ copy: false });
    expect(screen.queryByRole('button')).toBeNull();
  });
});
