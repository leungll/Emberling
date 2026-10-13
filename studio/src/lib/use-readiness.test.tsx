import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Readiness } from '@/api/client';
import { READY_POLL_MS, UNREACHABLE_POLL_MS, useReadiness } from './use-readiness';

let visibility: DocumentVisibilityState = 'visible';

beforeEach(() => {
  vi.useFakeTimers();
  visibility = 'visible';
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function setVisibility(next: DocumentVisibilityState) {
  visibility = next;
  document.dispatchEvent(new Event('visibilitychange'));
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
  });
}

describe('useReadiness', () => {
  it('shows checking until the first probe answers, then each of the three states', async () => {
    const answers: Readiness[] = ['ready', 'starting', 'unreachable'];
    const probe = vi.fn(() => Promise.resolve(answers.shift() ?? 'ready'));
    const { result } = renderHook(() => useReadiness(probe));
    expect(result.current).toBe('checking');

    await flush();
    expect(result.current).toBe('ready');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(READY_POLL_MS);
    });
    expect(result.current).toBe('starting');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(READY_POLL_MS);
    });
    expect(result.current).toBe('unreachable');
  });

  it('backs off to 60 s while the Backend is unreachable', async () => {
    const probe = vi.fn(() => Promise.resolve<Readiness>('unreachable'));
    renderHook(() => useReadiness(probe));
    await flush();
    expect(probe).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(READY_POLL_MS);
    });
    expect(probe).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(UNREACHABLE_POLL_MS - READY_POLL_MS);
    });
    expect(probe).toHaveBeenCalledTimes(2);
  });

  it('stops while the page is hidden and checks again when it is shown', async () => {
    const probe = vi.fn(() => Promise.resolve<Readiness>('ready'));
    renderHook(() => useReadiness(probe));
    await flush();
    expect(probe).toHaveBeenCalledTimes(1);

    act(() => {
      setVisibility('hidden');
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(READY_POLL_MS * 4);
    });
    expect(probe).toHaveBeenCalledTimes(1);

    act(() => {
      setVisibility('visible');
    });
    await flush();
    expect(probe).toHaveBeenCalledTimes(2);
  });

  it('stops polling when the shell unmounts', async () => {
    const probe = vi.fn(() => Promise.resolve<Readiness>('ready'));
    const { unmount } = renderHook(() => useReadiness(probe));
    await flush();
    unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(READY_POLL_MS * 2);
    });
    expect(probe).toHaveBeenCalledTimes(1);
  });
});
