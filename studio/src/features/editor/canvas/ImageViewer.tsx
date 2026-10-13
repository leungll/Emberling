import { ChevronLeft, ChevronRight, ImageIcon } from 'lucide-react';
import { useState } from 'react';

import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { useCopy } from '@/lib/copy';
import type { PreviewImage } from '@/features/editor/model/images';

/** One of a step's images at full size, with Previous and Next for a set. */
export function ImageViewer({
  title,
  subtitle,
  items,
  index,
  onClose,
}: {
  title: string;
  subtitle: string;
  items: PreviewImage[];
  index: number;
  onClose: () => void;
}) {
  const copy = useCopy();
  const t = copy.editor.canvas.imageArea;
  const [at, setAt] = useState(Math.min(index, Math.max(0, items.length - 1)));
  const it = items[at];
  const many = items.length > 1;
  return (
    <Dialog
      open
      onClose={onClose}
      title={title}
      description={many ? `${subtitle}, ${t.image(at + 1)} / ${String(items.length)}` : subtitle}
      size="lg"
      footer={
        <>
          {many ? (
            <>
              <Button
                variant="secondary"
                onClick={() => setAt((at - 1 + items.length) % items.length)}
              >
                <ChevronLeft aria-hidden="true" />
                {copy.editor.viewer.previous}
              </Button>
              <Button variant="secondary" onClick={() => setAt((at + 1) % items.length)}>
                {copy.editor.viewer.next}
                <ChevronRight aria-hidden="true" />
              </Button>
            </>
          ) : null}
          <Button data-dialog-primary onClick={onClose}>
            {copy.done}
          </Button>
        </>
      }
    >
      <div
        style={{ minHeight: 240 }}
        className="flex items-center justify-center rounded-md border border-border-subtle bg-surface-inset"
      >
        {it?.src ? (
          <img
            src={it.src}
            alt={it.name ?? t.image(at + 1)}
            style={{ maxHeight: '60vh' }}
            className="block max-w-full object-contain"
          />
        ) : (
          <span className="flex flex-col items-center gap-1 p-4 text-sm text-fg-muted">
            <ImageIcon aria-hidden="true" className="size-icon-lg" />
            {t.unavailable}
          </span>
        )}
      </div>
    </Dialog>
  );
}
