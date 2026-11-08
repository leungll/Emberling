import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { ToolMetadata } from '@/api/types';
import { WhatHappened } from './RunStory';
import { TOOLS } from '@/test/registry-fixture';
import { agentTrace, toolAttempt, turn } from '@/test/run-fixtures';

const tools: ToolMetadata[] = [{ ...TOOLS[0]!, name: 'check_cut', title: 'Check the cut' }];

describe('WhatHappened', () => {
  it('reads each turn as a sentence with the tool’s title and its verdict', () => {
    const trace = agentTrace([
      turn(1, 'check_cut', 0, { attempts: [toolAttempt('ta_1', 'check_cut')] }),
      turn(2, 'check_cut', 5, { attempts: [toolAttempt('ta_2', 'check_cut')] }),
      turn(3, null, 9),
    ]);
    const fact = (id: string, verdict: boolean, toolAttemptId: string) => ({
      id,
      factType: 'cut_review',
      subject: 'cut_1',
      bindings: {},
      verdict,
      basisFactId: null,
      toolAttemptId,
      createdAt: '2026-10-01T10:00:00.000Z',
    });
    trace.facts.items = [fact('f1', true, 'ta_1'), fact('f2', false, 'ta_2')];
    render(<WhatHappened trace={trace} tools={tools} onSelect={() => undefined} />);
    expect(screen.getByText('Check the cut, passed')).toBeInTheDocument();
    expect(screen.getByText('Check the cut, did not pass')).toBeInTheDocument();
    expect(screen.getByText('Handed back the final report')).toBeInTheDocument();
    expect(screen.getByText('What happened')).toBeInTheDocument();
  });

  it('describes a failure in plain words and never shows the raw error text', () => {
    const trace = agentTrace([
      turn(1, 'check_cut', 0, {
        action: {
          status: 'FAILED',
          error: { code: 'INVALID_ACTION', message: 'INVALID_ACTION: decision: bad envelope' },
        },
      }),
      turn(2, null, 3, {
        turn: {
          status: 'FAILED',
          decision: null,
          error: { code: 'SOMETHING_NEW', message: 'raw internals' },
        },
      }),
    ]);
    render(<WhatHappened trace={trace} tools={tools} onSelect={() => undefined} />);
    expect(screen.getByText('Check the cut, failed')).toBeInTheDocument();
    expect(screen.getByText('Its decision was rejected')).toBeInTheDocument();
    expect(screen.getByText('Turn failed')).toBeInTheDocument();
    expect(screen.getByText('Something went wrong')).toBeInTheDocument();
    expect(screen.queryByText(/INVALID_ACTION/)).not.toBeInTheDocument();
    expect(screen.queryByText(/raw internals/)).not.toBeInTheDocument();
  });

  it('opens the turn a row names', () => {
    const onSelect = vi.fn();
    render(
      <WhatHappened trace={agentTrace([turn(4, null, 0)])} tools={tools} onSelect={onSelect} />,
    );
    const row = screen.getByRole('button', { name: /Handed back the final report/ });
    expect(row).toHaveAttribute('title', 'Open turn 4');
    row.click();
    expect(onSelect).toHaveBeenCalledWith('turn_4');
  });
});
