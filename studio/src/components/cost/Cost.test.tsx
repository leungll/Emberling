import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { Money, RunCostValue } from './Cost';

describe('Money', () => {
  it('shows a reported charge', () => {
    render(<Money cost={{ amount: '0.04', currency: 'USD', source: 'image-model-v1' }} />);
    expect(screen.getByText('$0.04')).toBeInTheDocument();
  });

  it('shows a reported zero as a price', () => {
    render(<Money cost={{ amount: '0', currency: 'USD', source: 'text-model-v1' }} />);
    expect(screen.getByText('$0.00')).toBeInTheDocument();
  });

  it('renders nothing for a call whose charge is not known', () => {
    const { container } = render(<Money cost={null} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe('RunCostValue', () => {
  it('shows the total and, under it, the calls that reported no price', () => {
    render(
      <RunCostValue cost={{ totals: [{ amount: '0.04', currency: 'USD' }], unknownCalls: 2 }} />,
    );
    expect(screen.getByText('$0.04')).toBeInTheDocument();
    expect(screen.getByText('2 calls not priced')).toBeInTheDocument();
  });

  it('shows only the unpriced calls when nothing was priced', () => {
    render(<RunCostValue cost={{ totals: [], unknownCalls: 1 }} />);
    expect(screen.getByText('1 call not priced')).toBeInTheDocument();
    expect(screen.queryByText(/\$/)).not.toBeInTheDocument();
  });

  it('shows a known zero total, which is not the same as no price', () => {
    render(<RunCostValue cost={{ totals: [{ amount: '0', currency: 'USD' }], unknownCalls: 0 }} />);
    expect(screen.getByText('$0.00')).toBeInTheDocument();
    expect(screen.queryByText(/not priced/)).not.toBeInTheDocument();
  });

  it('renders nothing for a run that made no priced or unpriced call', () => {
    const { container } = render(<RunCostValue cost={{ totals: [], unknownCalls: 0 }} />);
    expect(container).toBeEmptyDOMElement();
  });
});
