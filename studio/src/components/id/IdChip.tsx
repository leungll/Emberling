import { Check, Copy } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router';

import { Tooltip } from '@/components/ui/tooltip';
import { copyText } from '@/lib/clipboard';
import { useCopy } from '@/lib/copy';
import { matchInId, shortCode } from '@/lib/short-code';
import { showToast } from '@/lib/toast';
import { cn } from '@/lib/utils';

/** How long the copy button shows its check and "Copied". */
export const COPIED_MS = 1500;

interface IdChipProps {
  id: string;
  /** The visible kind label ("Run ID"). Omit only where a column head or a fact label
   *  already names the ID. */
  kind?: string;
  /** Names the copy button and the link: "run ID" gives "Copy run ID". */
  label?: string;
  size?: 'sm' | 'md';
  /** Makes the code a link to the object. */
  to?: string;
  /** A search query to highlight. */
  match?: string;
  copy?: boolean;
  className?: string;
}

/**
 * An ID as a small bordered group, like a commit SHA: the eight-character code (full ID in
 * the tooltip) and an always-visible copy button that copies the full ID.
 */
export function IdChip({
  id,
  kind,
  label = 'ID',
  size = 'sm',
  to,
  match,
  copy: withCopy = true,
  className,
}: IdChipProps) {
  const text = useCopy();
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const code = shortCode(id);
  const m = matchInId(id, match);
  const shown = m ? (
    <>
      {m.before}
      <mark className="rounded-sm bg-surface-selected text-accent-fg">{m.hit}</mark>
      {m.after}
    </>
  ) : (
    code
  );

  const md = size === 'md';
  const valueClass = cn(
    'inline-flex items-center font-mono text-fg-secondary',
    md ? 'px-2 text-sm' : 'px-1.5 text-xs',
    withCopy ? 'rounded-l-sm' : 'rounded-sm',
  );
  const copyLabel = text.ids.copy(label);

  const onCopy = async () => {
    const ok = await copyText(id);
    if (!ok) {
      showToast(text.ids.copyFailed);
      return;
    }
    setCopied(true);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      setCopied(false);
    }, COPIED_MS);
  };

  return (
    <span
      className={cn(
        'inline-flex min-w-0 items-center gap-1.5 align-middle whitespace-nowrap',
        className,
      )}
    >
      {kind ? (
        <span className={cn('text-fg-muted', md ? 'text-sm' : 'text-xs')}>{kind}</span>
      ) : null}
      <span
        className={cn(
          // An outline, not a border, so the code and the copy button fill the control height.
          'inline-flex shrink-0 items-stretch rounded-sm bg-surface outline outline-1 -outline-offset-1 outline-border',
          md ? 'h-control-sm' : 'h-control-xs',
        )}
      >
        <Tooltip content={id}>
          {to ? (
            <Link
              to={to}
              aria-label={text.ids.open(label, id)}
              className={cn(
                valueClass,
                'no-underline underline-offset-3 hover:bg-surface-hover hover:text-accent-fg hover:underline focus-visible:relative focus-visible:z-local',
              )}
            >
              {shown}
            </Link>
          ) : (
            <span className={cn(valueClass, 'cursor-text')}>{shown}</span>
          )}
        </Tooltip>
        {withCopy ? (
          <Tooltip content={copied ? text.ids.copied : copyLabel}>
            <button
              type="button"
              aria-label={copied ? text.ids.copied : copyLabel}
              data-copied={copied || undefined}
              onClick={() => void onCopy()}
              className={cn(
                'inline-flex items-center justify-center rounded-r-sm border-l border-border text-fg-muted transition-colors duration-1 hover:bg-surface-hover hover:text-fg focus-visible:relative focus-visible:z-local',
                md ? 'px-2 [&_svg]:size-icon-sm' : 'px-1.5 [&_svg]:size-icon-xs',
                copied && 'text-st-succeeded-fg hover:text-st-succeeded-fg',
              )}
            >
              {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
            </button>
          </Tooltip>
        ) : null}
      </span>
    </span>
  );
}
