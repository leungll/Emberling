import { Upload } from 'lucide-react';
import { useState, type DragEvent } from 'react';

import type { Node } from '@/api/types';
import { Button } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { useEditor } from '@/features/editor/editor-context';
import { ImageViewer } from '@/features/editor/canvas/ImageViewer';
import { MB, formatNames } from '@/features/editor/model/fields';
import { STAGE_TYPES, assetUrl, type PreviewImage } from '@/features/editor/model/images';
import { fieldLabel } from '@/features/editor/model/steps';

/**
 * The field the Run dialog shows for an input step. A text field is an inert copy; an image
 * field is live, and what is added to it is staged for the next run started from this page.
 */
export function RunPreview({ node }: { node: Node }) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const { staged, pickFiles, unstage, stageFiles } = useEditor();
  const [over, setOver] = useState(false);
  const [viewing, setViewing] = useState<number | null>(null);
  const c = node.config;
  const multi = node.type === 'media_brief';
  const live = STAGE_TYPES.includes(node.type);
  const label = multi ? node.name : fieldLabel(node);
  const required = multi || c.required === true;

  const head = (
    <div className="text-sm font-medium text-fg">
      {label}
      {required ? null : (
        <span className="font-normal text-fg-muted"> {copy.editor.panel.optional}</span>
      )}
    </div>
  );

  if (!live) {
    const lo = typeof c.minLength === 'number' ? c.minLength : undefined;
    const hi = typeof c.maxLength === 'number' ? c.maxLength : undefined;
    const length = f.length(lo, hi);
    return (
      <div
        aria-hidden="true"
        className="mb-4 flex flex-col gap-1.5 rounded-lg border border-dashed border-border bg-surface-inset p-3"
      >
        {head}
        <div className="flex h-control-md items-center rounded-md border border-border-control bg-surface px-3 text-sm text-fg-placeholder">
          {node.name === label ? f.previewType(label) : f.previewTypeHere}
        </div>
        {length ? <div className="text-sm text-fg-muted">{length}</div> : null}
      </div>
    );
  }

  const items = staged[node.id] ?? [];
  const images: PreviewImage[] = items.map((s) => ({
    key: s.ref.assetId,
    src: assetUrl(s.ref.assetId),
    name: s.name,
    mediaType: s.ref.mediaType,
  }));
  const maxBytes = typeof c.maxSizeBytes === 'number' ? c.maxSizeBytes : 0;
  const accepts =
    node.type === 'image_input'
      ? f.accepts(
          formatNames(c.acceptedMediaTypes),
          maxBytes ? `${String(+(maxBytes / MB).toFixed(2))} MB` : '',
        )
      : '';
  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    if (!e.dataTransfer.types.includes('Files')) return;
    e.preventDefault();
    setOver(false);
    stageFiles(node.id, Array.from(e.dataTransfer.files), !multi);
  };

  return (
    <div
      data-run-preview
      className="mb-4 flex flex-col gap-1.5 rounded-lg border border-dashed border-border bg-surface-inset p-3"
    >
      {head}
      {images.length ? (
        <>
          <div className="grid grid-cols-4 gap-2">
            {images.map((it, i) => (
              <button
                key={it.key}
                type="button"
                aria-label={copy.editor.canvas.imageArea.open(
                  it.name ?? copy.editor.canvas.imageArea.image(i + 1),
                )}
                onClick={() => setViewing(i)}
                className="flex aspect-square cursor-zoom-in items-center justify-center overflow-hidden rounded-md border border-border-subtle bg-surface p-0"
              >
                <img src={it.src ?? undefined} alt="" className="block size-full object-cover" />
              </button>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="flex-1 text-sm font-medium text-fg-secondary">
              {copy.editor.canvas.imageArea.staged}
            </span>
            <Button variant="secondary" size="sm" onClick={() => pickFiles(node.id, !multi)}>
              <Upload aria-hidden="true" />
              {multi ? f.previewAddPhotos : f.previewReplace}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => unstage(node.id)}>
              {f.previewRemove}
            </Button>
          </div>
        </>
      ) : (
        <div
          onDragOver={(e) => {
            if (!e.dataTransfer.types.includes('Files')) return;
            e.preventDefault();
            setOver(true);
          }}
          onDragLeave={() => setOver(false)}
          onDrop={onDrop}
          className={cn(
            'flex min-h-control-md flex-wrap items-center justify-center gap-1.5 rounded-md border border-dashed border-border-control bg-surface px-3 py-4 text-sm text-fg-muted',
            over && 'border-focus bg-surface-selected',
          )}
        >
          <Upload aria-hidden="true" className="size-icon-sm" />
          <span>{f.previewDrop(multi)}</span>
          <Button
            variant="link"
            size="sm"
            onClick={() => pickFiles(node.id, !multi)}
            className="min-h-control-xs"
          >
            {f.previewChoose(multi)}
          </Button>
        </div>
      )}
      {accepts ? <div className="text-sm text-fg-muted">{accepts}</div> : null}
      <div className="text-sm text-fg-muted">
        {images.length ? f.previewNotSaved : f.previewUsed(multi)}
      </div>
      {viewing !== null ? (
        <ImageViewer
          title={label}
          subtitle={node.name}
          items={images}
          index={viewing}
          onClose={() => setViewing(null)}
        />
      ) : null}
    </div>
  );
}
