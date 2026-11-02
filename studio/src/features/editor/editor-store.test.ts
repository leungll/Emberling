import { beforeEach, describe, expect, it } from 'vitest';

import { link, step } from '@/test/registry-fixture';
import { useEditorStore } from './editor-store';

const WORKSPACE = {
  repositoryId: 'vuln-demo',
  baseCommit: '0123456789abcdef0123456789abcdef01234567',
  allowedPaths: ['go.mod'],
  deployTarget: { service: 'orders-api', environment: 'staging' },
};

const AGENT_CONFIG = {
  instructions: 'Repair the reported dependency.',
  modelId: 'text-model-v1',
  allowedTools: ['lookup'],
  maxTurns: 6,
  timeoutMs: 600000,
  workspace: WORKSPACE,
  goalPointer: '/goal',
  approvalTimeoutMs: 3600000,
  releaseConfirmTimeoutMs: 900000,
  voidGraceMs: 60000,
  maxReleaseRetries: 2,
  humanWaitTimeoutMs: 7200000,
};

function agentConfig() {
  return useEditorStore
    .getState()
    .current()
    .nodes.find((n) => n.id === 'agent')?.config;
}

beforeEach(() => {
  useEditorStore.getState().load(
    {
      name: 'Repair',
      description: '',
      nodes: [
        step('in', 'text_input', 0, 0, { inputKey: 'brief', required: true }),
        step('agent', 'agent', 160, 0, AGENT_CONFIG),
        step('out', 'text_output', 320, 0, {}),
      ],
      edges: [
        link('e1', 'in', 'text', 'agent', 'input'),
        link('e2', 'agent', 'text', 'out', 'text'),
      ],
    },
    'wf_123@4',
  );
});

describe('editor store agent config', () => {
  it('keeps the workspace, goal and wait bounds when one rendered field changes', () => {
    const s = useEditorStore.getState();
    s.begin('agent.maxTurns');
    s.setConfig('agent', 'maxTurns', 8);
    expect(agentConfig()).toEqual({ ...AGENT_CONFIG, maxTurns: 8 });
  });

  it('keeps them through a patch that removes another field', () => {
    const s = useEditorStore.getState();
    s.begin();
    s.patchConfig('agent', { reviewModelId: 'vision-model-v1' }, ['allowedTools']);
    const { allowedTools: _dropped, ...rest } = AGENT_CONFIG;
    expect(agentConfig()).toEqual({ ...rest, reviewModelId: 'vision-model-v1' });
  });
});
