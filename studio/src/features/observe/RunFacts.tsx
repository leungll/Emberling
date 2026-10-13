import { durationOf, type RunModel, type TraceElement } from './run-model';
import type { TokenUsage } from '@/api/types';
import { Duration, LiveDuration, Offset } from '@/components/time/Time';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { duration } from '@/lib/time';
import { useNow } from '@/lib/use-now';

/**
 * The small time and usage values of the run view. Every duration subtracts two
 * timestamps of one record; a record that has not ended counts up from its server start,
 * labelled "so far".
 */

/** An element's duration, or nothing when its record has none. */
export function ElementTime({ model, el }: { model: RunModel; el: TraceElement }) {
  const d = durationOf(model, el);
  if (!d) return null;
  return 'ms' in d ? <Duration ms={d.ms} /> : <LiveDuration start={d.live} />;
}

/** A moment of the run as an offset from its start. */
export function RunOffset({ model, iso }: { model: RunModel; iso: string | null | undefined }) {
  if (!iso) return null;
  return <Offset iso={iso} start={model.runStart} />;
}

/**
 * Time passed since a server timestamp, for a sentence that already says "so far" or
 * "for". The one place besides LiveDuration where the viewer's clock is used.
 */
export function Elapsed({ since }: { since: string }) {
  const now = useNow(1000);
  return (
    <span className="tabular-nums">{duration(now.getTime() - new Date(since).getTime())}</span>
  );
}

/** Tokens with the input and output split in the tooltip. */
export function Tokens({ usage }: { usage: TokenUsage | null | undefined }) {
  const copy = useCopy();
  if (!usage) return null;
  const d = copy.runs.details;
  return (
    <Tooltip content={d.tokensTip(usage.inputTokens, usage.outputTokens)}>
      <span className="cursor-default rounded-sm tabular-nums">{d.tokens(usage.totalTokens)}</span>
    </Tooltip>
  );
}
