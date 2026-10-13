import { ImageIcon, Upload, X } from 'lucide-react';
import { useState, type DragEvent } from 'react';

import type { Node } from '@/api/types';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import { RelativeTime } from '@/components/time/Time';
import { IconButton } from '@/components/ui/button';
import { useCopy } from '@/lib/copy';
import { shortCode } from '@/lib/short-code';
import { cn } from '@/lib/utils';
import { useEditor } from '@/features/editor/editor-context';
import {
  IMAGE_TYPES,
  STAGE_TYPES,
  assetUrl,
  lastRunImages,
  type PreviewImage,
} from '@/features/editor/model/images';
import { ImageViewer } from './ImageViewer';

/**
 * A card's image area: images staged for the next run, the last run's images, the reason
 * the last run made none, or a drop zone on an input that asks for images.
 */
export function CardMedia({ node }: { node: Node }) {
  const copy = useCopy();
  const t = copy.editor.canvas.imageArea;
  const { staged, lastRun, pickFiles, unstage, stageFiles } = useEditor();
  const [over, setOver] = useState(false);
  const [viewing, setViewing] = useState<number | null>(null);
  if (!IMAGE_TYPES.includes(node.type)) return null;

  const multi = node.type === 'media_brief';
  const stageable = STAGE_TYPES.includes(node.type);
  const runId = lastRun.snapshot?.run.id ?? null;
  const nr = lastRun.byNode.get(node.id);
  const stagedItems = staged[node.id] ?? [];
  const items: PreviewImage[] = stagedItems.length
    ? stagedItems.map((s) => ({
        key: s.ref.assetId,
        src: assetUrl(s.ref.assetId),
        name: s.name,
        mediaType: s.ref.mediaType,
      }))
    : lastRunImages(node, nr, runId, lastRun.snapshot?.run.input ?? null);
  const kind = stagedItems.length
    ? 'staged'
    : items.length
      ? 'lastrun'
      : nr?.status === 'FAILED' && !stageable
        ? 'failed'
        : stageable
          ? 'drop'
          : 'empty';

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    if (!stageable || !e.dataTransfer.types.includes('Files')) return;
    e.preventDefault();
    e.stopPropagation();
    setOver(false);
    stageFiles(node.id, Array.from(e.dataTransfer.files), !multi);
  };

  const caption = runId ? (
    <div className="flex min-h-control-xs flex-wrap items-center gap-2 text-sm text-fg-muted">
      <span className="font-medium whitespace-nowrap text-fg-secondary">
        {copy.editor.canvas.lastRunLabel}
      </span>
      <span className="font-mono">{shortCode(runId)}</span>
      <RelativeTime iso={lastRun.snapshot?.run.startedAt ?? lastRun.summary?.createdAt ?? null} />
    </div>
  ) : null;

  const thumb = (it: PreviewImage, i: number, single: boolean) => (
    <button
      key={it.key}
      type="button"
      tabIndex={-1}
      aria-label={t.open(it.name ?? t.image(i + 1))}
      onClick={(e) => {
        e.stopPropagation();
        setViewing(i);
      }}
      style={single ? { height: 144 } : undefined}
      className={cn(
        'nodrag relative flex w-full cursor-zoom-in items-center justify-center overflow-hidden rounded-md border border-border-subtle bg-surface-inset p-0 text-fg-muted hover:border-fg-muted',
        !single && 'aspect-square',
      )}
    >
      {it.src ? (
        <img
          src={it.src}
          alt=""
          loading="lazy"
          draggable={false}
          className="block size-full object-contain"
        />
      ) : (
        <span className="flex flex-col items-center gap-1 p-2 text-center text-sm text-fg-muted">
          <ImageIcon aria-hidden="true" className="size-icon-md" />
          {t.unavailable}
        </span>
      )}
    </button>
  );

  const images = (list: PreviewImage[]) => {
    if (!multi && list.length === 1 && list[0]) return thumb(list[0], 0, true);
    const shown = list.slice(0, 4);
    const more = list.length - shown.length;
    return (
      <div
        style={{ gridTemplateColumns: `repeat(${String(more > 0 ? 5 : 4)}, minmax(0, 1fr))` }}
        className="grid gap-1"
      >
        {shown.map((it, i) => thumb(it, i, false))}
        {more > 0 ? (
          <button
            type="button"
            tabIndex={-1}
            aria-label={t.more(list.length)}
            onClick={(e) => {
              e.stopPropagation();
              setViewing(4);
            }}
            className="nodrag flex aspect-square items-center justify-center rounded-md border border-border-subtle bg-surface-inset text-sm font-medium text-fg-secondary"
          >
            {`+${String(more)}`}
          </button>
        ) : null}
      </div>
    );
  };

  const pick = (label: string) => (
    <IconButton
      size="xs"
      label={label}
      tabIndex={-1}
      className="nodrag"
      onClick={(e) => {
        e.stopPropagation();
        pickFiles(node.id, !multi);
      }}
    >
      <Upload aria-hidden="true" className="size-icon-sm" />
    </IconButton>
  );

  let body;
  if (kind === 'staged') {
    body = (
      <>
        {images(items)}
        <div className="flex min-h-control-xs items-center gap-2 text-sm">
          <span className="font-medium text-accent-fg">{t.staged}</span>
          <span className="flex-1" />
          {pick(multi ? t.addOrReplace : t.replace)}
          <IconButton
            size="xs"
            label={multi ? t.removeMany : t.removeOne}
            tooltip={t.removeTip}
            tabIndex={-1}
            className="nodrag"
            onClick={(e) => {
              e.stopPropagation();
              unstage(node.id);
            }}
          >
            <X aria-hidden="true" className="size-icon-sm" />
          </IconButton>
        </div>
      </>
    );
  } else if (kind === 'lastrun') {
    body = (
      <>
        {images(items)}
        <div className="flex items-center gap-2">
          {caption}
          {stageable ? (
            <>
              <span className="flex-1" />
              {pick(multi ? t.chooseMany : t.chooseOne)}
            </>
          ) : null}
        </div>
      </>
    );
  } else if (kind === 'failed') {
    body = (
      <>
        <div
          style={{ height: 112 }}
          className="flex flex-col items-center justify-center gap-1 rounded-md border border-border-subtle bg-surface-inset p-2 text-center"
        >
          <StatusIndicator status={{ kind: 'step', value: 'FAILED' }} size="icon" />
          <span className="text-sm font-medium text-fg">{t.noImageCreated}</span>
        </div>
        {caption}
      </>
    );
  } else if (kind === 'drop') {
    body = (
      <div className="flex flex-col items-center gap-1 rounded-md border border-dashed border-border-control px-2 py-3 text-center text-fg-muted">
        <Upload aria-hidden="true" className="size-icon-md" />
        <span className="text-sm text-fg-secondary">
          {t.drop(multi)}{' '}
          <button
            type="button"
            tabIndex={-1}
            onClick={(e) => {
              e.stopPropagation();
              pickFiles(node.id, !multi);
            }}
            className="nodrag rounded-sm font-medium text-accent-fg hover:underline"
          >
            {t.choose(multi)}
          </button>
        </span>
        <span className="text-sm text-fg-muted">{t.dropHint}</span>
      </div>
    );
  } else {
    const text = !nr ? t.afterRun : nr.status === 'SUCCEEDED' ? t.noImage : t.didntRun;
    body = (
      <>
        <div
          style={{ height: 112 }}
          className="flex flex-col items-center justify-center gap-1 rounded-md border border-dashed border-border-subtle bg-surface-inset p-2 text-center text-fg-muted"
        >
          <ImageIcon aria-hidden="true" className="size-icon-md" />
          <span className="text-sm text-fg-muted">{text}</span>
        </div>
        {nr ? caption : null}
      </>
    );
  }

  return (
    <div
      data-node-field={stageable ? 'label' : undefined}
      onDragOver={(e) => {
        if (!stageable || !e.dataTransfer.types.includes('Files')) return;
        e.preventDefault();
        e.stopPropagation();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={onDrop}
      className={cn(
        'relative flex flex-col gap-1 rounded-md px-3 py-1',
        over && 'bg-surface-selected outline-2 -outline-offset-2 outline-focus outline-dashed',
      )}
    >
      {body}
      {viewing !== null ? (
        <ImageViewer
          title={node.name}
          subtitle={kind === 'staged' ? t.staged : copy.editor.canvas.lastRunLabel}
          items={items}
          index={viewing}
          onClose={() => setViewing(null)}
        />
      ) : null}
    </div>
  );
}
