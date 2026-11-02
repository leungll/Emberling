import { describe, expect, it } from 'vitest';

import { formatCost, formatCostTotals } from './cost';

describe('formatCost', () => {
  it('formats a dollar charge with at least cents', () => {
    expect(formatCost({ amount: '0.04', currency: 'USD' })).toBe('$0.04');
    expect(formatCost({ amount: '2', currency: 'USD' })).toBe('$2.00');
  });

  it('keeps every reported digit of a tiny charge instead of rounding it to free', () => {
    expect(formatCost({ amount: '0.000032', currency: 'USD' })).toBe('$0.000032');
  });

  it('shows a reported zero as a price, distinct from no price at all', () => {
    expect(formatCost({ amount: '0', currency: 'USD' })).toBe('$0.00');
  });

  it('falls back to the raw amount and code for text it cannot format', () => {
    expect(formatCost({ amount: '1e-3', currency: 'USD' })).toBe('1e-3 USD');
    expect(formatCost({ amount: '1.5', currency: 'NOT_A_CODE' })).toBe('1.5 NOT_A_CODE');
  });

  it('joins several currencies on one line', () => {
    expect(
      formatCostTotals([
        { amount: '1.5', currency: 'EUR' },
        { amount: '0.084032', currency: 'USD' },
      ]),
    ).toBe('€1.50 + $0.084032');
    expect(formatCostTotals([])).toBe('');
  });
});
