import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { RunInputDialog } from './RunInputDialog';
import { ApiRequestError, uploadAsset } from '@/api/client';
import type * as ClientModule from '@/api/client';
import type { RunInputSchema } from '@/api/types';

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof ClientModule>();
  return { ...actual, uploadAsset: vi.fn() };
});

const uploadAssetMock = vi.mocked(uploadAsset);

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

/** Same shape, but the Image Input is required. */
const requiredImageSchema: RunInputSchema = {
  ...runInputSchema,
  required: ['brief', 'reference'],
};

function renderDialog(onSubmit = vi.fn(), schema: RunInputSchema = runInputSchema) {
  render(
    <RunInputDialog
      open
      onClose={vi.fn()}
      workflowId="wf_123"
      definitionVersion={4}
      runInputSchema={schema}
      onSubmit={onSubmit}
    />,
  );
  return onSubmit;
}

const assetRef = {
  assetId: 'asset_123',
  mediaType: 'image/png',
  sizeBytes: 4,
  sha256: 'abc123',
};

function pngFile(name = 'reference.png') {
  return new File(['ref'], name, { type: 'image/png' });
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

  it('uploads an image and writes the AssetRef object into the submitted run input', async () => {
    uploadAssetMock.mockResolvedValue(assetRef);
    const onSubmit = renderDialog();
    const user = userEvent.setup();

    await user.type(screen.getByLabelText(/Creative Brief/), 'A small ember creature');
    const fileInput = screen.getByLabelText(/reference/i);
    await user.upload(fileInput, pngFile());

    await screen.findByText(/asset_123/);
    await user.click(screen.getByRole('button', { name: 'Create run' }));

    expect(onSubmit).toHaveBeenCalledWith({
      brief: 'A small ember creature',
      reference: assetRef,
    });
    // The AssetRef is the only thing written; no browser-local blob or download URL.
    const submitted = onSubmit.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(typeof submitted.reference).toBe('object');
    expect(JSON.stringify(submitted)).not.toMatch(/^blob:|downloadUrl/);
  });

  it('shows the server-rejected media type and keeps the field empty', async () => {
    uploadAssetMock.mockRejectedValue(
      new ApiRequestError(400, {
        error: { code: 'VALIDATION_FAILED', message: 'unsupported asset media type' },
      }),
    );
    const onSubmit = renderDialog();
    const user = userEvent.setup();

    await user.type(screen.getByLabelText(/Creative Brief/), 'A small ember creature');
    const fileInput = screen.getByLabelText(/reference/i);
    await user.upload(fileInput, pngFile());

    await screen.findByText(/unsupported asset media type/);
    expect(screen.queryByText(/asset_123/)).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Create run' }));
    const submitted = onSubmit.mock.calls[0]?.[0] as Record<string, unknown> | undefined;
    expect(submitted?.reference).toBeUndefined();
  });

  it('blocks Run when a required Image Input has no completed upload', async () => {
    const onSubmit = renderDialog(vi.fn(), requiredImageSchema);
    const user = userEvent.setup();

    await user.type(screen.getByLabelText(/Creative Brief/), 'A small ember creature');
    await user.click(screen.getByRole('button', { name: 'Create run' }));

    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText(/upload an image/i)).toBeInTheDocument();
  });

  it('submits only the filled scalar fields and omits an optional Image Input with no upload', async () => {
    const onSubmit = renderDialog();
    const user = userEvent.setup();

    await user.type(screen.getByLabelText(/Creative Brief/), 'A small ember creature');
    await user.click(screen.getByRole('button', { name: 'Create run' }));

    expect(onSubmit).toHaveBeenCalledWith({ brief: 'A small ember creature' });
    const submitted = onSubmit.mock.calls[0]?.[0] as Record<string, unknown>;
    expect('reference' in submitted).toBe(false);
  });

  it('disables the Run button while an upload is in flight', async () => {
    let resolveUpload!: (value: typeof assetRef) => void;
    uploadAssetMock.mockReturnValue(
      new Promise((resolve) => {
        resolveUpload = resolve;
      }),
    );
    renderDialog();
    const user = userEvent.setup();

    const fileInput = screen.getByLabelText(/reference/i);
    await user.upload(fileInput, pngFile());

    await waitFor(() => expect(screen.getByRole('button', { name: 'Create run' })).toBeDisabled());

    resolveUpload(assetRef);
    await screen.findByText(/asset_123/);
    expect(screen.getByRole('button', { name: 'Create run' })).not.toBeDisabled();
  });
});
