import { describe, expect, it } from 'vitest';

import type { ToolMetadata } from '@/api/types';
import { agentToolFlow, maxFailedFacts, retriesText, toolFlow, toolTitle } from './agent-tools';
import { TOOLS } from '@/test/registry-fixture';
import { node } from '@/test/run-fixtures';

const base = TOOLS[0]!;
const make = (name: string, extra: Partial<ToolMetadata>): ToolMetadata => ({
  ...base,
  name,
  description: `${name} description`,
  ...extra,
});
const read = make('read_source', { title: 'Read the source' });
const check = make('check_cut', {
  produces: { factType: 'cut_review' } as ToolMetadata['produces'],
});
const publish = make('publish_cut', {
  executionKind: 'ASYNC',
  requires: [{ factType: 'cut_review', subjectArgument: '/cut', matchBindings: [] }],
});
const registry = new Map([read, check, publish].map((t) => [t.name, t]));

describe('agent tool flow', () => {
  it('orders readers, then fact producers, then the tools that need those facts', () => {
    const flow = toolFlow(['publish_cut', 'check_cut', 'read_source'], registry, {
      cut_review: 2,
    });
    expect(flow?.map((s) => s.label)).toEqual(['Read the source', 'Check cut', 'Publish cut']);
    expect(flow?.[1]?.fixes).toBe(2);
    expect(flow?.[2]?.after).toEqual(['Check cut']);
    expect(flow?.[2]?.later).toBe(true);
    expect(flow?.[0]?.fixes).toBeNull();
  });

  it('is null when a tool is not registered or the list is empty', () => {
    expect(toolFlow(['read_source', 'missing'], registry, {})).toBeNull();
    expect(toolFlow([], registry, {})).toBeNull();
  });

  it('reads an Agent node’s tools and failed-fact allowance from its configuration', () => {
    const agent = node('a', 'agent', 'Agent', {
      config: {
        allowedTools: ['check_cut', 'read_source'],
        maxFailedFacts: { cut_review: 1, bad: 'x' },
      },
    });
    expect(maxFailedFacts(agent)).toEqual({ cut_review: 1 });
    expect(agentToolFlow(agent, registry)?.map((s) => s.tool.name)).toEqual([
      'read_source',
      'check_cut',
    ]);
    expect(agentToolFlow(node('t', 'task_input', 'Task'), registry)).toBeNull();
  });

  it('titles a tool by its registered title, else its name in words', () => {
    expect(toolTitle(read)).toBe('Read the source');
    expect(toolTitle(check)).toBe('Check cut');
    expect(retriesText(1)).toBe('up to 1 retry');
    expect(retriesText(3)).toBe('up to 3 retries');
  });
});
