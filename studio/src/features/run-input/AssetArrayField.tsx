import { X } from 'lucide-react';
import type { ChangeEvent, DragEvent } from 'react';

import { API_BASE, ApiRequestError, uploadAsset } from '@/api/client';
import type { AssetRef, JsonSchema } from '@/api/types';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

interface AssetArrayFieldProps {
  id: string;
  helperId: string;
  /** The array property of the frozen runInputSchema; its `items` is an AssetRef object. */
  schema: JsonSchema;
  refs: readonly AssetRef[];
  onChange: (update: (prev: AssetRef[]) => AssetRef[]) => void;
  uploading: boolean;
  onUploadingChange: (uploading: boolean) => void;
  error: string | null | undefined;
  onErrorChange: (error: string | null) => void;
}

const plural = (count: number) => (count === 1 ? 'image' : 'images');

/**
 * A collection of uploaded images for one run input property. Each selected file is
 * uploaded as its own immutable Asset first; only completed AssetRefs enter the value, in
 * selection order. The item count, media type and size checks read the schema's own
 * limits and exist for responsiveness only: the Backend re-validates the whole array.
 */
export function AssetArrayField({
  id,
  helperId,
  schema,
  refs,
  onChange,
  uploading,
  onUploadingChange,
  error,
  onErrorChange,
}: AssetArrayFieldProps) {
  const { minItems, maxItems } = schema;
  const itemProperties = schema.items?.properties;
  const acceptedMediaTypes = itemProperties?.mediaType?.enum as string[] | undefined;
  const maxSizeBytes = itemProperties?.sizeBytes?.maximum;
  const atMax = maxItems !== undefined && refs.length >= maxItems;

  const addFiles = (files: File[]) => {
    if (files.length === 0) return;
    onErrorChange(null);
    if (maxItems !== undefined && refs.length + files.length > maxItems) {
      onErrorChange(`At most ${maxItems} ${plural(maxItems)}.`);
      return;
    }
    const unsupported = acceptedMediaTypes
      ? files.find((file) => !acceptedMediaTypes.includes(file.type))
      : undefined;
    if (unsupported && acceptedMediaTypes) {
      onErrorChange(
        `Unsupported media type "${unsupported.type}". Accepted: ${acceptedMediaTypes.join(', ')}.`,
      );
      return;
    }
    if (typeof maxSizeBytes === 'number' && files.some((file) => file.size > maxSizeBytes)) {
      onErrorChange(`Each image must be at most ${maxSizeBytes} bytes.`);
      return;
    }

    onUploadingChange(true);
    void Promise.allSettled(files.map((file) => uploadAsset(file)))
      .then((results) => {
        const uploaded = results.flatMap((result) =>
          result.status === 'fulfilled' ? [result.value] : [],
        );
        if (uploaded.length > 0) onChange((prev) => [...prev, ...uploaded]);
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
    if (uploading || atMax) return;
    addFiles(Array.from(event.dataTransfer.files));
  };

  const limits = [
    `${refs.length} selected`,
    ...(minItems !== undefined ? [`at least ${minItems}`] : []),
    ...(maxItems !== undefined ? [`at most ${maxItems}`] : []),
  ].join(' · ');

  return (
    <>
      <div
        className="space-y-2 rounded-md border border-dashed border-[var(--border)] p-2"
        onDragOver={(event) => event.preventDefault()}
        onDrop={onDrop}
      >
        <Input
          id={id}
          type="file"
          multiple
          aria-describedby={helperId}
          accept={acceptedMediaTypes?.join(',')}
          disabled={uploading || atMax}
          onChange={onInputChange}
        />
        <p className="text-[12px] text-[var(--muted-foreground)]">
          Choose images or drop them here.
        </p>
        <p className="text-[12px] text-[var(--muted-foreground)]">{limits}</p>
        {uploading ? (
          <p className="text-[12px] text-[var(--muted-foreground)]">Uploading…</p>
        ) : null}
        {refs.length > 0 ? (
          <ul className="flex flex-wrap gap-2">
            {refs.map((ref, index) => (
              <li key={`${ref.assetId}-${index}`} className="relative">
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
                  aria-label={`Remove image ${index + 1}`}
                  className="absolute -right-2 -top-2 h-6 w-6 rounded-full bg-[var(--card)]"
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
