import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { Button, IconButton } from './button';
import { Dialog } from './dialog';
import { Menu, MenuContent, MenuItem, MenuTrigger } from './menu';
import { Toaster } from './toast';
import { dismissToast, showToast, toastDuration } from '@/lib/toast';

function DialogHarness({ onSubmit }: { onSubmit?: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open
      </button>
      <Dialog
        open={open}
        onClose={() => setOpen(false)}
        title="Run workflow"
        description="Workflow version 3"
        onSubmit={onSubmit}
        footer={
          <>
            <Button variant="secondary" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button data-dialog-primary>Start run</Button>
          </>
        }
      >
        <label>
          Topic
          <input />
        </label>
      </Dialog>
    </>
  );
}

describe('Dialog', () => {
  it('focuses the first field, traps Tab, closes on Escape and returns focus', async () => {
    const user = userEvent.setup();
    render(<DialogHarness />);
    const trigger = screen.getByRole('button', { name: 'Open' });
    await user.click(trigger);

    const dialog = screen.getByRole('dialog', { name: 'Run workflow' });
    expect(dialog).toHaveAccessibleDescription('Workflow version 3');
    expect(within(dialog).getByRole('textbox', { name: 'Topic' })).toHaveFocus();

    for (let i = 0; i < 6; i += 1) await user.tab();
    expect(dialog).toContainElement(document.activeElement as HTMLElement);

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(trigger).toHaveFocus();
  });

  it('ignores a click outside', async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 });
    render(<DialogHarness />);
    await user.click(screen.getByRole('button', { name: 'Open' }));
    await user.click(document.body);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('submits with Cmd or Ctrl and Enter', async () => {
    const user = userEvent.setup();
    const submit = vi.fn();
    render(<DialogHarness onSubmit={submit} />);
    await user.click(screen.getByRole('button', { name: 'Open' }));
    await user.keyboard('{Control>}{Enter}{/Control}');
    expect(submit).toHaveBeenCalledOnce();
  });
});

describe('Dialog initial focus on an icon button', () => {
  function IconHarness() {
    const [open, setOpen] = useState(false);
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>
          Open
        </button>
        <Dialog open={open} onClose={() => setOpen(false)} title="Image">
          <IconButton label="Next image" data-dialog-primary>
            →
          </IconButton>
        </Dialog>
      </>
    );
  }

  it('shows no tooltip for the focus it sets, so one Escape closes it', async () => {
    const user = userEvent.setup();
    render(<IconHarness />);
    await user.click(screen.getByRole('button', { name: 'Open' }));
    expect(screen.getByRole('button', { name: 'Next image' })).toHaveFocus();
    expect(screen.queryByRole('tooltip')).toBeNull();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('opened from the keyboard, still shows no tooltip for the focus it sets', async () => {
    const user = userEvent.setup();
    render(<IconHarness />);
    await user.tab();
    await user.keyboard('{Enter}');
    expect(screen.getByRole('button', { name: 'Next image' })).toHaveFocus();
    expect(screen.queryByRole('tooltip')).toBeNull();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('shows the tooltip when Tab moves onto the button', async () => {
    const user = userEvent.setup();
    render(<IconHarness />);
    await user.click(screen.getByRole('button', { name: 'Open' }));
    await user.tab();
    await user.tab();
    expect(screen.getByRole('button', { name: 'Next image' })).toHaveFocus();
    expect(await screen.findByRole('tooltip')).toHaveTextContent('Next image');
  });
});

describe('Menu', () => {
  function MenuHarness({ onRun }: { onRun: () => void }) {
    return (
      <Menu>
        <MenuTrigger asChild>
          <button type="button">Actions</button>
        </MenuTrigger>
        <MenuContent>
          <MenuItem onSelect={onRun}>Run</MenuItem>
          <MenuItem>Edit</MenuItem>
          <MenuItem>Runs</MenuItem>
          <MenuItem danger>Delete</MenuItem>
        </MenuContent>
      </Menu>
    );
  }

  it('opens from the keyboard on the first item, moves with arrows and typeahead, and returns focus', async () => {
    const user = userEvent.setup();
    const run = vi.fn();
    render(<MenuHarness onRun={run} />);
    const trigger = screen.getByRole('button', { name: 'Actions' });
    trigger.focus();
    await user.keyboard('{Enter}');
    expect(screen.getByRole('menuitem', { name: 'Run' })).toHaveFocus();

    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();
    await user.keyboard('{End}');
    expect(screen.getByRole('menuitem', { name: 'Delete' })).toHaveFocus();
    await user.keyboard('r');
    expect(screen.getByRole('menuitem', { name: 'Run' })).toHaveFocus();

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(trigger).toHaveFocus();

    await user.keyboard('{ArrowDown}');
    await user.keyboard('{Enter}');
    expect(run).toHaveBeenCalledOnce();
    expect(trigger).toHaveFocus();
  });
});

describe('Toast', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('stays 5 s, or 8 s with an action', () => {
    expect(toastDuration({})).toBe(5000);
    expect(toastDuration({ action: { label: 'Undo', onAction: () => undefined } })).toBe(8000);
  });

  it('shows one toast at a time; a new one replaces the old', () => {
    render(<Toaster />);
    act(() => {
      showToast('Saved as version 4.');
    });
    expect(screen.getByText('Saved as version 4.')).toBeInTheDocument();
    act(() => {
      showToast('Copied run ID', { done: true });
    });
    expect(screen.queryByText('Saved as version 4.')).toBeNull();
    expect(screen.getByText('Copied run ID')).toBeInTheDocument();
  });

  it('offers its one action', async () => {
    const undo = vi.fn();
    render(<Toaster />);
    act(() => {
      showToast('Step deleted.', { action: { label: 'Undo', onAction: undo } });
    });
    await userEvent.click(screen.getByRole('button', { name: 'Undo' }));
    expect(undo).toHaveBeenCalledOnce();
    act(() => {
      dismissToast(Number.MAX_SAFE_INTEGER);
    });
  });
});
