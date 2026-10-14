import {
  CircleAlert,
  CircleCheck,
  CircleDashed,
  Circle,
  CircleMinus,
  Hourglass,
  LoaderCircle,
  type LucideIcon,
} from 'lucide-react';

import { statusInfo, type StatusInfo, type StatusSubject, type Tone } from './status-info';
import { Tooltip, TooltipMeta } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

const TONE_ICON: Record<Tone, LucideIcon> = {
  running: LoaderCircle,
  waiting: Hourglass,
  succeeded: CircleCheck,
  failed: CircleAlert,
  queued: CircleDashed,
  inactive: Circle,
};

const ICON_COLOUR: Record<Tone, string> = {
  running: 'text-st-running-fg',
  waiting: 'text-st-waiting-fg',
  succeeded: 'text-st-succeeded-fg',
  failed: 'text-st-failed-fg',
  queued: 'text-st-queued-fg',
  inactive: 'text-st-inactive-fg',
};

const PILL: Record<Tone, string> = {
  running: 'bg-st-running-bg text-st-running-fg',
  waiting: 'bg-st-waiting-bg text-st-waiting-fg',
  succeeded: 'bg-st-succeeded-bg text-st-succeeded-fg',
  failed: 'bg-st-failed-bg text-st-failed-fg',
  queued: 'bg-st-queued-bg text-st-queued-fg',
  inactive: 'bg-st-inactive-bg text-st-inactive-fg',
};

export type StatusSize = 'row' | 'header' | 'icon';

interface StatusIndicatorProps {
  status: StatusSubject;
  size?: StatusSize;
  pill?: boolean;
  /** Colours the word as well as the icon (list tables, run headers, run list items). */
  colourLabel?: boolean;
  /** Makes the status a Tab stop so keyboard users reach its tooltip. */
  focusable?: boolean;
  /** An ended wait: inactive tone with the hourglass outline. */
  endedWait?: boolean;
  help?: string;
  className?: string;
}

/**
 * The only way a status is shown: icon and word from one tone table. The tooltip carries
 * the help sentence and the raw value; the visible word is never a raw enum.
 */
export function StatusIndicator({
  status,
  size = 'row',
  pill = false,
  colourLabel = false,
  focusable = false,
  endedWait = false,
  help,
  className,
}: StatusIndicatorProps) {
  const base: StatusInfo = statusInfo(status);
  const info: StatusInfo = endedWait ? { ...base, tone: 'inactive', icon: 'hourglass' } : base;
  const Icon =
    info.icon === 'circle-minus'
      ? CircleMinus
      : info.icon === 'hourglass'
        ? Hourglass
        : TONE_ICON[info.tone];
  const helpText = help ?? info.help;
  const iconOnly = size === 'icon';

  const body = (
    <span
      data-tone={info.tone}
      data-size={size}
      tabIndex={focusable && helpText ? 0 : undefined}
      role={iconOnly ? 'img' : undefined}
      aria-label={iconOnly ? info.label : undefined}
      className={cn(
        'inline-flex items-center gap-1.5 align-middle whitespace-nowrap',
        size === 'header' ? 'text-md font-medium' : 'text-sm',
        info.tone === 'inactive' && !pill ? 'text-fg-muted' : 'text-fg',
        colourLabel && !pill && ICON_COLOUR[info.tone],
        pill && cn('h-control-xs rounded-md px-2', PILL[info.tone]),
        focusable && 'min-h-control-xs cursor-default rounded-sm',
        className,
      )}
    >
      <Icon
        aria-hidden="true"
        className={cn(
          'shrink-0',
          size === 'header' ? 'size-icon-md' : 'size-icon-sm',
          ICON_COLOUR[info.tone],
          info.tone === 'running' && !info.icon && 'animate-spin-slow',
        )}
      />
      {iconOnly ? null : <span>{info.label}</span>}
    </span>
  );

  if (!helpText) return body;
  return <Tooltip content={<TooltipMeta body={helpText} meta={info.raw} />}>{body}</Tooltip>;
}
