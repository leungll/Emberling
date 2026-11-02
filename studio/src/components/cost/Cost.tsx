import type { Cost, RunCost } from '@/api/types';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { formatCost, formatCostTotals } from '@/lib/cost';

/** One call's reported charge, with the model that reported it in the tooltip. */
export function Money({ cost }: { cost: Cost | null | undefined }) {
  const copy = useCopy();
  if (!cost) return null;
  return (
    <Tooltip content={copy.runs.details.costTip(cost.source)}>
      <span className="cursor-default rounded-sm tabular-nums">{formatCost(cost)}</span>
    </Tooltip>
  );
}

/**
 * A Run's cost: the known totals on one line and, under them, the calls that reported no
 * price. Nothing is shown for a Run with neither, so a Run that made no priced call stays
 * quiet. The two never merge into one figure: an unpriced call is a count, not a guess.
 */
export function RunCostValue({ cost }: { cost: RunCost | null | undefined }) {
  const copy = useCopy();
  if (!cost || (cost.totals.length === 0 && cost.unknownCalls === 0)) return null;
  return (
    <span className="inline-flex flex-col items-end gap-0.5 tabular-nums">
      {cost.totals.length > 0 ? <span>{formatCostTotals(cost.totals)}</span> : null}
      {cost.unknownCalls > 0 ? (
        <span className="text-xs text-fg-muted">
          {copy.runs.page.costUnknown(cost.unknownCalls)}
        </span>
      ) : null}
    </span>
  );
}
