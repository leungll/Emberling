import type { Cost, CostTotal } from '@/api/types';

/**
 * Formats a decimal cost amount in its currency for display. The amount arrives as text
 * with at most six fraction digits, so the Number conversion is exact for every value the
 * backend stores. Small charges keep every reported digit: a $0.000032 model call must not
 * round to $0.00 and read as free. A currency the formatter does not know, or text that is
 * not a number, falls back to the raw amount with its code.
 */
export function formatCost({ amount, currency }: Pick<Cost, 'amount' | 'currency'>): string {
  const value = Number(amount);
  if (!/^\d+(\.\d+)?$/.test(amount) || !Number.isFinite(value)) return `${amount} ${currency}`;
  try {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency,
      minimumFractionDigits: 2,
      maximumFractionDigits: 6,
    }).format(value);
  } catch {
    return `${amount} ${currency}`;
  }
}

/** Formats every per-currency total, in the order given, joined for one line. */
export function formatCostTotals(totals: readonly CostTotal[]): string {
  return totals.map(formatCost).join(' + ');
}
