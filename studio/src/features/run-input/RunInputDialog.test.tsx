import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ComponentProps } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { RunInputDialog } from './RunInputDialog';
import { runInputLabels } from './runInputLabels';
import { ApiRequestError, uploadAsset } from '@/api/client';
import type * as ClientModule from '@/api/client';
import type { JsonObject, Node, RunInputSchema } from '@/api/types';
import { usePrefsStore } from '@/stores/prefs-store';

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof ClientModule>();
  return { ...actual, uploadAsset: vi.fn() };
});

const uploadAssetMock = vi.mocked(uploadAsset);

beforeEach(() => {
  uploadAssetMock.mockReset();
  usePrefsStore.setState({ timeZonePref: 'UTC' });
});

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

const requiredImageSchema: RunInputSchema = { ...runInputSchema, required: ['brief', 'reference'] };

type Props = ComponentProps<typeof RunInputDialog>;

function renderDialog(props: Partial<Omit<Props, 'onSubmit'>> = {}) {
  const onSubmit = vi.fn();
  const view = render(
    <RunInputDialog
      open
      onClose={vi.fn()}
      workflowName="Photo Effect"
      definitionVersion={4}
      runInputSchema={runInputSchema}
      {...props}
      onSubmit={onSubmit}
    />,
  );
  return { onSubmit, ...view };
}

const assetRef = { assetId: 'asset_123', mediaType: 'image/png', sizeBytes: 4, sha256: 'abc123' };

function pngFile(name = 'reference.png') {
  return new File(['ref'], name, { type: 'image/png' });
}

const startButton = () => screen.getByRole('button', { name: 'Start run' });
const referenceInput = () => screen.getByLabelText(/^Reference/, { selector: 'input' });

describe('RunInputDialog', () => {
  it('is titled by the workflow name and version, never its raw ID', () => {
    renderDialog();
    expect(screen.getByRole('dialog', { name: 'Photo Effect' })).toBeInTheDocument();
    expect(screen.getByText('Workflow version 4')).toBeInTheDocument();
    expect(screen.queryByText(/wf_/)).not.toBeInTheDocument();
  });

  it('falls back to a plain title when the workflow has no name', () => {
    renderDialog({ workflowName: '' });
    expect(screen.getByRole('dialog', { name: 'Untitled workflow' })).toBeInTheDocument();
  });

  it('renders a required string field from the frozen schema', () => {
    renderDialog();
    expect(screen.getByRole('textbox', { name: /Creative Brief/ })).toBeRequired();
  });

  it('says when the workflow takes no run input, and still starts', async () => {
    const { onSubmit } = renderDialog({ runInputSchema: undefined });
    expect(screen.getByText('This workflow takes no run input.')).toBeInTheDocument();
    await userEvent.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith({});
  });

  it('shows an error and no fields when the bound version could not be loaded', () => {
    renderDialog({ runInputSchema: undefined, loadFailed: true });
    expect(screen.getByRole('alert')).toHaveTextContent('Couldn’t load the run form.');
    expect(screen.queryByRole('button', { name: 'Start run' })).not.toBeInTheDocument();
    // The footer's Close sits beside the header's.
    expect(screen.getAllByRole('button', { name: 'Close' })).toHaveLength(2);
  });

  it('keeps Start run unavailable while the bound version loads', () => {
    renderDialog({ runInputSchema: undefined, loading: true });
    expect(startButton()).toBeDisabled();
  });

  it('submits only the filled fields: the Backend injects no defaults', async () => {
    const { onSubmit } = renderDialog();
    const user = userEvent.setup();
    await user.type(screen.getByRole('textbox', { name: /Creative Brief/ }), 'A small ember');
    await user.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'A small ember' });
  });

  it('starts with ⌘↵ from inside the dialog', async () => {
    const { onSubmit } = renderDialog();
    const brief = screen.getByRole('textbox', { name: /Creative Brief/ });
    await userEvent.type(brief, 'x');
    fireEvent.keyDown(brief, { key: 'Enter', metaKey: true });
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'x' });
  });

  it('names a missing required field under it and focuses it', async () => {
    const { onSubmit } = renderDialog();
    await userEvent.click(startButton());
    expect(onSubmit).not.toHaveBeenCalled();
    const brief = screen.getByRole('textbox', { name: /Creative Brief/ });
    expect(brief).toHaveAttribute('aria-invalid', 'true');
    expect(brief).toHaveAccessibleDescription('Creative Brief is required.');
    expect(brief).toHaveFocus();
    // One error needs no summary.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('sums up two or more errors above the form', async () => {
    renderDialog({ runInputSchema: requiredImageSchema });
    await userEvent.click(startButton());
    expect(screen.getByRole('alert')).toHaveTextContent('Fix 2 fields to start the run.');
    expect(screen.getByText('Add an image to start the run.')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /Creative Brief/ })).toHaveFocus();
  });

  it('shows why the run didn’t start', () => {
    renderDialog({ submitError: new TypeError('Failed to fetch') });
    expect(screen.getByRole('alert')).toHaveTextContent('The server didn’t respond.');
  });

  it('shows the pending state while the run starts', () => {
    renderDialog({ submitting: true });
    const button = screen.getByRole('button', { name: 'Starting…' });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByRole('textbox', { name: /Creative Brief/ })).toBeDisabled();
  });
});

describe('RunInputDialog — JSON values', () => {
  const schema: RunInputSchema = {
    type: 'object',
    properties: { change: { type: 'string' }, settings: { type: 'object' } },
    required: ['change'],
  };
  const change = { title: 'Fix login', diff_text: '- a\n+ b', meta: { retries: 2 } };

  it('shows a JSON object held in a text input as labelled fields and writes edits back', async () => {
    const { onSubmit } = renderDialog({
      runInputSchema: schema,
      source: { runId: 'run_1', startedAt: null, input: { change: JSON.stringify(change) } },
    });
    const user = userEvent.setup();

    expect(screen.getByRole('radiogroup', { name: 'Show as Change' })).toBeInTheDocument();
    const title = screen.getByRole('textbox', { name: 'Title' });
    // A multi-line string keeps its real line breaks.
    expect(screen.getByRole('textbox', { name: 'Diff text' })).toHaveValue('- a\n+ b');
    await user.clear(title);
    await user.type(title, 'Fix logout');
    await user.clear(screen.getByRole('textbox', { name: 'Retries' }));
    await user.type(screen.getByRole('textbox', { name: 'Retries' }), '5');

    await user.click(screen.getByRole('radio', { name: 'JSON' }));
    const json = screen.getByRole('textbox', { name: /^Change/ });
    expect(JSON.parse((json as HTMLTextAreaElement).value)).toEqual({
      ...change,
      title: 'Fix logout',
      meta: { retries: 5 },
    });

    await user.click(startButton());
    const sent = onSubmit.mock.calls[0]?.[0] as JsonObject;
    expect(typeof sent.change).toBe('string');
    expect(JSON.parse(sent.change as string)).toEqual({
      ...change,
      title: 'Fix logout',
      meta: { retries: 5 },
    });
  });

  it('places invalid JSON by line and column, and won’t show it as fields', async () => {
    const { onSubmit } = renderDialog({
      runInputSchema: schema,
      source: { runId: 'run_1', startedAt: null, input: { change: '{"a": 1}' } },
    });
    const user = userEvent.setup();
    await user.click(screen.getByRole('radio', { name: 'JSON' }));
    const json = screen.getByRole('textbox', { name: /^Change/ });
    await user.clear(json);
    // user-event reads braces as key descriptors; doubled they type literally.
    await user.type(json, '{{\n"a": 1\n"b": 2\n}');
    await user.tab();

    expect(json).toHaveAccessibleDescription(/^Line 3, column 1: .+\.$/);
    await user.click(screen.getByRole('radio', { name: 'Fields' }));
    expect(screen.getByText('Fix the JSON to show it as fields.')).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: 'JSON' })).toBeChecked();

    await user.click(startButton());
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('parses an object-typed field into the run input', async () => {
    const { onSubmit } = renderDialog({ runInputSchema: schema });
    const user = userEvent.setup();
    await user.type(screen.getByRole('textbox', { name: /^Change/ }), 'plain text');
    await user.type(screen.getByRole('textbox', { name: /^Settings/ }), '{{"depth": 2}');
    await user.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith({ change: 'plain text', settings: { depth: 2 } });
  });
});

describe('RunInputDialog — Run again and last input', () => {
  const input = { brief: 'A small ember', reference: assetRef };
  const source = { runId: 'run_9f8e7d6c5b4a', startedAt: '2026-10-12T09:30:00Z', input };

  it('starts from the earlier run’s input and resubmits its image without uploading', async () => {
    const { onSubmit } = renderDialog({ source, latestVersion: 4 });
    expect(
      screen.getByText('Starts with the input of run 9f8e7d6c from 2026-10-12 09:30.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /Creative Brief/ })).toHaveValue('A small ember');
    expect(screen.getByText('Image from the earlier run, 4 B')).toBeInTheDocument();

    await userEvent.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith(input);
    expect(uploadAssetMock).not.toHaveBeenCalled();
  });

  it('says when Run again uses an older version than the latest', () => {
    renderDialog({ source, definitionVersion: 3, latestVersion: 5 });
    expect(
      screen.getByText(
        'Runs version 3 again with the input of run 9f8e7d6c from 2026-10-12 09:30. Changes saved after that run aren’t included.',
      ),
    ).toBeInTheDocument();
  });

  it('fills a new run from the last run’s input on request', async () => {
    const { onSubmit } = renderDialog({ lastRun: source });
    const user = userEvent.setup();
    expect(screen.getByRole('textbox', { name: /Creative Brief/ })).toHaveValue('');
    await user.click(screen.getByRole('button', { name: 'Use last run’s input' }));
    expect(screen.getByRole('textbox', { name: /Creative Brief/ })).toHaveValue('A small ember');
    await user.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith(input);
  });

  it('offers the last run’s input only for a new run', () => {
    renderDialog({ source, lastRun: source });
    expect(screen.queryByRole('button', { name: 'Use last run’s input' })).not.toBeInTheDocument();
  });

  it('starts empty again after closing and reopening', async () => {
    const { rerender } = renderDialog();
    await userEvent.type(screen.getByRole('textbox', { name: /Creative Brief/ }), 'draft');
    const props = {
      onClose: vi.fn(),
      workflowName: 'Photo Effect',
      definitionVersion: 4,
      runInputSchema,
      onSubmit: vi.fn(),
    };
    rerender(<RunInputDialog open={false} {...props} />);
    rerender(<RunInputDialog open {...props} />);
    expect(screen.getByRole('textbox', { name: /Creative Brief/ })).toHaveValue('');
  });
});

describe('RunInputDialog — one image', () => {
  it('uploads an image and writes the AssetRef into the run input', async () => {
    uploadAssetMock.mockResolvedValue(assetRef);
    const { onSubmit } = renderDialog();
    const user = userEvent.setup();

    await user.type(screen.getByRole('textbox', { name: /Creative Brief/ }), 'A small ember');
    await user.upload(referenceInput(), pngFile());

    await screen.findByText('reference.png, 4 B');
    await user.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'A small ember', reference: assetRef });
    expect(JSON.stringify(onSubmit.mock.calls[0]?.[0])).not.toMatch(/blob:|downloadUrl/);
  });

  it('shows the Backend’s reason for a rejected upload and keeps the field empty', async () => {
    uploadAssetMock.mockRejectedValue(
      new ApiRequestError(400, {
        error: { code: 'VALIDATION_FAILED', message: 'unsupported asset media type' },
      }),
    );
    renderDialog();
    await userEvent.upload(referenceInput(), pngFile());
    expect(
      await screen.findByText('Couldn’t upload reference.png: unsupported asset media type.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Choose image' })).toBeInTheDocument();
  });

  it('says to try again when the upload got no answer', async () => {
    uploadAssetMock.mockRejectedValue(new TypeError('Failed to fetch'));
    renderDialog();
    await userEvent.upload(referenceInput(), pngFile());
    expect(
      await screen.findByText('Couldn’t upload reference.png. Try again.'),
    ).toBeInTheDocument();
  });

  it('names the image by file and size, never its asset ID, with Replace and Remove', async () => {
    uploadAssetMock.mockResolvedValue(assetRef);
    const { onSubmit } = renderDialog();
    const user = userEvent.setup();
    const fileInput = referenceInput();

    // The native input is hidden; a focusable button opens the picker.
    expect(fileInput).toHaveClass('sr-only');
    expect(fileInput).toHaveAttribute('tabindex', '-1');
    const click = vi.spyOn(fileInput, 'click');
    await user.click(screen.getByRole('button', { name: 'Choose image' }));
    expect(click).toHaveBeenCalled();

    await user.upload(fileInput, pngFile());
    await screen.findByText('reference.png, 4 B');
    expect(screen.getByRole('img', { name: 'reference.png' })).toHaveAttribute(
      'src',
      '/api/assets/asset_123/content',
    );
    expect(screen.queryByText(/asset_123/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Replace reference.png' })).toBeEnabled();

    await user.click(screen.getByRole('button', { name: 'Remove reference.png' }));
    expect(screen.queryByText('reference.png, 4 B')).not.toBeInTheDocument();

    await user.type(screen.getByRole('textbox', { name: /Creative Brief/ }), 'brief');
    await user.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'brief' });
  });

  it('checks the schema size limit before uploading', async () => {
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
    renderDialog({ runInputSchema: limited });
    expect(screen.getByText('or drop it here (Up to 2 B)')).toBeInTheDocument();
    await userEvent.upload(referenceInput(), pngFile('big.png'));
    expect(screen.getByText('big.png is larger than 2 B.')).toBeInTheDocument();
    expect(uploadAssetMock).not.toHaveBeenCalled();
  });

  it('keeps Start run unavailable while an upload is in flight', async () => {
    let resolveUpload!: (value: typeof assetRef) => void;
    uploadAssetMock.mockReturnValue(
      new Promise((resolve) => {
        resolveUpload = resolve;
      }),
    );
    const { onSubmit } = renderDialog();
    const user = userEvent.setup();
    await user.type(screen.getByRole('textbox', { name: /Creative Brief/ }), 'b');
    await user.upload(referenceInput(), pngFile());

    await waitFor(() => expect(startButton()).toHaveAttribute('aria-disabled', 'true'));
    expect(startButton()).toHaveAccessibleDescription('Wait for the upload to finish.');
    expect(screen.getByText('Uploading reference.png…')).toBeInTheDocument();
    await user.click(startButton());
    expect(onSubmit).not.toHaveBeenCalled();

    resolveUpload(assetRef);
    await screen.findByText('reference.png, 4 B');
    expect(startButton()).not.toHaveAttribute('aria-disabled');
  });
});

describe('RunInputDialog — Input step labels and type sizes', () => {
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
  // The Backend's runInputSchema carries no titles: the label comes from the Input step.
  const untitledSchema: RunInputSchema = {
    type: 'object',
    additionalProperties: false,
    properties: { brief: { type: 'string', minLength: 1 } },
    required: ['brief'],
  };

  it('maps each step declaring an inputKey and a label to that key, skipping the rest', () => {
    expect(runInputLabels(inputNodes)).toEqual({ brief: 'Creative Brief' });
    expect(runInputLabels(undefined)).toEqual({});
  });

  it('falls back to the step name when the step declaring the input has no label', () => {
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

  it('labels the field with the Input step label; the submitted key is unchanged', async () => {
    const { onSubmit } = renderDialog({
      runInputSchema: untitledSchema,
      inputLabels: runInputLabels(inputNodes),
    });
    await userEvent.type(screen.getByRole('textbox', { name: /Creative Brief/ }), 'a lighthouse');
    await userEvent.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'a lighthouse' });
  });

  it('keeps every text at or above the 13 px body size', () => {
    renderDialog({ runInputSchema: requiredImageSchema });
    for (const element of screen.getByRole('dialog').querySelectorAll('*')) {
      expect(element.getAttribute('class') ?? '').not.toMatch(/text-\[(10|11|12)px\]|text-xs/);
    }
  });
});

describe('RunInputDialog — photo set', () => {
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
    assetId: `asset_photo_${String(n)}`,
    mediaType: 'image/png',
    sizeBytes: 3,
    sha256: `sha_${String(n)}`,
  });
  const photosInput = () => screen.getByLabelText(/^Photos/, { selector: 'input' });

  function renderPhotos(input?: JsonObject) {
    return renderDialog({
      runInputSchema: photoSetSchema,
      definitionVersion: 1,
      ...(input ? { source: { runId: 'run_1', startedAt: null, input } } : {}),
    });
  }

  it('renders an array of AssetRefs as a multi-image picker with the schema’s limits', () => {
    renderPhotos();
    const picker = photosInput();
    expect(picker).toHaveAttribute('type', 'file');
    expect(picker).toHaveAttribute('multiple');
    expect(picker).toHaveAttribute('accept', 'image/jpeg,image/png,image/webp');
    expect(screen.getByRole('button', { name: 'Add photos' })).toBeEnabled();
    expect(screen.getByRole('group', { name: /Photos/ })).toHaveTextContent(
      'or drop them here (PNG, JPEG or WebP)',
    );
    expect(screen.getByText('Add at least 1 photo.')).toBeInTheDocument();
  });

  it('uploads each image and submits the AssetRefs in selection order', async () => {
    uploadAssetMock
      .mockResolvedValueOnce(refFor(1))
      .mockResolvedValueOnce(refFor(2))
      .mockResolvedValueOnce(refFor(3));
    const { onSubmit } = renderPhotos();
    const user = userEvent.setup();

    await user.type(screen.getByRole('textbox', { name: /Brief/ }), 'clay look');
    await user.upload(photosInput(), [pngFile('a.png'), pngFile('b.png'), pngFile('c.png')]);

    expect(await screen.findAllByRole('img', { name: /^[abc]\.png$/ })).toHaveLength(3);
    expect(screen.getByText('3 photos added. That’s the limit.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add photos' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Remove b.png' })).toBeInTheDocument();

    await user.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith({
      brief: 'clay look',
      photos: [refFor(1), refFor(2), refFor(3)],
    });
  });

  it('prefills Run again’s photos, removes one and keeps the rest in order', async () => {
    const { onSubmit } = renderPhotos({ brief: 'clay', photos: [refFor(1), refFor(2), refFor(3)] });
    const user = userEvent.setup();
    const thumbs = screen.getAllByRole('img', { name: /from the earlier run$/ });
    expect(thumbs).toHaveLength(3);
    expect(thumbs[0]).toHaveAttribute('src', '/api/assets/asset_photo_1/content');

    await user.click(screen.getByRole('button', { name: 'Remove Photo 2 from the earlier run' }));
    await user.click(startButton());
    expect(onSubmit).toHaveBeenCalledWith({ brief: 'clay', photos: [refFor(1), refFor(3)] });
    expect(uploadAssetMock).not.toHaveBeenCalled();
  });

  it('blocks Start run below the schema minimum', async () => {
    const { onSubmit } = renderPhotos();
    await userEvent.type(screen.getByRole('textbox', { name: /Brief/ }), 'clay');
    await userEvent.click(startButton());
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText('Add at least 1 photo to start the run.')).toBeInTheDocument();
  });

  it('rejects a selection over the schema maximum without uploading it', async () => {
    renderPhotos({ brief: 'clay', photos: [refFor(1), refFor(2)] });
    await userEvent.upload(photosInput(), [pngFile('c.png'), pngFile('d.png')]);
    expect(screen.getByText('You can add up to 3 photos.')).toBeInTheDocument();
    expect(uploadAssetMock).not.toHaveBeenCalled();
  });

  it('rejects a media type outside the schema enum without uploading it', async () => {
    renderPhotos();
    const user = userEvent.setup({ applyAccept: false });
    await user.upload(photosInput(), new File(['gif'], 'a.gif', { type: 'image/gif' }));
    expect(
      screen.getByText('a.gif isn’t a supported image. Use PNG, JPEG or WebP.'),
    ).toBeInTheDocument();
    expect(uploadAssetMock).not.toHaveBeenCalled();
  });

  it('keeps the uploads that succeeded and reports the one that failed', async () => {
    uploadAssetMock
      .mockResolvedValueOnce(refFor(1))
      .mockRejectedValueOnce(new TypeError('Failed to fetch'));
    renderPhotos();
    await userEvent.upload(photosInput(), [pngFile('a.png'), pngFile('b.png')]);
    expect(await screen.findByText('Couldn’t upload b.png. Try again.')).toBeInTheDocument();
    const group = screen.getByRole('group', { name: /Photos/ });
    expect(within(group).getAllByRole('img')).toHaveLength(1);
  });
});
