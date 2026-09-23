import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { RunInputDialog } from './RunInputDialog';
import { runInputLabels } from './runInputLabels';
import { ApiRequestError, uploadAsset } from '@/api/client';
import type * as ClientModule from '@/api/client';
import type { Node, RunInputSchema } from '@/api/types';

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

function renderDialog(
  onSubmit = vi.fn(),
  schema: RunInputSchema = runInputSchema,
  initialInput?: Record<string, unknown>,
) {
  render(
    <RunInputDialog
      open
      onClose={vi.fn()}
      workflowId="wf_123"
      definitionVersion={4}
      runInputSchema={schema}
      initialInput={initialInput as never}
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

  it('carries a prior AssetRef through on Run Again without re-uploading', async () => {
    const onSubmit = renderDialog(vi.fn(), runInputSchema, {
      brief: 'A small ember creature',
      reference: assetRef,
    });
    const user = userEvent.setup();

    // The prior Asset preview renders immediately, with no upload interaction at all.
    expect(screen.getByText(/asset_123/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Create run' }));

    expect(onSubmit).toHaveBeenCalledWith({
      brief: 'A small ember creature',
      reference: assetRef,
    });
    expect(uploadAssetMock).not.toHaveBeenCalled();
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

describe('RunInputDialog — Input node labels and typography', () => {
  const inputNodes: Node[] = [
    {
      id: 'brief',
      type: 'text_input',
      name: 'Creative Brief',
      position: { x: 0, y: 0 },
      config: { inputKey: 'brief', label: 'Creative Brief', required: true },
    },
    {
      id: 'gen',
      type: 'image_generation',
      name: 'Image Generation',
      position: { x: 200, y: 0 },
      config: { prompt: 'x' },
    },
  ];
  // The Backend's runInputSchema carries no titles: the label comes from the Input node.
  const untitledSchema: RunInputSchema = {
    type: 'object',
    additionalProperties: false,
    properties: { brief: { type: 'string', minLength: 1 } },
    required: ['brief'],
  };

  it('maps each node declaring an inputKey and a label to that key, skipping nodes without them', () => {
    expect(runInputLabels(inputNodes)).toEqual({ brief: 'Creative Brief' });
    expect(runInputLabels(undefined)).toEqual({});
  });

  it('labels the field with the Input node label and describes it with key, requiredness and kind', async () => {
    const onSubmit = vi.fn();
    render(
      <RunInputDialog
        open
        onClose={vi.fn()}
        workflowId="wf_123"
        definitionVersion={4}
        runInputSchema={untitledSchema}
        inputLabels={runInputLabels(inputNodes)}
        onSubmit={onSubmit}
      />,
    );

    const field = screen.getByRole('textbox', { name: /Creative Brief/ });
    expect(field).toHaveAccessibleDescription('brief · required · text');
    const helper = screen.getByText('brief · required · text');
    expect(helper).toHaveClass('text-[12px]');
    expect(screen.getByText('Creative Brief')).toHaveClass('text-[14px]');

    // The label is display only: the submitted field name is still the input key.
    await userEvent.type(field, 'a lighthouse');
    await userEvent.click(screen.getByRole('button', { name: 'Create run' }));
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'a lighthouse' });
  });

  it('keeps every dialog text at or above the 12px floor and buttons at 14px', () => {
    renderDialog();
    const dialog = screen.getByRole('dialog');
    for (const element of dialog.querySelectorAll('*')) {
      expect(element.className.toString()).not.toMatch(/text-\[(10|11)px\]|text-xs/);
    }
    expect(screen.getByRole('button', { name: 'Create run' })).toHaveClass('text-sm');
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveClass('text-sm');
  });
});
