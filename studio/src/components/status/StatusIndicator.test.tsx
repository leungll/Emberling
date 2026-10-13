import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { statusInfo } from './status-info';
import { StatusIndicator } from './StatusIndicator';

describe('statusInfo', () => {
  it.each([
    [{ kind: 'run', value: 'PAUSED' } as const, 'Waiting', 'waiting'],
    [{ kind: 'runLong', value: 'PAUSED' } as const, 'Waiting for external result', 'waiting'],
    [{ kind: 'run', value: 'COMPLETED' } as const, 'Completed', 'succeeded'],
    [{ kind: 'step', value: 'READY' } as const, 'Queued', 'queued'],
    [{ kind: 'step', value: 'WAITING_CALLBACK' } as const, 'Waiting for reply', 'waiting'],
    [{ kind: 'step', value: 'NONE' } as const, 'Not started', 'inactive'],
    [{ kind: 'step', value: 'NONE_RUN_FAILED' } as const, 'Did not run', 'inactive'],
    [{ kind: 'turn', value: 'COMPLETED' } as const, 'Succeeded', 'succeeded'],
    [{ kind: 'action', value: 'READY' } as const, 'Decided, not started', 'inactive'],
    [{ kind: 'attempt', value: 'DISPATCHED' } as const, 'Waiting for reply', 'waiting'],
    [{ kind: 'connection', value: 'reconnecting' } as const, 'Reconnecting', 'waiting'],
  ])('%o reads %s with the %s tone', (subject, label, tone) => {
    const info = statusInfo(subject);
    expect(info.label).toBe(label);
    expect(info.tone).toBe(tone);
  });

  it('keeps the raw value for the tooltip, but not for a step that never ran', () => {
    expect(statusInfo({ kind: 'run', value: 'FAILED' }).raw).toBe('FAILED');
    expect(statusInfo({ kind: 'step', value: 'NONE_RUN_FAILED' }).raw).toBeUndefined();
    expect(statusInfo({ kind: 'step', value: 'NONE_RUN_FAILED' }).icon).toBe('circle-minus');
  });

  it('shows an unknown value as itself, inactive, rather than guessing', () => {
    const info = statusInfo({ kind: 'run', value: 'CANCELLED' as never });
    expect(info).toMatchObject({ label: 'CANCELLED', tone: 'inactive' });
  });
});

describe('StatusIndicator', () => {
  it('shows the word, never the raw enum, and the help and raw value in its tooltip', async () => {
    render(<StatusIndicator status={{ kind: 'run', value: 'PAUSED' }} focusable />);
    const status = screen.getByText('Waiting').parentElement!;
    expect(status).toHaveAttribute('data-tone', 'waiting');
    expect(screen.queryByText('PAUSED')).toBeNull();

    await userEvent.tab();
    expect(status).toHaveFocus();
    const tip = await screen.findByRole('tooltip');
    expect(tip).toHaveTextContent('Waiting for an outside service to reply.');
    expect(tip).toHaveTextContent('PAUSED');
  });

  it('gives an icon-only status an accessible name', () => {
    render(<StatusIndicator status={{ kind: 'step', value: 'FAILED' }} size="icon" />);
    expect(screen.getByRole('img', { name: 'Failed' })).toBeInTheDocument();
  });

  it('shows an ended wait as inactive', () => {
    render(<StatusIndicator status={{ kind: 'step', value: 'WAITING_CALLBACK' }} endedWait />);
    expect(screen.getByText('Waiting for reply').parentElement).toHaveAttribute(
      'data-tone',
      'inactive',
    );
  });
});
