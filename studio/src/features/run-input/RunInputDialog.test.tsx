import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { RunInputDialog } from './RunInputDialog';
import type { RunInputSchema } from '@/api/types';

/** Shaped like the Backend output for one Text Input and one optional Image Input. */
const runInputSchema: RunInputSchema = {
  type: 'object',
  additionalProperties: false,
  properties: {
    brief: { type: 'string', title: 'Creative Brief', minLength: 1 },
    reference: {
      type: 'object',
      additionalProperties: false,
      properties: {
        assetId: { type: 'string', minLength: 1 },
        mediaType: { type: 'string', minLength: 1 },
        sizeBytes: { type: 'integer', minimum: 0 },
        sha256: { type: 'string', minLength: 1 },
      },
      required: ['assetId', 'mediaType', 'sizeBytes', 'sha256'],
    },
  },
  required: ['brief'],
};

function renderDialog(onSubmit = vi.fn()) {
  render(
    <RunInputDialog
      open
      onClose={vi.fn()}
      workflowId="wf_123"
      definitionVersion={4}
      runInputSchema={runInputSchema}
      onSubmit={onSubmit}
    />,
  );
  return onSubmit;
}

describe('RunInputDialog', () => {
  it('renders the bound definition version', () => {
    renderDialog();
    expect(screen.getByText('wf_123 · v4')).toBeInTheDocument();
  });

  it('renders a required string field from the frozen schema', () => {
    renderDialog();
    const brief = screen.getByLabelText(/Creative Brief/);
    expect(brief).toBeRequired();
  });

  it('renders an AssetRef field as a disabled placeholder with an M2 note', () => {
    renderDialog();

    expect(screen.getByLabelText(/reference/i)).toBeDisabled();
    expect(screen.getByText(/Asset upload lands in M2/)).toBeInTheDocument();
  });

  it('submits only the filled scalar fields and omits absent optional input', async () => {
    const onSubmit = renderDialog();
    const user = userEvent.setup();

    await user.type(screen.getByLabelText(/Creative Brief/), 'A small ember creature');
    await user.click(screen.getByRole('button', { name: 'Create run' }));

    expect(onSubmit).toHaveBeenCalledWith({ brief: 'A small ember creature' });
  });
});
