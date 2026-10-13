import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { Button, IconButton } from './button';
import { Field, TextInput } from './field';
import { Segmented } from './segmented';
import { Tabs } from './tabs';

describe('Button', () => {
  it('stays focusable when unavailable, ignores clicks and says why', async () => {
    const user = userEvent.setup();
    const click = vi.fn();
    render(
      <Button
        unavailableReason="Save your changes to run. A run always uses a saved version."
        onClick={click}
      >
        Run
      </Button>,
    );
    const run = screen.getByRole('button', { name: 'Run' });
    expect(run).toHaveAttribute('aria-disabled', 'true');
    expect(run).toHaveAccessibleDescription(
      'Save your changes to run. A run always uses a saved version.',
    );
    await user.click(run);
    expect(click).not.toHaveBeenCalled();
    expect(run).not.toBeDisabled();
    expect(run).toHaveFocus();
  });

  it('gives an icon button its name', () => {
    render(<IconButton label="Close">x</IconButton>);
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument();
  });
});

describe('Segmented and Tabs', () => {
  it('has one Tab stop and selects with the arrow keys', async () => {
    const user = userEvent.setup();
    function Harness() {
      const [v, setV] = useState<'fields' | 'json'>('fields');
      return (
        <Segmented
          label="Show as"
          value={v}
          onValueChange={setV}
          options={[
            { value: 'fields', label: 'Fields' },
            { value: 'json', label: 'JSON' },
          ]}
        />
      );
    }
    render(<Harness />);
    await user.tab();
    expect(screen.getByRole('radio', { name: 'Fields' })).toHaveFocus();
    // Radix moves focus on the next task and selects while the arrow is still held, as a
    // real key press does.
    await user.keyboard('{ArrowRight>}');
    await waitFor(() => {
      expect(screen.getByRole('radio', { name: 'JSON' })).toBeChecked();
    });
    await user.keyboard('{/ArrowRight}');
  });

  it('shows tab counts and the selected tab content', async () => {
    const user = userEvent.setup();
    function Harness() {
      const [v, setV] = useState<'all' | 'failed'>('all');
      return (
        <Tabs
          label="Run status"
          value={v}
          onValueChange={setV}
          items={[
            { value: 'all', label: 'All', count: 1240 },
            { value: 'failed', label: 'Failed', count: 3 },
          ]}
        >
          <p>{v} rows</p>
        </Tabs>
      );
    }
    render(<Harness />);
    expect(screen.getByRole('tab', { name: 'All 1,240' })).toHaveAttribute('aria-selected', 'true');
    screen.getByRole('tab', { name: 'All 1,240' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(screen.getByRole('tabpanel')).toHaveTextContent('failed rows');
  });
});

describe('Field', () => {
  it('links the label, hint and error to the control', () => {
    render(
      <Field label="Name" optional hint="Shown in lists." error="Name is required.">
        {(p) => <TextInput {...p} />}
      </Field>,
    );
    const input = screen.getByRole('textbox', { name: 'Name (optional)' });
    expect(input).toHaveAttribute('aria-invalid', 'true');
    expect(input).toHaveAccessibleDescription('Shown in lists. Name is required.');
  });
});
