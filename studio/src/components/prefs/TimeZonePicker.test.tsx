import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { TimeZonePicker } from './TimeZonePicker';
import { Timestamp } from '@/components/time/Time';
import { TZ_KEY, usePrefsStore } from '@/stores/prefs-store';

beforeEach(() => {
  usePrefsStore.setState({ timeZonePref: 'local', localZone: 'Asia/Shanghai' });
});
afterEach(() => {
  localStorage.clear();
});

describe('TimeZonePicker', () => {
  it('shows the offset, finds a zone by offset, and re-renders every time on the page', async () => {
    const user = userEvent.setup();
    render(
      <>
        <TimeZonePicker />
        <Timestamp iso="2026-10-08T06:32:10Z" />
      </>,
    );
    expect(screen.getByText('2026-10-08 14:32')).toBeInTheDocument();
    const trigger = screen.getByRole('button', { name: /Shanghai time \(UTC\+8\)/ });
    expect(trigger).toHaveTextContent('UTC+8');

    await user.click(trigger);
    expect(screen.getByRole('textbox', { name: 'Search city, zone or offset' })).toHaveFocus();
    await user.keyboard('−7');
    const la = screen.getByRole('option', { name: /Los Angeles/ });
    await user.click(la);

    expect(localStorage.getItem(TZ_KEY)).toBe('America/Los_Angeles');
    expect(screen.getByText('2026-10-07 23:32')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Los Angeles time/ })).toHaveFocus();
  });

  it('offers UTC and the detected zone, and forgets the choice when back on the detected zone', async () => {
    const user = userEvent.setup();
    render(<TimeZonePicker />);
    await user.click(screen.getByRole('button', { name: /Change time zone/ }));
    expect(screen.getByRole('group', { name: 'Detected' })).toHaveTextContent('This device');
    await user.click(screen.getByRole('option', { name: /Coordinated Universal Time/ }));
    expect(localStorage.getItem(TZ_KEY)).toBe('UTC');
    expect(
      screen.getByRole('button', { name: 'Times are shown in UTC. Change time zone' }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /Change time zone/ }));
    await user.click(screen.getByRole('option', { name: /This device/ }));
    expect(localStorage.getItem(TZ_KEY)).toBeNull();
  });

  it('says how to search when nothing matches', async () => {
    const user = userEvent.setup();
    render(<TimeZonePicker />);
    await user.click(screen.getByRole('button', { name: /Change time zone/ }));
    await user.keyboard('Atlantis');
    expect(
      screen.getByText('No time zone matches “Atlantis”. Try a city or an offset like +8.'),
    ).toBeInTheDocument();
  });

  it('ignores an unknown zone', () => {
    act(() => {
      usePrefsStore.getState().setTimeZone('Mars/Olympus');
    });
    expect(usePrefsStore.getState().timeZonePref).toBe('local');
  });
});
