import { X } from 'lucide-react';
import { useRef, useState } from 'react';
import type { ChangeEvent, DragEvent } from 'react';

import { API_BASE } from '@/api/client';
import type { AssetRef } from '@/api/types';
import { Button } from '@/components/ui/button';
import { formatBytes } from '@/lib/format';

interface AssetFieldProps {
  id: string;
  /** The field label's element id; it names the drop zone group. */
  labelId: string;
  accept: string | undefined;
  /** The completed upload, or a prior Run's AssetRef on Run Again. */
  value: AssetRef | undefined;
  uploading: boolean;
  /** A chosen or dropped file to upload, or undefined to clear the field. */
  onFile: (file: File | undefined) => void;
  error: string | null | undefined;
}

/**
 * One uploaded image, styled like the photo set field. The Asset ID is never shown: the
 * image is identified by the file name chosen in this dialog, or as the previous run's
 * image when Run Again prefilled it.
 */
export function AssetField({
  id,
  labelId,
  accept,
  value,
  uploading,
  onFile,
  error,
}: AssetFieldProps) {
  const fileInput = useRef<HTMLInputElement>(null);
  const [fileName, setFileName] = useState<string | null>(null);
  const name = fileName ?? 'Image from the previous run';

  const choose = (file: File | undefined) => {
    setFileName(file?.name ?? null);
    onFile(file);
  };

  const onInputChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    // Reset so choosing the same file again still fires a change.
    event.target.value = '';
    if (file) choose(file);
  };

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    if (uploading) return;
    const file = event.dataTransfer.files[0];
    if (file) choose(file);
  };

  return (
    <>
      <div
        role="group"
        aria-labelledby={labelId}
        className="space-y-2 rounded-md border border-dashed border-border p-3 focus-within:ring-2 focus-within:ring-focus"
        onDragOver={(event) => event.preventDefault()}
        onDrop={onDrop}
      >
        {/* The visible control is the button; the native input only opens the picker. */}
        <input
          ref={fileInput}
          id={id}
          type="file"
          tabIndex={-1}
          className="sr-only"
          accept={accept}
          disabled={uploading}
          onChange={onInputChange}
        />
        {value ? (
          <div className="flex items-center gap-3">
            <div className="relative h-16 w-16 shrink-0">
              {/* Preview only; the value written into run input is the AssetRef. */}
              <img
                src={`${API_BASE}/assets/${encodeURIComponent(value.assetId)}/content`}
                alt={name}
                className="h-16 w-16 rounded-sm border border-border object-cover"
              />
              <Button
                type="button"
                variant="secondary"
                size="sm"
                aria-label={`Remove ${fileName ?? 'image'}`}
                className="absolute right-1 top-1 size-control-xs rounded-full bg-surface px-0"
                onClick={() => choose(undefined)}
              >
                <X className="h-icon-sm w-icon-sm" aria-hidden="true" />
              </Button>
            </div>
            <div className="min-w-0 space-y-1">
              <p className="truncate text-sm text-fg">
                {name}, {formatBytes(value.sizeBytes)}
              </p>
              <Button
                type="button"
                variant="secondary"
                aria-label={`Replace ${fileName ?? 'image'}`}
                disabled={uploading}
                onClick={() => fileInput.current?.click()}
              >
                Replace
              </Button>
            </div>
          </div>
        ) : (
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="secondary"
              disabled={uploading}
              onClick={() => fileInput.current?.click()}
            >
              Choose image
            </Button>
            <span className="text-sm text-fg-muted">or drop it here</span>
          </div>
        )}
        {uploading ? <p className="text-sm text-fg-muted">Uploading…</p> : null}
      </div>
      {error ? (
        <p role="alert" className="text-sm text-danger-fg">
          {error}
        </p>
      ) : null}
    </>
  );
}
