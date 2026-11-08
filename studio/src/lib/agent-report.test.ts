import { describe, expect, it } from 'vitest';

import { findBySchema, hasReport, lastReport, reportOf, schemaObj } from './agent-report';
import { definition, node, nodeRun } from '@/test/run-fixtures';

const schema = {
  properties: { summary: { type: 'string' }, score: { type: 'number' } },
  required: ['summary'],
};
const agent = node('agent', 'agent', 'Reviewer', { config: { outputSchema: schema } });
const out = node('out', 'text_output', 'Result');
const flow = definition([agent, out], [['agent', 'out']]);
const envelope = JSON.stringify({
  outcome: 'CLIP_DELIVERED',
  agentReport: { summary: 'Looks good', score: 9 },
  checks: [
    { name: 'clip_reviewed', passed: true },
    { name: 'clip_delivered', passed: false },
  ],
});

describe('an Agent’s final report', () => {
  it('finds the object that follows the schema inside JSON text and nested output', () => {
    const s = schemaObj(schema);
    expect(s).not.toBeNull();
    expect(findBySchema(s!, '{"summary":"x"}')).toEqual({ summary: 'x' });
    expect(findBySchema(s!, { text: { report: { summary: 'y', score: 1 } } })).toEqual({
      summary: 'y',
      score: 1,
    });
    expect(findBySchema(s!, { other: 1 })).toBeNull();
    expect(findBySchema(s!, 'not json')).toBeNull();
    expect(schemaObj({ type: 'string' })).toBeNull();
  });

  it('reads the completion envelope: outcome, report and checks', () => {
    const report = reportOf(agent, nodeRun('agent', 'agent', { output: { text: envelope } }));
    expect(report?.outcome).toBe('CLIP_DELIVERED');
    expect(report?.data).toEqual({ summary: 'Looks good', score: 9 });
    expect(report?.checks).toEqual([
      { name: 'clip_reviewed', passed: true },
      { name: 'clip_delivered', passed: false },
    ]);
    expect(report?.text).toBeNull();
    expect(hasReport(report)).toBe(true);
  });

  it('keeps plain text when the output follows no format', () => {
    const plain = node('p', 'agent', 'Plain');
    const report = reportOf(plain, nodeRun('p', 'agent', { output: { text: 'All done.' } }));
    expect(report?.text).toBe('All done.');
    expect(report?.data).toBeNull();
    expect(hasReport(report)).toBe(true);
    expect(hasReport(reportOf(plain, nodeRun('p', 'agent', { output: {} })))).toBe(false);
    expect(reportOf(plain, undefined)).toBeNull();
  });

  it('reads the report an output step shows from the Agent that feeds it', () => {
    const report = lastReport(
      flow.nodes,
      flow.edges,
      out,
      nodeRun('out', 'text_output', { output: { text: envelope } }),
    );
    expect(report?.data?.summary).toBe('Looks good');
    expect(lastReport(flow.nodes, flow.edges, agent, undefined)).toBeNull();
  });
});
