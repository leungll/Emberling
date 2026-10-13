import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { ObservedRun } from './runObserver';
import { observeRun } from './runObserver';
import { useRunData } from './useRunData';
import { getAgentTrace, getDefinitionVersion, getNodeRunDetail } from '@/api/client';
import type * as ClientModule from '@/api/client';
import type { NodeRunDetail } from '@/api/types';
import { AGENT_FLOW, agentTrace, event, nodeRun, snapshot, turn } from '@/test/run-fixtures';

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof ClientModule>();
  return {
    ...actual,
    getAgentTrace: vi.fn(),
    getDefinitionVersion: vi.fn(),
    getNodeRunDetail: vi.fn(),
  };
});
vi.mock('./runObserver', () => ({ observeRun: vi.fn() }));

const observeMock = vi.mocked(observeRun);
const detailMock = vi.mocked(getNodeRunDetail);
const traceMock = vi.mocked(getAgentTrace);

function deferred<T>() {
  let resolve: (v: T) => void = () => undefined;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

let emit: (state: ObservedRun) => void = () => undefined;

beforeEach(() => {
  vi.mocked(getDefinitionVersion).mockResolvedValue(AGENT_FLOW);
  observeMock.mockImplementation((_runId, onChange) => {
    emit = onChange;
    return { close: () => undefined, reconnect: () => undefined };
  });
  detailMock.mockReset();
  traceMock.mockReset();
});

describe('useRunData', () => {
  it('keeps a NodeRun’s read in flight when another NodeRun changes', async () => {
    const agentRun = nodeRun('agent', 'agent', {
      status: 'RUNNING',
      completedAt: null,
      latencyMs: null,
    });
    const slowTrace = deferred<ReturnType<typeof agentTrace>>();
    detailMock.mockImplementation((_run, id) =>
      Promise.resolve({
        nodeRun: nodeRun(id.replace('nr_', ''), 'x'),
        attempts: [],
      } as NodeRunDetail),
    );
    traceMock.mockReturnValueOnce(slowTrace.promise);

    const { result } = renderHook(() => useRunData('run_1', 0, []));
    act(() => {
      emit({
        snapshot: snapshot('RUNNING', [nodeRun('in', 'text_input'), agentRun]),
        events: [event(1, 'NODE_STARTED', { nodeRunId: 'nr_agent' })],
        loadError: null,
        connection: 'live',
      });
    });
    // A new Event about another NodeRun changes the read signature while the trace is in flight.
    act(() => {
      emit({
        snapshot: snapshot('RUNNING', [nodeRun('in', 'text_input'), agentRun]),
        events: [
          event(1, 'NODE_STARTED', { nodeRunId: 'nr_agent' }),
          event(2, 'NODE_COMPLETED', { nodeRunId: 'nr_in' }),
        ],
        loadError: null,
        connection: 'live',
      });
    });
    await act(async () => {
      slowTrace.resolve(agentTrace([turn(1, 'lint', 1)]));
      await slowTrace.promise;
    });

    await waitFor(() => {
      expect(result.current.model?.byId.get('turn_1')).toBeDefined();
    });
    expect(traceMock).toHaveBeenCalledTimes(1);
  });
});
