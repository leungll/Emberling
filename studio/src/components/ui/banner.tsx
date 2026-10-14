import { CircleAlert, Hourglass, TriangleAlert, Info } from 'lucide-react';
import type { ReactNode } from 'react';

import { cn } from '@/lib/utils';

export type BannerTone = 'failed' | 'waiting' | 'caution';

const BANNER_TONE: Record<BannerTone, { bg: string; icon: string; Icon: typeof CircleAlert }> = {
  failed: { bg: 'bg-st-failed-bg', icon: 'text-st-failed-fg', Icon: CircleAlert },
  waiting: { bg: 'bg-st-waiting-bg', icon: 'text-st-waiting-fg', Icon: Hourglass },
  caution: { bg: 'bg-caution-bg', icon: 'text-caution-fg', Icon: TriangleAlert },
};

/**
 * A full-width line at the top of the content: a run's failure or wait summary, or a live
 * connection that is reconnecting or stopped. Icon, one sentence that wraps, at most one
 * action.
 */
export function Banner({
  tone,
  children,
  action,
  className,
}: {
  tone: BannerTone;
  children: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  const t = BANNER_TONE[tone];
  return (
    <div
      role={tone === 'failed' ? 'alert' : 'status'}
      className={cn(
        'flex min-h-10 items-center gap-3 border-b border-border px-4 py-2 text-md text-fg',
        t.bg,
        className,
      )}
    >
      <t.Icon className={cn('size-icon-md shrink-0', t.icon)} aria-hidden="true" />
      <div className="min-w-0 flex-1 wrap-anywhere">{children}</div>
      {action}
    </div>
  );
}

export type CalloutTone = 'neutral' | 'danger' | 'caution';

/** A note inside a panel or dialog: icon and one sentence, at most two actions. */
export function Callout({
  tone = 'neutral',
  children,
  className,
}: {
  tone?: CalloutTone;
  children: ReactNode;
  className?: string;
}) {
  const Icon = tone === 'danger' ? CircleAlert : tone === 'caution' ? TriangleAlert : Info;
  return (
    <div
      className={cn(
        'flex gap-3 rounded-lg px-4 py-3 text-md',
        tone === 'danger' && 'bg-st-failed-bg text-fg',
        tone === 'caution' && 'bg-caution-bg text-fg',
        tone === 'neutral' && 'bg-surface-hover text-fg-secondary',
        className,
      )}
    >
      <Icon
        aria-hidden="true"
        className={cn(
          'mt-0.5 size-icon-md shrink-0',
          tone === 'danger'
            ? 'text-st-failed-fg'
            : tone === 'caution'
              ? 'text-caution-fg'
              : 'text-fg-muted',
        )}
      />
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}
