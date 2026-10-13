import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';

import { Pager } from './Pager';
import { Select } from './Select';

const MODELS = [
  { value: 'm1', label: 'Opus', description: 'Plans and calls tools' },
  { value: 'm2', label: 'Sonnet' },
  { value: 'm3', label: 'Haiku', search: 'fast' },
];

function Harness({ search }: { search?: string }) {
  const [value, setValue] = useState('m1');
  return (
    <Select
      label="Model"
      value={value}
      options={MODELS}
      onChange={setValue}
      search={search}
      noMatch={(q) => `No model matches “${q}”.`}
    />
  );
}

describe('Select', () => {
  it('names the trigger with the selected display name and picks by keyboard', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const trigger = screen.getByRole('button', { name: 'Model: Opus' });
    expect(trigger).toHaveAttribute('aria-haspopup', 'listbox');

    trigger.focus();
    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('option', { name: /Opus/ })).toHaveFocus();
    expect(screen.getByRole('option', { name: /Opus/ })).toHaveAttribute('aria-selected', 'true');

    await user.keyboard('{End}');
    expect(screen.getByRole('option', { name: 'Haiku' })).toHaveFocus();
    await user.keyboard('s');
    expect(screen.getByRole('option', { name: 'Sonnet' })).toHaveFocus();
    await user.keyboard('{Enter}');

    expect(screen.queryByRole('listbox')).toBeNull();
    expect(screen.getByRole('button', { name: 'Model: Sonnet' })).toHaveFocus();
  });

  it('closes on Escape and returns focus to the trigger without changing the value', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole('button', { name: 'Model: Opus' }));
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('listbox')).toBeNull();
    expect(screen.getByRole('button', { name: 'Model: Opus' })).toHaveFocus();
  });

  it('searches by label and extra text, and says when nothing matches', async () => {
    const user = userEvent.setup();
    render(<Harness search="Search models" />);
    await user.click(screen.getByRole('button', { name: 'Model: Opus' }));
    expect(screen.getByRole('textbox', { name: 'Search models' })).toHaveFocus();

    await user.keyboard('fast');
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual(['Haiku']);
    await user.keyboard('{Enter}');
    expect(screen.getByRole('button', { name: 'Model: Haiku' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Model: Haiku' }));
    await user.keyboard('zzz');
    expect(screen.getByText('No model matches “zzz”.')).toBeInTheDocument();
  });
});

function PagerHarness() {
  const [page, setPage] = useState(1);
  const total = 45;
  const from = (page - 1) * 20 + 1;
  return (
    <Pager
      from={from}
      to={Math.min(page * 20, total)}
      total={total}
      noun={['run', 'runs']}
      size={20}
      onSize={() => undefined}
      hasPrevious={page > 1}
      hasNext={page * 20 < total}
      onPrevious={() => setPage((p) => p - 1)}
      onNext={() => setPage((p) => p + 1)}
    />
  );
}

describe('Pager', () => {
  it('shows the range and keeps focus on Next until it runs out, then on Previous', async () => {
    const user = userEvent.setup();
    render(<PagerHarness />);
    expect(screen.getByText('1–20 of 45 runs')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled();

    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('21–40 of 45 runs')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Next' })).toHaveFocus();

    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('41–45 of 45 runs')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Previous' })).toHaveFocus();
  });

  it('shows only the count when everything fits on one page, and only the range without a total', () => {
    const { rerender } = render(
      <Pager
        from={1}
        to={7}
        total={7}
        noun={['workflow', 'workflows']}
        size={20}
        onSize={() => undefined}
        hasPrevious={false}
        hasNext={false}
        onPrevious={() => undefined}
        onNext={() => undefined}
      />,
    );
    expect(screen.getByText('7 workflows')).toBeInTheDocument();
    expect(screen.queryByRole('navigation')).toBeNull();
    rerender(
      <Pager
        from={21}
        to={40}
        total={null}
        noun={['run', 'runs']}
        size={20}
        onSize={() => undefined}
        hasPrevious
        hasNext
        onPrevious={() => undefined}
        onNext={() => undefined}
      />,
    );
    expect(screen.getByText('21–40')).toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: 'Pages' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Rows per page/ })).toHaveTextContent('20');
  });
});
