import { describe, expect, it } from 'vitest';

import { openState, rowMatches, visibleRows } from './trace-rows';
import { AGENT_FLOW, agentTrace, model, nodeRun, snapshot, turn } from '@/test/run-fixtures';

const m = model({
  definition: AGENT_FLOW,
  snapshot: snapshot('FAILED', [
    nodeRun('in', 'text_input'),
    nodeRun('agent', 'agent', { status: 'FAILED' }),
  ]),
  traces: {
    nr_agent: agentTrace([
      turn(1, 'lint', 1),
      turn(2, 'deploy', 2, { action: { status: 'FAILED', completionSource: null } }),
    ]),
  },
});
const none = { query: '', failuresOnly: false };

describe('visibleRows', () => {
  it('shows a row only when every row above it is open', () => {
    const closed = visibleRows(m, (el) => el.kind === 'run', none);
    expect(closed.rows.map((r) => r.id)).toEqual(['run', 'in', 'agent', 'out']);
    expect(closed.hits).toBeNull();
    const open = visibleRows(m, (el) => el.kind === 'run' || el.id === 'agent', none);
    expect(open.rows.map((r) => r.id)).toEqual(['run', 'in', 'agent', 'turn_1', 'turn_2', 'out']);
  });

  it('keeps each search hit in place with its ancestors marked as the path', () => {
    const { rows, hits } = visibleRows(m, () => false, { query: 'deploy', failuresOnly: false });
    expect(rows.map((r) => r.id)).toEqual(['run', 'agent', 'turn_2', 'dec:turn_2', 'act_2']);
    expect(hits?.get('agent')).toBe('path');
    expect(hits?.get('turn_2')).toBe('hit');
  });

  it('keeps only failed rows under Failures only', () => {
    const { rows } = visibleRows(m, () => false, { query: '', failuresOnly: true });
    expect(rows.every((r) => r.tone === 'failed' || r.kind === 'run')).toBe(true);
    expect(rows.map((r) => r.id)).toContain('act_2');
    expect(rows.map((r) => r.id)).not.toContain('in');
  });
});

describe('rowMatches', () => {
  it('matches the name and the quieter type word, ignoring case', () => {
    const call = m.byId.get('act_1');
    expect(call && rowMatches(call, { query: 'TOOL CALL', failuresOnly: false })).toBe(true);
  });
});

describe('openState', () => {
  it('opens the selection’s ancestors unless the viewer closed them', () => {
    const selected = m.byId.get('act_2') ?? null;
    const isOpen = openState(new Set(), new Set(['turn_2']), selected);
    expect(isOpen(m.root)).toBe(true);
    expect(isOpen(m.byId.get('agent') ?? m.root)).toBe(true);
    expect(isOpen(m.byId.get('turn_2') ?? m.root)).toBe(false);
    expect(isOpen(m.byId.get('in') ?? m.root)).toBe(false);
  });
});
