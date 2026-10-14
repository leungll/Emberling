import { ImageIcon, Upload, X } from 'lucide-react';
import { useRef, useState, type ChangeEvent, type DragEvent } from 'react';

import { mediaList, photoCount, type RunField } from './run-form';
import { API_BASE, ApiRequestError, uploadAsset } from '@/api/client';
import type { AssetRef } from '@/api/types';
import { Button, IconButton } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import { bytes } from '@/lib/time';
import { cn } from '@/lib/utils';

interface ImageFieldProps {
  field: RunField;
  /** The field label points here: the native file input, so the label opens the picker. */
  id: string;
  /** The field label's id: it names the drop zone and the thumbnail list. */
  labelId: string;
  /** Linked to the error under the field. */
  describedBy: string | undefined;
  invalid: boolean;
  value: AssetRef | AssetRef[] | null;
  onChange: (next: AssetRef | AssetRef[] | null) => void;
  onError: (message: string | null) => void;
  onUploadingChange: (uploading: boolean) => void;
}

function contentUrl(ref: AssetRef): string {
  return `${API_BASE}/assets/${encodeURIComponent(ref.assetId)}/content`;
}

/**
 * One image or a photo set. Each chosen or dropped file is uploaded as its own immutable
 * Asset first; only completed AssetRefs enter the value. The media type, size and count
 * checks read the schema's own limits and are for responsiveness only: the Backend
 * re-validates the input. The asset ID is never shown; an image is named by the file
 * chosen here, or as the earlier run's when Run again filled it.
 */
export function ImageField({
  field,
  id,
  labelId,
  describedBy,
  invalid,
  value,
  onChange,
  onError,
  onUploadingChange,
}: ImageFieldProps) {
  const copy = useCopy();
  const r = copy.runDialog;
  const fileInput = useRef<HTMLInputElement>(null);
  const [names, setNames] = useState<Record<string, string>>({});
  const [uploading, setUploading] = useState<string | null>(null);
  const [over, setOver] = useState(false);
  const many = field.kind === 'photos';
  const refs: AssetRef[] = Array.isArray(value) ? value : value ? [value] : [];
  const atMax = many && field.maxItems !== undefined && refs.length >= field.maxItems;
  const types = field.types ? mediaList(field.types) : '';
  const hint = r.hint(types, field.maxBytes ? bytes(field.maxBytes) : '');

  const nameOf = (ref: AssetRef, i: number) =>
    names[ref.assetId] ?? (many ? r.photos.earlier(i + 1) : r.image.earlier);

  const setBusy = (name: string | null) => {
    setUploading(name);
    onUploadingChange(name !== null);
  };

  const addFiles = (chosen: File[]) => {
    const files = many ? chosen : chosen.slice(0, 1);
    if (files.length === 0) return;
    if (many && field.maxItems !== undefined && refs.length + files.length > field.maxItems) {
      onError(r.tooMany(field.maxItems));
      return;
    }
    const badType = field.types ? files.find((f) => !field.types?.includes(f.type)) : undefined;
    if (badType) {
      onError(r.badType(badType.name, types));
      return;
    }
    const tooBig = field.maxBytes ? files.find((f) => f.size > (field.maxBytes ?? 0)) : undefined;
    if (tooBig && field.maxBytes) {
      onError(r.tooBig(tooBig.name, bytes(field.maxBytes)));
      return;
    }
    onError(null);
    const first = files[0];
    setBusy(files.length === 1 && first ? first.name : r.photos.word(files.length));
    void Promise.allSettled(files.map((f) => uploadAsset(f))).then((results) => {
      const uploaded: AssetRef[] = [];
      const added: Record<string, string> = {};
      let failed: string | null = null;
      results.forEach((result, i) => {
        const file = files[i];
        if (result.status === 'fulfilled') {
          uploaded.push(result.value);
          if (file) added[result.value.assetId] = file.name;
        } else if (failed === null) {
          // The Backend's own reason when it rejected the file; otherwise it didn't answer.
          const reason: unknown = result.reason;
          const name = file?.name ?? '';
          failed =
            reason instanceof ApiRequestError && reason.message
              ? r.uploadRejected(name, reason.message)
              : r.uploadFailed(name);
        }
      });
      setNames((prev) => ({ ...prev, ...added }));
      if (uploaded.length > 0) onChange(many ? [...refs, ...uploaded] : (uploaded[0] ?? null));
      if (failed !== null) onError(failed);
      setBusy(null);
    });
  };

  const onInput = (e: ChangeEvent<HTMLInputElement>) => {
    addFiles(Array.from(e.target.files ?? []));
    // Reset so choosing the same file again still fires a change.
    e.target.value = '';
  };
  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setOver(false);
    if (uploading || atMax) return;
    addFiles(Array.from(e.dataTransfer.files));
  };
  const remove = (i: number) => {
    onError(null);
    onChange(many ? refs.filter((_, j) => j !== i) : null);
  };

  const pick = (
    <Button
      variant="secondary"
      size="sm"
      disabled={uploading !== null || atMax}
      onClick={() => fileInput.current?.click()}
    >
      <Upload aria-hidden="true" />
      {many ? r.photos.add : r.image.choose}
    </Button>
  );

  const single = !many ? refs[0] : undefined;
  // Below the minimum, the error under the field already says how many to add.
  const short = field.minItems !== undefined && refs.length < field.minItems;

  return (
    <div
      role="group"
      aria-labelledby={labelId}
      aria-describedby={describedBy}
      aria-invalid={invalid || undefined}
      onDragOver={(e) => {
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOver(false);
      }}
      onDrop={onDrop}
      className={cn(
        'flex flex-col gap-3 rounded-md border border-dashed bg-surface-inset p-3',
        invalid ? 'border-danger' : 'border-border-control',
        over && 'border-focus bg-surface-selected',
      )}
    >
      {/* The visible control is the button; the native input only opens the picker. */}
      <input
        ref={fileInput}
        id={id}
        type="file"
        tabIndex={-1}
        className="sr-only"
        multiple={many}
        accept={field.types?.join(',')}
        disabled={uploading !== null || atMax}
        onChange={onInput}
      />

      {many && refs.length > 0 ? (
        <ul aria-labelledby={labelId} className="m-0 flex list-none flex-wrap gap-2 p-0">
          {refs.map((ref, i) => {
            const name = nameOf(ref, i);
            return (
              <li
                key={`${ref.assetId}-${String(i)}`}
                className="relative size-16 overflow-hidden rounded-md border border-border bg-checker"
              >
                <img src={contentUrl(ref)} alt={name} className="size-full object-contain" />
                <IconButton
                  size="xs"
                  label={r.image.removeNamed(name)}
                  onClick={() => {
                    remove(i);
                  }}
                  className="absolute top-0.5 right-0.5 bg-surface"
                >
                  <X aria-hidden="true" />
                </IconButton>
              </li>
            );
          })}
        </ul>
      ) : null}

      {single ? (
        <div className="flex items-center gap-3">
          <span className="relative size-16 shrink-0 overflow-hidden rounded-md border border-border bg-checker">
            <img
              src={contentUrl(single)}
              alt={nameOf(single, 0)}
              className="size-full object-contain"
            />
          </span>
          <div className="flex min-w-0 flex-col gap-2">
            <span className="text-sm wrap-anywhere text-fg">
              {r.sizeOf(nameOf(single, 0), bytes(single.sizeBytes))}
            </span>
            <div className="flex gap-2">
              <Button
                variant="secondary"
                size="sm"
                aria-label={r.image.replaceNamed(nameOf(single, 0))}
                disabled={uploading !== null}
                onClick={() => fileInput.current?.click()}
              >
                {r.image.replace}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                aria-label={r.image.removeNamed(nameOf(single, 0))}
                disabled={uploading !== null}
                onClick={() => {
                  remove(0);
                }}
              >
                {r.image.remove}
              </Button>
            </div>
          </div>
        </div>
      ) : uploading !== null && !many ? null : !many || !atMax ? (
        <div className="flex flex-wrap items-center gap-2 text-sm text-fg-muted">
          {pick}
          <span>{r.dropHint(many ? r.photos.drop : r.image.drop, hint)}</span>
        </div>
      ) : (
        <div className="flex items-center gap-2">{pick}</div>
      )}

      {uploading !== null ? (
        <p aria-live="polite" className="m-0 flex items-center gap-1.5 text-sm text-fg-muted">
          <ImageIcon aria-hidden="true" className="size-icon-sm" />
          {r.uploading(uploading)}
        </p>
      ) : many && !(invalid && short) ? (
        <p
          aria-live="polite"
          className={cn('m-0 text-sm', short ? 'text-fg-secondary' : 'text-fg-muted')}
        >
          {photoCount(refs.length, field.minItems, field.maxItems)}
        </p>
      ) : null}
    </div>
  );
}
