import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { TurnsTab } from './TurnsTab';
import type { DetailContext } from './detail-context';
import type { ManifestItem } from '@/api/types';
import { MODELS, NODE_TYPES, TOOLS } from '@/test/registry-fixture';
import { AGENT_FLOW, agentTrace, model, nodeRun, snapshot, turn } from '@/test/run-fixtures';

const ARTIFACT_ID = 'art_7d3f9a1c2b4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4';

/** The second turn's materials: the request carried four items and left the log out. */
const MANIFEST: ManifestItem[] = [
  {
    kind: 'INSTRUCTIONS',
    label: 'Instructions',
    ref: 'ar_1',
    version: '',
    sha256: 'aa',
    length: 48,
    included: true,
    omittedReason: null,
    range: null,
  },
  {
    kind: 'TASK_INPUT',
    label: 'Task input',
    ref: 'nr_agent',
    version: '',
    sha256: 'bb',
    length: 30,
    included: true,
    omittedReason: null,
    range: null,
  },
  {
    kind: 'WORKSPACE_FILE',
    label: 'go.mod',
    ref: 'go.mod',
    version: '3',
    sha256: 'cc',
    length: 2048,
    included: true,
    omittedReason: null,
    range: null,
  },
  {
    kind: 'TOOL_RESULT',
    label: 'lookup result',
    ref: 'ta_1',
    version: '1',
    sha256: 'dd',
    length: 120,
    included: true,
    omittedReason: null,
    range: null,
  },
  {
    kind: 'MATERIAL_RANGE',
    label: ARTIFACT_ID,
    ref: ARTIFACT_ID,
    version: '7d3f9a1c2b4e',
    sha256: 'ff',
    length: 96,
    included: true,
    omittedReason: null,
    range: { startLine: 2, lineCount: 2, totalLines: 40 },
  },
  {
    kind: 'ARTIFACT',
    label: ARTIFACT_ID,
    ref: ARTIFACT_ID,
    version: '',
    sha256: '7d3f',
    length: 4096,
    included: false,
    omittedReason: 'REFERENCE_ONLY',
    range: null,
  },
  {
    kind: 'STATE',
    label: 'Agent state',
    ref: 'asv_1',
    version: '1',
    sha256: 'ee',
    length: 64,
    included: true,
    omittedReason: null,
    range: null,
  },
];

function ctx(): DetailContext {
  const turns = [turn(1, 'lookup', 1), turn(2, null, 2, { turn: { manifest: MANIFEST } })];
  const m = model({
    definition: AGENT_FLOW,
    snapshot: snapshot('COMPLETED', [nodeRun('agent', 'agent')]),
    traces: { nr_agent: agentTrace(turns) },
  });
  return {
    model: m,
    failure: null,
    wait: null,
    tools: TOOLS,
    models: MODELS,
    nodeTypes: NODE_TYPES,
    workflowId: 'wf_1',
    latestVersion: 1,
    selectedId: m.root.id,
    onSelect: () => undefined,
  };
}

function renderTurns() {
  const c = ctx();
  const el = c.model.byId.get('agent');
  if (!el) throw new Error('fixture has no agent step');
  return render(
    <MemoryRouter>
      <TurnsTab el={el} ctx={c} />
    </MemoryRouter>,
  );
}

describe('the Turns tab’s materials list', () => {
  it('lists what each turn’s model call was given, with versions and sizes', () => {
    renderTurns();

    const lists = screen.getAllByRole('list', { name: 'Materials' });
    // Only the turn with a recorded Manifest lists materials; an unclaimed or older
    // turn without one shows nothing rather than an empty list.
    expect(lists).toHaveLength(1);
    const list = lists[0];
    if (!list) throw new Error('no materials list');
    const rows = within(list)
      .getAllByRole('listitem')
      .map((li) => li.textContent);
    expect(rows).toHaveLength(7);
    expect(rows[0]).toContain('Instructions');
    expect(rows[0]).toContain('48 B');
    expect(rows[2]).toContain('go.mod');
    expect(rows[2]).toContain('patch 3');
    expect(rows[2]).toContain('2 KB');
    expect(rows[3]).toContain('lookup result');
    expect(rows[3]).toContain('attempt 1');
    expect(rows[4]).toContain('content 7d3f9a1c2b4e');
    expect(rows[4]).toContain('lines 2–3 of 40');
    expect(rows[4]).toContain('96 B');
    expect(rows[6]).toContain('Agent state');
    expect(rows[6]).toContain('version 1');
  });

  it('marks an omitted artifact with its reason and links to its content', () => {
    renderTurns();

    const list = screen.getByRole('list', { name: 'Materials' });
    const omitted = within(list)
      .getAllByRole('listitem')
      .find((li) => li.textContent?.includes('Not included'));
    expect(omitted).toBeDefined();
    if (!omitted) return;
    expect(omitted.textContent).toContain('Not included: available by reference');
    expect(omitted.textContent).toContain('4 KB');
    expect(within(omitted).getByRole('link', { name: 'Open' })).toHaveAttribute(
      'href',
      `/api/runs/run_1/artifacts/${ARTIFACT_ID}/content`,
    );
    // Included items carry no link and no omission note.
    const included = within(list)
      .getAllByRole('listitem')
      .filter((li) => !li.textContent?.includes('Not included'));
    expect(included).toHaveLength(6);
    for (const li of included) {
      expect(within(li).queryByRole('link')).toBeNull();
    }
  });
});
