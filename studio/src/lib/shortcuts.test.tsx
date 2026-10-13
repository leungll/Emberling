import { render } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { isTypingTarget, useKeyShortcut } from './shortcuts';

function Harness({ onKey }: { onKey: () => void }) {
  useKeyShortcut('[', onKey);
  return (
    <>
      <input aria-label="Name" />
      <input type="checkbox" aria-label="Flag" />
      <button type="button">Go</button>
    </>
  );
}

describe('single-key shortcuts', () => {
  it('fire on the page but never while typing in a field', async () => {
    const user = userEvent.setup();
    const onKey = vi.fn();
    const { getByRole } = render(<Harness onKey={onKey} />);

    getByRole('button').focus();
    await user.keyboard('[[[[');
    expect(onKey).toHaveBeenCalledTimes(2);

    getByRole('textbox').focus();
    await user.keyboard('[[');
    expect(onKey).toHaveBeenCalledTimes(2);

    getByRole('checkbox').focus();
    await user.keyboard('[[');
    expect(onKey).toHaveBeenCalledTimes(3);
  });

  it('know which targets take text', () => {
    const textarea = document.createElement('textarea');
    const div = document.createElement('div');
    div.contentEditable = 'true';
    const range = document.createElement('input');
    range.type = 'range';
    expect(isTypingTarget(textarea)).toBe(true);
    expect(isTypingTarget(range)).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });
});
