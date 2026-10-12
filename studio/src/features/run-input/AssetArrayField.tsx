import { X } from 'lucide-react';
import { useRef, useState } from 'react';
import type { ChangeEvent, DragEvent } from 'react';

import { photoSetStatus } from './photoSetCopy';
import { API_BASE, ApiRequestError, uploadAsset } from '@/api/client';
import type { AssetRef, JsonSchema } from '@/api/types';
import { Button } from '@/components/ui/button';

interface AssetArrayFieldProps {
  id: string;
  /** The field label's element id; it names the drop zone group. */
  labelId: string;
  /** The array property of the frozen runInputSchema; its `items` is an AssetRef object. */
  schema: JsonSchema;
  refs: readonly AssetRef[];
  onChange: (update: (prev: AssetRef[]) => AssetRef[]) => void;
  uploading: boolean;
  onUploadingChange: (uploading: boolean) => void;
  error: string | null | undefined;
  onErrorChange: (error: string | null) => void;
}

/**
 * A photo set for one run input property. Each selected or dropped file is uploaded as
 * its own immutable Asset first; only completed AssetRefs enter the value, in selection
 * order. The count, media type and size checks read the schema's own limits and exist
 * for responsiveness only: the Backend re-validates the whole array.
 */
export function AssetArrayField({
  id,
  labelId,
  schema,
  refs,
  onChange,
  uploading,
  onUploadingChange,
  error,
  onErrorChange,
}: AssetArrayFieldProps) {
  const fileInput = useRef<HTMLInputElement>(null);
  // File names of uploads made in this dialog, for the remove buttons' names only.
  const [fileNames, setFileNames] = useState<Record<string, string>>({});
  const { minItems, maxItems } = schema;
  const itemProperties = schema.items?.properties;
  const acceptedMediaTypes = itemProperties?.mediaType?.enum as string[] | undefined;
  const maxSizeBytes = itemProperties?.sizeBytes?.maximum;
  const atMax = maxItems !== undefined && refs.length >= maxItems;
  const disabled = uploading || atMax;

  const addFiles = (files: File[]) => {
    if (files.length === 0) return;
    onErrorChange(null);
    if (maxItems !== undefined && refs.length + files.length > maxItems) {
      onErrorChange(`You can add up to ${maxItems} photos.`);
      return;
    }
    const unsupported = acceptedMediaTypes
      ? files.find((file) => !acceptedMediaTypes.includes(file.type))
      : undefined;
    if (unsupported && acceptedMediaTypes) {
      onErrorChange(
        `${unsupported.name} is not a supported image type. Use ${acceptedMediaTypes.join(', ')}.`,
      );
      return;
    }
    const oversized =
      typeof maxSizeBytes === 'number' ? files.find((file) => file.size > maxSizeBytes) : undefined;
    if (oversized) {
      onErrorChange(`${oversized.name} is larger than ${maxSizeBytes} bytes.`);
      return;
    }

    onUploadingChange(true);
    void Promise.allSettled(files.map((file) => uploadAsset(file)))
      .then((results) => {
        const uploaded: AssetRef[] = [];
        const names: Record<string, string> = {};
        results.forEach((result, index) => {
          if (result.status !== 'fulfilled') return;
          uploaded.push(result.value);
          const file = files[index];
          if (file) names[result.value.assetId] = file.name;
        });
        if (uploaded.length > 0) {
          setFileNames((prev) => ({ ...prev, ...names }));
          onChange((prev) => [...prev, ...uploaded]);
        }
        const failed = results.find((result) => result.status === 'rejected');
        if (failed) {
          onErrorChange(
            failed.reason instanceof ApiRequestError ? failed.reason.message : 'Upload failed.',
          );
        }
      })
      .finally(() => onUploadingChange(false));
  };

  const onInputChange = (event: ChangeEvent<HTMLInputElement>) => {
    addFiles(Array.from(event.target.files ?? []));
    // Reset so choosing the same file again still fires a change.
    event.target.value = '';
  };

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    if (disabled) return;
    addFiles(Array.from(event.dataTransfer.files));
  };

  return (
    <>
      <div
        role="group"
        aria-labelledby={labelId}
        className="space-y-2 rounded-md border border-dashed border-[var(--border)] p-3 focus-within:ring-2 focus-within:ring-[var(--ring)]"
        onDragOver={(event) => event.preventDefault()}
        onDrop={onDrop}
      >
        {/* The visible control is the button; the native input only opens the picker. */}
        <input
          ref={fileInput}
          id={id}
          type="file"
          multiple
          tabIndex={-1}
          className="sr-only"
          accept={acceptedMediaTypes?.join(',')}
          disabled={disabled}
          onChange={onInputChange}
        />
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={disabled}
            onClick={() => fileInput.current?.click()}
          >
            Add photos
          </Button>
          <span className="text-[13px] text-[var(--muted-foreground)]">or drop them here</span>
        </div>
        <p className="text-[13px] text-[var(--muted-foreground)]">
          {uploading ? 'Uploading…' : photoSetStatus(refs.length, minItems, maxItems)}
        </p>
        {refs.length > 0 ? (
          <ul className="flex flex-wrap gap-2">
            {refs.map((ref, index) => (
              <li key={`${ref.assetId}-${index}`} className="relative h-16 w-16">
                {/* Preview only; the value written into run input is the AssetRef. */}
                <img
                  src={`${API_BASE}/assets/${encodeURIComponent(ref.assetId)}/content`}
                  alt={`Image ${index + 1}`}
                  title={ref.assetId}
                  className="h-16 w-16 rounded border border-[var(--border)] object-cover"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  aria-label={`Remove ${fileNames[ref.assetId] ?? `image ${index + 1}`}`}
                  className="absolute right-1 top-1 h-6 w-6 rounded-full bg-[var(--card)]"
                  onClick={() => {
                    onErrorChange(null);
                    onChange((prev) => prev.filter((_, i) => i !== index));
                  }}
                >
                  <X className="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
              </li>
            ))}
          </ul>
        ) : null}
      </div>
      {error ? (
        <p role="alert" className="text-[13px] text-red-500">
          {error}
        </p>
      ) : null}
    </>
  );
}
