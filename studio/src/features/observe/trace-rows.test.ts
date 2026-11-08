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

  it('shows only the selected element and its rows when scoped to it', () => {
    const agent = m.byId.get('agent');
    if (!agent) throw new Error('fixture has no agent step');
    const scoped = visibleRows(m, (el) => el.kind === 'run', none, agent);
    expect(scoped.rows.map((r) => r.id)).toEqual(['agent', 'turn_1', 'turn_2']);
    const hit = visibleRows(m, () => false, { query: 'deploy', failuresOnly: false }, agent);
    expect(hit.rows.map((r) => r.id)).toEqual(['agent', 'turn_2', 'dec:turn_2', 'act_2']);
  });
});

describe('rowMatches', () => {
  it('matches the name and the quieter type word, ignoring case', () => {
    const call = m.byId.get('act_1');
    expect(call && rowMatches(call, { query: 'TOOL CALL', failuresOnly: false })).toBe(true);
  });
});

describe('openState', () => {
  const el = (id: string) => m.byId.get(id) ?? m.root;

  it('opens the run and its steps by default, showing turn rows that stay closed', () => {
    const isOpen = openState(new Set(), new Set());
    expect(isOpen(m.root)).toBe(true);
    expect(isOpen(el('agent'))).toBe(true);
    expect(isOpen(el('in'))).toBe(true);
    expect(isOpen(el('turn_1'))).toBe(false);
    expect(isOpen(el('turn_2'))).toBe(false);
  });

  it('lets the viewer open a turn and close a step, never the run', () => {
    const isOpen = openState(new Set(['turn_2']), new Set(['agent', m.root.id]));
    expect(isOpen(m.root)).toBe(true);
    expect(isOpen(el('agent'))).toBe(false);
    expect(isOpen(el('turn_2'))).toBe(true);
    expect(isOpen(el('turn_1'))).toBe(false);
  });
});
