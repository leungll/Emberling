import { describe, expect, it } from 'vitest';

import type { AgentTrace } from '@/api/agent-types';
import { mediaOfRun, mediaOfTraces } from './media-source';
import { agentTrace } from '@/test/run-fixtures';

const item = (ref: string) => ({ ref, url: `/api/x/${ref}`, mediaType: 'video/webm' });

describe('mediaOfRun', () => {
  it('lists the run’s media first, then each trace’s, in order', () => {
    const run = { items: [{ ...item('src'), nodeRunId: 'nr_task' }], truncated: false };
    const a = agentTrace([]);
    a.media = { items: [{ ...item('clip_a'), toolAttemptId: 'ta_1' }], truncated: false };
    const b = agentTrace([]);
    b.media = { items: [{ ...item('clip_b'), toolAttemptId: 'ta_2' }], truncated: false };
    expect(mediaOfRun(run, [a, b]).items.map((x) => x.ref)).toEqual(['src', 'clip_a', 'clip_b']);
  });

  it('is truncated when any list is', () => {
    const a = agentTrace([]);
    a.media = { items: [], truncated: true };
    expect(mediaOfRun({ items: [], truncated: false }, [a]).truncated).toBe(true);
    expect(mediaOfRun({ items: [], truncated: true }, []).truncated).toBe(true);
    expect(mediaOfRun({ items: [], truncated: false }, [agentTrace([])]).truncated).toBe(false);
  });

  it('tolerates a run or a trace served without a list', () => {
    const bare = agentTrace([]) as Partial<AgentTrace>;
    delete bare.media;
    expect(mediaOfRun(undefined, [bare as AgentTrace])).toEqual({ items: [], truncated: false });
    expect(mediaOfTraces([bare as AgentTrace]).items).toEqual([]);
  });
});
