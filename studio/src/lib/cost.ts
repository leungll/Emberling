import type { Cost, CostTotal } from '@/api/types';

/**
 * Formats a decimal cost amount in its currency for display, to two fraction digits. The
 * amount arrives as text with at most six fraction digits, so the Number conversion is exact
 * for every value the backend stores. A charge above zero that would round to nothing shows
 * as less than the smallest shown unit ("<$0.01"), so a tiny model call never reads as free;
 * a reported zero stays "$0.00". A currency the formatter does not know, or text that is not
 * a number, falls back to the raw amount with its code.
 */
export function formatCost({ amount, currency }: Pick<Cost, 'amount' | 'currency'>): string {
  const value = Number(amount);
  if (!/^\d+(\.\d+)?$/.test(amount) || !Number.isFinite(value)) return `${amount} ${currency}`;
  try {
    const money = new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency,
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    });
    const smallest = 10 ** -(money.resolvedOptions().maximumFractionDigits ?? 2);
    if (value > 0 && value < smallest / 2) return `<${money.format(smallest)}`;
    return money.format(value);
  } catch {
    return `${amount} ${currency}`;
  }
}

/** Formats every per-currency total, in the order given, joined for one line. */
export function formatCostTotals(totals: readonly CostTotal[]): string {
  return totals.map(formatCost).join(' + ');
}
