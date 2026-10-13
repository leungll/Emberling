import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { RunInputDialog } from './RunInputDialog';
import { photoSetStatus } from './photoSetCopy';
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
  it('names the bound workflow and version, never its raw ID', () => {
    render(
      <RunInputDialog
        open
        onClose={vi.fn()}
        workflowId="wf_123"
        workflowName="Photo Effect Template"
        definitionVersion={4}
        runInputSchema={runInputSchema}
        onSubmit={vi.fn()}
      />,
    );
    expect(screen.getByText('Photo Effect Template')).toBeInTheDocument();
    expect(screen.getByText('Version 4')).toBeInTheDocument();
    expect(screen.queryByText(/wf_123/)).not.toBeInTheDocument();
    expect(screen.queryByText(/revalidates|frozen schema/)).not.toBeInTheDocument();
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
    const fileInput = screen.getByLabelText(/reference/i, { selector: 'input' });
    await user.upload(fileInput, pngFile());

    await screen.findByText('reference.png, 0.1 KB');
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
    const fileInput = screen.getByLabelText(/reference/i, { selector: 'input' });
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
    expect(screen.getByText('Image from the previous run, 0.1 KB')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Create run' }));

    expect(onSubmit).toHaveBeenCalledWith({
      brief: 'A small ember creature',
      reference: assetRef,
    });
    expect(uploadAssetMock).not.toHaveBeenCalled();
  });

  it('shows an uploaded image by file name and size, never its asset ID, with Replace and Remove', async () => {
    uploadAssetMock.mockReset();
    uploadAssetMock.mockResolvedValue(assetRef);
    const onSubmit = renderDialog();
    const user = userEvent.setup();
    const fileInput = screen.getByLabelText(/reference/i, { selector: 'input' });

    // The native input is hidden; a focusable button opens the picker.
    expect(fileInput).toHaveClass('sr-only');
    expect(fileInput).toHaveAttribute('tabindex', '-1');
    const click = vi.spyOn(fileInput, 'click');
    await user.click(screen.getByRole('button', { name: 'Choose image' }));
    expect(click).toHaveBeenCalled();

    await user.upload(fileInput, pngFile());
    await screen.findByText('reference.png, 0.1 KB');
    expect(screen.getByRole('img', { name: 'reference.png' })).toHaveAttribute(
      'src',
      '/api/assets/asset_123/content',
    );
    expect(screen.queryByText(/asset_123/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Replace reference.png' })).toBeEnabled();

    await user.click(screen.getByRole('button', { name: 'Remove reference.png' }));
    expect(screen.queryByText('reference.png, 0.1 KB')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Choose image' })).toBeInTheDocument();

    await user.type(screen.getByLabelText(/Creative Brief/), 'brief');
    await user.click(screen.getByRole('button', { name: 'Create run' }));
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'brief' });
  });

  it('reports a schema size limit in human units', async () => {
    uploadAssetMock.mockReset();
    const limited: RunInputSchema = {
      ...runInputSchema,
      properties: {
        ...runInputSchema.properties,
        reference: {
          ...runInputSchema.properties!.reference!,
          properties: {
            ...runInputSchema.properties!.reference!.properties,
            sizeBytes: { type: 'integer', minimum: 0, maximum: 2 },
          },
        },
      },
    };
    renderDialog(vi.fn(), limited);
    await userEvent.upload(
      screen.getByLabelText(/reference/i, { selector: 'input' }),
      pngFile('big.png'),
    );
    expect(screen.getByRole('alert')).toHaveTextContent('big.png is larger than 0.1 KB.');
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

    const fileInput = screen.getByLabelText(/reference/i, { selector: 'input' });
    await user.upload(fileInput, pngFile());

    await waitFor(() => expect(screen.getByRole('button', { name: 'Create run' })).toBeDisabled());

    resolveUpload(assetRef);
    await screen.findByText('reference.png, 0.1 KB');
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

  it('falls back to the step name when the node declaring the input has no label', () => {
    const nodes: Node[] = [
      {
        id: 'photos',
        type: 'media_brief',
        name: 'Photo Set',
        position: { x: 0, y: 0 },
        config: { inputKey: 'photos' },
      },
      {
        id: 'ref',
        type: 'image_input',
        name: 'Reference',
        position: { x: 0, y: 0 },
        config: { inputKey: 'reference', label: '  ' },
      },
    ];
    expect(runInputLabels(nodes)).toEqual({ photos: 'Photo Set', reference: 'Reference' });
  });

  it('labels the field with the Input node label and shows no key or type line', async () => {
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
    expect(screen.queryByText(/required ·|· text/)).not.toBeInTheDocument();
    expect(screen.getByText('Creative Brief')).toHaveClass('text-md');

    // The label is display only: the submitted field name is still the input key.
    await userEvent.type(field, 'a lighthouse');
    await userEvent.click(screen.getByRole('button', { name: 'Create run' }));
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'a lighthouse' });
  });

  it('keeps every dialog text at or above the 12px floor and buttons at 14px', () => {
    renderDialog();
    const dialog = screen.getByRole('dialog');
    for (const element of dialog.querySelectorAll('*')) {
      expect(element.getAttribute('class') ?? '').not.toMatch(/text-\[(10|11)px\]|text-xs/);
    }
    expect(screen.getByRole('button', { name: 'Create run' })).toHaveClass('text-md');
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveClass('text-md');
  });
});

describe('RunInputDialog — image collection input', () => {
  const assetRefItem = {
    type: 'object' as const,
    additionalProperties: false,
    properties: {
      assetId: { type: 'string' as const, minLength: 1 },
      mediaType: {
        type: 'string' as const,
        minLength: 1,
        enum: ['image/jpeg', 'image/png', 'image/webp'],
      },
      sizeBytes: { type: 'integer' as const, minimum: 0 },
      sha256: { type: 'string' as const, minLength: 1 },
    },
    required: ['assetId', 'mediaType', 'sizeBytes', 'sha256'],
  };
  /** Shaped like the Backend output for a Text Input plus a photo set of 1 to 3 images. */
  const photoSetSchema: RunInputSchema = {
    type: 'object',
    additionalProperties: false,
    properties: {
      brief: { type: 'string' },
      photos: { type: 'array', items: assetRefItem, minItems: 1, maxItems: 3 },
    },
    required: ['brief', 'photos'],
  };

  const refFor = (n: number) => ({
    assetId: `asset_photo_${n}`,
    mediaType: 'image/png',
    sizeBytes: 3,
    sha256: `sha_${n}`,
  });

  function renderPhotoDialog(onSubmit = vi.fn(), initialInput?: Record<string, unknown>) {
    render(
      <RunInputDialog
        open
        onClose={vi.fn()}
        workflowId="wf_photo"
        definitionVersion={1}
        runInputSchema={photoSetSchema}
        initialInput={initialInput as never}
        onSubmit={onSubmit}
      />,
    );
    return onSubmit;
  }

  it('renders an array of AssetRefs as a multi-image picker, not a text box', () => {
    renderPhotoDialog();
    const picker = screen.getByLabelText(/photos/, { selector: 'input' });
    expect(picker).toHaveAttribute('type', 'file');
    expect(picker).toHaveAttribute('multiple');
    expect(picker).toHaveAttribute('accept', 'image/jpeg,image/png,image/webp');
    // The native input is hidden; the visible control is a focusable button.
    expect(picker).toHaveClass('sr-only');
    expect(picker).toHaveAttribute('tabindex', '-1');
    expect(screen.getByRole('button', { name: 'Add photos' })).toBeEnabled();
    expect(screen.getByRole('group', { name: /photos/ })).toHaveTextContent('or drop them here');
    // The limit sentence uses the schema's own minItems.
    expect(screen.getByText('Add at least 1 photo.')).toBeInTheDocument();
  });

  it('opens the file picker from the Add photos button', async () => {
    renderPhotoDialog();
    const picker = screen.getByLabelText(/photos/, { selector: 'input' });
    const click = vi.spyOn(picker, 'click');
    await userEvent.click(screen.getByRole('button', { name: 'Add photos' }));
    expect(click).toHaveBeenCalled();
  });

  it('describes the count in a plain sentence against the schema limits', () => {
    expect(photoSetStatus(0, 1, 12)).toBe('Add at least 1 photo.');
    expect(photoSetStatus(3, 1, 12)).toBe('3 photos added. Add up to 12.');
    expect(photoSetStatus(1, 1, 12)).toBe('1 photo added. Add up to 12.');
    expect(photoSetStatus(12, 1, 12)).toBe("12 photos added. That's the limit.");
    expect(photoSetStatus(1, 2, undefined)).toBe('1 photo added. Add at least 2.');
    expect(photoSetStatus(0, undefined, undefined)).toBe('No photos added.');
  });

  it('uploads each selected image and submits the AssetRefs in selection order', async () => {
    uploadAssetMock.mockReset();
    uploadAssetMock
      .mockResolvedValueOnce(refFor(1))
      .mockResolvedValueOnce(refFor(2))
      .mockResolvedValueOnce(refFor(3));
    const onSubmit = renderPhotoDialog();
    const user = userEvent.setup();

    await user.type(screen.getByRole('textbox', { name: /brief/ }), 'clay look');
    await user.upload(screen.getByLabelText(/photos/, { selector: 'input' }), [
      pngFile('a.png'),
      pngFile('b.png'),
      pngFile('c.png'),
    ]);

    expect(await screen.findAllByRole('img', { name: /^Image \d$/ })).toHaveLength(3);
    expect(uploadAssetMock).toHaveBeenCalledTimes(3);
    expect(screen.getByText("3 photos added. That's the limit.")).toBeInTheDocument();
    // At the maximum, the picker stops accepting more images.
    expect(screen.getByLabelText(/photos/, { selector: 'input' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Add photos' })).toBeDisabled();
    // Images uploaded in this dialog are named by their file name.
    expect(screen.getByRole('button', { name: 'Remove b.png' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Create run' }));
    expect(onSubmit).toHaveBeenCalledWith({
      brief: 'clay look',
      photos: [refFor(1), refFor(2), refFor(3)],
    });
  });

  it('removes one image and keeps the others in order', async () => {
    const onSubmit = renderPhotoDialog(vi.fn(), {
      brief: 'clay look',
      photos: [refFor(1), refFor(2), refFor(3)],
    });
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: 'Remove image 2' }));
    expect(screen.getAllByRole('img', { name: /^Image \d$/ })).toHaveLength(2);

    await user.click(screen.getByRole('button', { name: 'Create run' }));
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'clay look', photos: [refFor(1), refFor(3)] });
  });

  it('prefills the photo set on Run Again and resubmits it without uploading', async () => {
    uploadAssetMock.mockReset();
    const onSubmit = renderPhotoDialog(vi.fn(), {
      brief: 'clay look',
      photos: [refFor(1), refFor(2)],
    });
    const user = userEvent.setup();

    const thumbnails = screen.getAllByRole('img', { name: /^Image \d$/ });
    expect(thumbnails).toHaveLength(2);
    expect(thumbnails[0]).toHaveAttribute('src', '/api/assets/asset_photo_1/content');

    await user.click(screen.getByRole('button', { name: 'Create run' }));
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'clay look', photos: [refFor(1), refFor(2)] });
    expect(uploadAssetMock).not.toHaveBeenCalled();
  });

  it('blocks Run below the schema minimum', async () => {
    const onSubmit = renderPhotoDialog();
    const user = userEvent.setup();

    await user.type(screen.getByRole('textbox', { name: /brief/ }), 'clay look');
    await user.click(screen.getByRole('button', { name: 'Create run' }));

    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByRole('alert')).toHaveTextContent('Add at least 1 photo to start the run.');
  });

  it('rejects a selection that would exceed the schema maximum without uploading it', async () => {
    uploadAssetMock.mockReset();
    renderPhotoDialog(vi.fn(), { brief: 'clay look', photos: [refFor(1), refFor(2)] });
    const user = userEvent.setup();

    await user.upload(screen.getByLabelText(/photos/, { selector: 'input' }), [
      pngFile('c.png'),
      pngFile('d.png'),
    ]);

    expect(screen.getByRole('alert')).toHaveTextContent('You can add up to 3 photos.');
    expect(uploadAssetMock).not.toHaveBeenCalled();
    expect(screen.getAllByRole('img', { name: /^Image \d$/ })).toHaveLength(2);
  });

  it('rejects a media type outside the schema enum without uploading it', async () => {
    uploadAssetMock.mockReset();
    renderPhotoDialog();
    const user = userEvent.setup({ applyAccept: false });

    await user.upload(
      screen.getByLabelText(/photos/, { selector: 'input' }),
      new File(['gif'], 'a.gif', { type: 'image/gif' }),
    );

    expect(screen.getByRole('alert')).toHaveTextContent('a.gif is not a supported image type.');
    expect(uploadAssetMock).not.toHaveBeenCalled();
  });

  it('keeps successful uploads and reports a failed one', async () => {
    uploadAssetMock.mockReset();
    uploadAssetMock.mockResolvedValueOnce(refFor(1)).mockRejectedValueOnce(
      new ApiRequestError(400, {
        error: { code: 'VALIDATION_FAILED', message: 'unsupported asset media type' },
      }),
    );
    renderPhotoDialog();
    const user = userEvent.setup();

    await user.upload(screen.getByLabelText(/photos/, { selector: 'input' }), [
      pngFile('a.png'),
      pngFile('b.png'),
    ]);

    expect(await screen.findByRole('alert')).toHaveTextContent('unsupported asset media type');
    expect(screen.getAllByRole('img', { name: /^Image \d$/ })).toHaveLength(1);
  });
});
