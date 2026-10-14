import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import {
  absoluteTime,
  duration,
  exactSeconds,
  exactTime,
  listTime,
  offsetText,
  secondsText,
  untilText,
} from '@/lib/time';
import { useNow } from '@/lib/use-now';
import { cn } from '@/lib/utils';
import { useTimeZone } from '@/stores/prefs-store';

/**
 * Time elements. Every one re-renders when the viewer changes time zone and shows seconds
 * and the zone only in its tooltip, which adds detail but never information a task needs.
 * Pages never format times themselves.
 */

const TIME = 'cursor-default rounded-sm tabular-nums';

function Missing() {
  const copy = useCopy();
  return <span className="text-fg-muted">{copy.time.missing}</span>;
}

/** A time in a list: relative within 24 hours, otherwise "YYYY-MM-DD HH:mm". */
export function RelativeTime({
  iso,
  className,
}: {
  iso: string | null | undefined;
  className?: string;
}) {
  const zone = useTimeZone();
  const now = useNow();
  if (!iso) return <Missing />;
  return (
    <Tooltip content={exactTime(iso, now, zone)}>
      <time dateTime={iso} className={cn(TIME, className)}>
        {listTime(iso, now, zone)}
      </time>
    </Tooltip>
  );
}

/** A time that always reads absolute: run titles, Started in a run overview. */
export function Timestamp({
  iso,
  className,
}: {
  iso: string | null | undefined;
  className?: string;
}) {
  const zone = useTimeZone();
  const now = useNow();
  if (!iso) return <Missing />;
  return (
    <Tooltip content={exactTime(iso, now, zone)}>
      <time dateTime={iso} className={cn(TIME, className)}>
        {absoluteTime(iso, zone)}
      </time>
    </Tooltip>
  );
}

/** A moment inside a run, as an offset from the run's start: "+3.2 s". */
export function Offset({
  iso,
  start,
  className,
}: {
  iso: string | null | undefined;
  start: string;
  className?: string;
}) {
  const zone = useTimeZone();
  if (!iso) return <Missing />;
  return (
    <Tooltip content={exactSeconds(iso, zone)}>
      <time dateTime={iso} className={cn(TIME, className)}>
        {offsetText(new Date(iso).getTime() - new Date(start).getTime())}
      </time>
    </Tooltip>
  );
}

/** A duration the Backend reported, with its exact seconds in the tooltip. */
export function Duration({ ms, className }: { ms: number | null | undefined; className?: string }) {
  const copy = useCopy();
  if (ms === null || ms === undefined) {
    return <span className="text-fg-muted">{copy.facts.notRecorded}</span>;
  }
  return (
    <Tooltip content={secondsText(ms)}>
      <span className={cn(TIME, className)}>{duration(ms)}</span>
    </Tooltip>
  );
}

/**
 * The one place the viewer's clock makes a duration: a record that has not ended counts up
 * from its server start time, marked "so far". When it ends, show `Duration` with the
 * server's value instead.
 */
export function LiveDuration({ start, className }: { start: string; className?: string }) {
  const copy = useCopy();
  const now = useNow(1000);
  const text = copy.time.soFar(duration(now.getTime() - new Date(start).getTime()));
  return (
    <Tooltip content={text}>
      <span className={cn(TIME, className)}>{text}</span>
    </Tooltip>
  );
}

/** A deadline: "in 4 min 00 s" or "12 s ago", with the absolute time in the tooltip. */
export function Deadline({ iso, className }: { iso: string; className?: string }) {
  const zone = useTimeZone();
  const now = useNow(1000);
  return (
    <Tooltip content={exactTime(iso, now, zone)}>
      <time dateTime={iso} className={cn(TIME, className)}>
        {untilText(iso, now)}
      </time>
    </Tooltip>
  );
}
