import { describe, expect, it } from 'vitest';

import { contentLabel, reportSchemaOf, reportSource, reportTitle } from './port-content';
import { definition, node } from '@/test/run-fixtures';

const agent = node('agent', 'agent', 'Writer', {
  config: { outputSchema: { title: 'Brief', properties: { text: { type: 'string' } } } },
});
const plain = node('agent2', 'agent', 'Plain');
const out = node('out', 'text_output', 'Answer');
const flow = definition([agent, plain, out], [['agent', 'out']]);

describe('port content labels', () => {
  it('names an Agent output after its declared report format, else Final report', () => {
    expect(reportTitle(agent)).toBe('Brief');
    expect(reportTitle(plain)).toBe('Final report');
    expect(reportTitle(out)).toBeNull();
    expect(reportTitle(undefined)).toBeNull();
  });

  it('reads a report format only from an Agent with properties', () => {
    expect(reportSchemaOf(agent)?.title).toBe('Brief');
    expect(reportSchemaOf(plain)).toBeNull();
    expect(reportSchemaOf(out)).toBeNull();
  });

  it('labels the input an Agent feeds after that Agent’s report', () => {
    expect(reportSource(flow.nodes, flow.edges, 'out')?.id).toBe('agent');
    expect(reportSource(flow.nodes, flow.edges, 'agent')).toBeNull();
    expect(contentLabel(flow.nodes, flow.edges, out, 'in', 'in')).toBe('Brief');
    expect(contentLabel(flow.nodes, flow.edges, out, 'in', 'other')).toBeNull();
    expect(contentLabel(flow.nodes, flow.edges, agent, 'out', 'out')).toBe('Brief');
    expect(contentLabel(flow.nodes, flow.edges, plain, 'in', 'in')).toBeNull();
  });
});
